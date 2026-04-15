package proxy

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxCacheBreakpoints = 4

// DefaultCacheTTL is the default cache duration hint.
// Empty/"ephemeral" means Anthropic's standard 5-minute TTL without explicit field.
const DefaultCacheTTL = "ephemeral"

// cacheControlBlockWithTTL returns the cache_control map with the given TTL.
func cacheControlBlockWithTTL(ttl string) map[string]any {
	if ttl == "" || ttl == "ephemeral" {
		return map[string]any{"type": "ephemeral"}
	}
	return map[string]any{"type": "ephemeral", "ttl": ttl}
}

// cacheControlBlock returns the standard cache_control map with default TTL.
func cacheControlBlock() map[string]any {
	return cacheControlBlockWithTTL(DefaultCacheTTL)
}

// InjectCacheBreakpoints adds cache_control breakpoints to system and tools blocks.
// Anthropic allows max 4 cache_control blocks per request. Claude Code may already
// set some, so we count existing ones and only add if budget remains.
//
// Priority: (1) last system block, (2) last tool definition.
// Returns the number of breakpoints injected.
func InjectCacheBreakpoints(req map[string]any, logger ...*log.Logger) int {
	existing := countCacheBreakpoints(req)

	// Debug: log where existing breakpoints are
	if len(logger) > 0 && logger[0] != nil {
		logCacheBreakpointLocations(req, logger[0])
	}

	budget := maxCacheBreakpoints - existing
	if budget <= 0 {
		return 0
	}

	cc := cacheControlBlock()
	injected := 0

	// Priority 1: last system block (most stable, biggest win)
	if budget > 0 {
		if added := injectSystemCache(req, cc); added {
			injected++
			budget--
		}
	}

	// Priority 2: last tool definition
	if budget > 0 {
		if added := injectToolsCache(req, cc); added {
			injected++
		}
	}

	return injected
}

// EnforceCacheBreakpointLimit removes surplus cache_control blocks until the request
// fits Anthropic's hard maximum. Lowest-priority blocks are trimmed first.
func EnforceCacheBreakpointLimit(req map[string]any, max int) int {
	holders := collectCacheControlHolders(req)
	if len(holders) <= max {
		return 0
	}

	sort.SliceStable(holders, func(i, j int) bool {
		if holders[i].priority != holders[j].priority {
			return holders[i].priority < holders[j].priority
		}
		return holders[i].path < holders[j].path
	})

	removed := 0
	for len(holders)-removed > max {
		delete(holders[removed].holder, "cache_control")
		removed++
	}
	return removed
}

// StripMessagesCacheControl removes ALL cache_control breakpoints from messages[start:end].
// Used to clean embedded CC breakpoints from frozen stubs before our pipeline
// sets exactly one breakpoint via InjectFrozenStubCacheBreakpoint.
func StripMessagesCacheControl(req map[string]any, start, end int) int {
	msgs, ok := req["messages"].([]any)
	if !ok {
		return 0
	}
	if end > len(msgs) {
		end = len(msgs)
	}
	removed := 0
	for i := start; i < end; i++ {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if _, has := b["cache_control"]; has {
				delete(b, "cache_control")
				removed++
			}
		}
	}
	return removed
}

// ShiftMessageBreakpoint moves the cache_control breakpoint from the last user message
// to the preceding assistant message. This keeps the last user message (which contains
// ephemeral injections like timestamps, system-reminders, and skill-eval that change
// between turns) in the write zone, preventing cache-breaking byte changes.
//
// Tool-result messages are excluded: they are stable cache anchors that CC does not
// modify between turns, so their breakpoint position is optimal.
func ShiftMessageBreakpoint(req map[string]any) bool {
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) < 2 {
		return false
	}

	lastMsg, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok || lastMsg["role"] != "user" {
		return false
	}

	// Tool-results are stable — don't shift
	if isToolResultMessage(lastMsg) {
		return false
	}

	// Find and remove cache_control from the last user message
	cc := removeCacheControl(lastMsg)
	if cc == nil {
		return false
	}

	// Find the previous assistant message (walk backwards)
	for i := len(msgs) - 2; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] == "assistant" {
			if addCacheControlToLastBlock(msg, cc) {
				return true
			}
			break
		}
	}

	// No suitable assistant found — restore on original
	restoreCacheControl(lastMsg, cc)
	return false
}

// isToolResultMessage returns true if the message contains tool_result content blocks.
func isToolResultMessage(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_result" || b["tool_use_id"] != nil {
			return true
		}
	}
	return false
}

// removeCacheControl finds and removes cache_control from a message's content blocks.
// Returns the removed cache_control value, or nil if none found.
func removeCacheControl(msg map[string]any) map[string]any {
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if cc, has := b["cache_control"]; has {
			ccMap, ok := cc.(map[string]any)
			if !ok {
				continue
			}
			delete(b, "cache_control")
			return ccMap
		}
	}
	return nil
}

// addCacheControlToLastBlock adds cache_control to the last content block of a message.
// Handles both string content (converts to block array) and existing block arrays.
func addCacheControlToLastBlock(msg map[string]any, cc map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		msg["content"] = []any{
			map[string]any{
				"type":          "text",
				"text":          content,
				"cache_control": cc,
			},
		}
		return true
	case []any:
		if len(content) == 0 {
			return false
		}
		lastBlock, ok := content[len(content)-1].(map[string]any)
		if !ok {
			return false
		}
		lastBlock["cache_control"] = cc
		return true
	}
	return false
}

// restoreCacheControl re-adds cache_control to the first content block of a message.
func restoreCacheControl(msg map[string]any, cc map[string]any) {
	content, ok := msg["content"].([]any)
	if !ok {
		return
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		b["cache_control"] = cc
		return
	}
}

// InjectFrozenStubCacheBreakpoint adds a cache_control breakpoint on the last content
// block of messages[frozenCount-1]. This creates a stable cache prefix covering all
// frozen stubs after a sawtooth collapse, restoring prompt cache efficiency.
// Without this, the CC messages-breakpoint is lost after collapse and only the system
// blocks (~38k) are cached instead of the full frozen prefix (~49k+).
//
// Must be called after frozen stubs are assembled in req["messages"].
// Returns true if a breakpoint was injected.
func InjectFrozenStubCacheBreakpoint(req map[string]any, frozenCount int, logger ...*log.Logger) bool {
	if frozenCount <= 0 {
		return false
	}
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) < frozenCount {
		return false
	}
	lastFrozen, ok := msgs[frozenCount-1].(map[string]any)
	if !ok {
		return false
	}
	content, ok := lastFrozen["content"]
	if !ok {
		return false
	}
	blocks, ok := content.([]any)
	if !ok || len(blocks) == 0 {
		return false
	}
	lastBlock, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		return false
	}
	if _, has := lastBlock["cache_control"]; has {
		return false // already has breakpoint, don't duplicate
	}
	lastBlock["cache_control"] = cacheControlBlock()
	return true
}

// IsAPIKeyAuth returns true if the request uses an API key (x-api-key header)
// rather than OAuth/subscription (Authorization: Bearer). TTL upgrades should
// only apply to API key auth — subscription users pay per-tier, not per-token.
func IsAPIKeyAuth(h http.Header) bool {
	return h.Get("x-api-key") != ""
}
// Claude Code sets {type: "ephemeral"} (5m default) — we upgrade or normalize all
// discovered blocks so Anthropic never sees mixed 5m/1h TTLs in request order.
func UpgradeCacheTTL(req map[string]any, ttl string) int {
	upgraded := 0
	for _, holder := range collectCacheControlHolders(req) {
		holder.holder["cache_control"] = cacheControlBlockWithTTL(ttl)
		if ttl == "" || ttl == "ephemeral" {
			if cc, ok := holder.holder["cache_control"].(map[string]any); ok {
				delete(cc, "ttl")
			}
		}
		upgraded++
	}
	return upgraded
}

func injectSystemCache(req map[string]any, cc map[string]any) bool {
	switch sys := req["system"].(type) {
	case []any:
		if len(sys) > 0 {
			if lastBlock, ok := sys[len(sys)-1].(map[string]any); ok {
				if _, has := lastBlock["cache_control"]; !has {
					lastBlock["cache_control"] = cc
					return true
				}
			}
		}
	case string:
		req["system"] = []any{
			map[string]any{
				"type":          "text",
				"text":          sys,
				"cache_control": cc,
			},
		}
		return true
	}
	return false
}

func injectToolsCache(req map[string]any, cc map[string]any) bool {
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		if lastTool, ok := tools[len(tools)-1].(map[string]any); ok {
			if _, has := lastTool["cache_control"]; !has {
				lastTool["cache_control"] = cc
				return true
			}
		}
	}
	return false
}

// countCacheBreakpoints counts existing cache_control blocks in the request.
func countCacheBreakpoints(req map[string]any) int {
	return len(collectCacheControlHolders(req))
}

// CacheGate tracks request timing and decides whether prompt caching is worthwhile.
// If requests come faster than the cache TTL (5 min), caching saves tokens.
// If slower, the 25% write surcharge makes it a net loss.
type CacheGate struct {
	lastRequest time.Time
	maxGap      time.Duration // disable caching if gap exceeds this
}

// NewCacheGate creates a gate with the given max gap (typically 4 min for 5 min TTL).
func NewCacheGate(maxGap time.Duration) *CacheGate {
	return &CacheGate{maxGap: maxGap}
}

// cacheGapForTTL returns the appropriate max gap for the given cache TTL config.
// Gap is TTL minus a safety margin to avoid requesting just as cache expires.
func cacheGapForTTL(ttl string) time.Duration {
	switch ttl {
	case "1h":
		return 61 * time.Minute // 1 min after TTL expires
	default: // "ephemeral" or empty = 5m TTL
		return 4 * time.Minute // 1 min safety margin
	}
}

// ShouldCache returns true if caching is expected to save tokens.
// It records the current time for subsequent calls.
func (g *CacheGate) ShouldCache() bool {
	now := time.Now()
	defer func() { g.lastRequest = now }()

	if g.lastRequest.IsZero() {
		// First request — enable caching to prime the cache
		return true
	}
	return now.Sub(g.lastRequest) < g.maxGap
}

// logCacheBreakpointLocations logs where cache_control breakpoints are set in the request.
func logCacheBreakpointLocations(req map[string]any, logger *log.Logger) {
	var locations []string

	ccStr := func(cc any) string {
		if m, ok := cc.(map[string]any); ok {
			if ttl, ok := m["ttl"].(string); ok {
				return fmt.Sprintf("[ttl=%s]", ttl)
			}
			return "[5m]"
		}
		return ""
	}

	for _, holder := range collectCacheControlHolders(req) {
		locations = append(locations, holder.path+ccStr(holder.holder["cache_control"]))
	}

	if len(locations) == 0 {
		logger.Printf("[prompt-cache] 0 breakpoints")
		return
	}
	logger.Printf("[prompt-cache] %d breakpoints at: %s", len(locations), strings.Join(locations, ", "))
}

type cacheControlHolder struct {
	holder   map[string]any
	path     string
	priority int
}

func collectCacheControlHolders(req map[string]any) []cacheControlHolder {
	holders := make([]cacheControlHolder, 0, 4)
	for _, key := range []string{"system", "tools", "messages"} {
		if value, ok := req[key]; ok {
			collectCacheControlHoldersAt(value, key, &holders)
		}
	}

	var extraKeys []string
	for key := range req {
		if key == "system" || key == "tools" || key == "messages" {
			continue
		}
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		collectCacheControlHoldersAt(req[key], key, &holders)
	}
	return holders
}

func collectCacheControlHoldersAt(value any, path string, holders *[]cacheControlHolder) {
	switch v := value.(type) {
	case []any:
		for i, item := range v {
			collectCacheControlHoldersAt(item, fmt.Sprintf("%s[%d]", path, i), holders)
		}
	case map[string]any:
		if _, has := v["cache_control"]; has {
			*holders = append(*holders, cacheControlHolder{
				holder:   v,
				path:     describeCacheHolder(path, v),
				priority: cacheTrimPriority(path, v),
			})
		}

		var keys []string
		for key := range v {
			if key == "cache_control" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectCacheControlHoldersAt(v[key], path+"."+key, holders)
		}
	}
}

func describeCacheHolder(path string, holder map[string]any) string {
	if name, ok := holder["name"].(string); ok && name != "" {
		return path + " " + name
	}
	if text, ok := holder["text"].(string); ok && text != "" {
		if idx := strings.IndexByte(text, '\n'); idx > 0 {
			return path + " " + text[:idx]
		}
		if len(text) <= 32 {
			return path + " " + text
		}
	}
	if role, ok := holder["role"].(string); ok && role != "" {
		return path + " " + role
	}
	if typ, ok := holder["type"].(string); ok && typ != "" {
		return path + " " + typ
	}
	return path
}

func cacheTrimPriority(path string, holder map[string]any) int {
	text, _ := holder["text"].(string)
	switch {
	case strings.Contains(text, "[yesmem-briefing]"):
		return 0
	case strings.HasPrefix(path, "tools["):
		return 1
	case strings.Contains(text, "[yesmem-"):
		return 2
	case strings.HasPrefix(path, "system["):
		return 3
	case strings.HasPrefix(path, "messages["):
		return 4
	default:
		return 5
	}
}
