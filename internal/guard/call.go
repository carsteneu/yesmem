package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/guardmodel"
)

// guardTTL is the response cache: identical calls within this window reuse the judgement. The hook runs once
// per tool call and gains little; the daemon serves many calls and gains a lot.
var guardTTL = 5 * time.Second

// maxGuardTokens bounds the answer. It is a short JSON object, but the violation strings are model text.
const maxGuardTokens = 4096

// maxAnswerBytes bounds what a model answer may hand to this process.
const maxAnswerBytes = 1 << 20

type cacheEntry struct {
	decision Decision
	ts       time.Time
}

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]cacheEntry)
)

// refusalRetry bounds how long a refused request is remembered. Short on purpose: it only stops an expired
// credential from costing a request per tool call, it is not a verdict about the call.
const refusalRetry = 60 * time.Second

var (
	refusalMu    sync.Mutex
	lastRefusal  Decision
	refusalKey   string
	refusalStamp time.Time
)

// freshRefusal returns the remembered refusal while it is young enough, for this endpoint and model only.
func freshRefusal(res *guardmodel.Resolution) (Decision, bool) {
	key := res.BaseURL + "\x00" + res.Model
	refusalMu.Lock()
	defer refusalMu.Unlock()
	if refusalKey != key || lastRefusal.Decision == "" || time.Since(refusalStamp) >= refusalRetry {
		return Decision{}, false
	}
	return lastRefusal, true
}

func rememberRefusal(res *guardmodel.Resolution, model string, decision Decision) {
	refusalMu.Lock()
	defer refusalMu.Unlock()
	refusalKey = res.BaseURL + "\x00" + model
	lastRefusal = decision
	refusalStamp = time.Now()
}

// resetRefusals is the test seam: one test must not inherit another's refusal.
func resetRefusals() {
	refusalMu.Lock()
	defer refusalMu.Unlock()
	lastRefusal = Decision{}
	refusalKey = ""
	refusalStamp = time.Time{}
}

// ResetState forgets the process-wide response cache and refusal lock. Test seam only: production lets both
// expire on their own.
func ResetState() {
	cacheMu.Lock()
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
	resetRefusals()
}

// judge asks the model, stepping one level down the account's haiku list when the endpoint no longer serves
// the chosen id. A stale model name is a name, not an outage; without the step the guard would report a
// retired id for as long as its 24h cache holds.
func judge(res *guardmodel.Resolution, req Request, rules string) Decision {
	// A refused request is remembered for a minute, per endpoint and model: a broken credential must not cost
	// a round trip on every tool call. The lock lives here because it is a property of the endpoint, not of a
	// harness's event stream.
	if refusal, ok := freshRefusal(res); ok {
		return refusal
	}
	candidates := []string{res.Model}
	if res.APIType == guardmodel.APITypeAnthropic {
		candidates = append(candidates, res.Fallbacks...)
	}
	for index, model := range candidates {
		decision, status := callModel(res, model, req, rules, maxOptionalLevel)
		if (status == http.StatusNotFound || status == http.StatusForbidden) && index+1 < len(candidates) {
			continue
		}
		// Remember the refusal only when nothing was left to try: a refused primary model with a working
		// fallback is not a refusal of the endpoint.
		if decision.Decision == "UNAVAILABLE" {
			rememberRefusal(res, model, decision)
		}
		return decision
	}
	return Decision{Decision: "PASS"}
}

// maxOptionalLevel is the start of the request ladder: both optional fields, the JSON answer format only,
// neither. An endpoint that refuses one of them gets the next level instead of a refusal.
const maxOptionalLevel = 2

// openAICompatNPMs are the provider modules whose endpoint may accept the JSON answer format.
var openAICompatNPMs = map[string]bool{
	"@ai-sdk/openai-compatible": true,
	"@ai-sdk/openai":            true,
	"@ai-sdk/mistral":           true,
}

// thinkingCapableHosts accept the DeepSeek-style `thinking` switch. The list is explicit because the parameter
// is not part of the openai-compatible surface: an endpoint that does not know it rejects the whole request,
// and that rejection used to be read as a PASS (YM-410). It cannot be derived from the provider's npm either.
var thinkingCapableHosts = map[string]bool{
	"api.deepseek.com": true,
	"api.z.ai":         true,
}

func cacheKey(rules, subject, tool, model, purpose string) string {
	sum := sha256.New()
	for _, part := range []string{rules, subject, tool, model, purpose} {
		sum.Write([]byte(part))
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// callModel performs one judgement call and returns the decision plus the HTTP status that produced it (0 for
// a transport error). The status is what lets the caller tell a retired model from an outage.
func callModel(res *guardmodel.Resolution, model string, req Request, rules string, optionalLevel int) (Decision, int) {
	tool := canonicalTool(req.Tool)
	text := subjectText(req)
	// The model and the purpose are part of the key: a resolution change must not hand out the old model's
	// verdict, and the two questions of the RPC must not answer each other.
	key := cacheKey(rules, text, tool, model, req.Purpose)

	cacheMu.RLock()
	entry, ok := cache[key]
	cacheMu.RUnlock()
	if ok && time.Since(entry.ts) < guardTTL {
		return entry.decision, http.StatusOK
	}

	body, headers, url := request(res, model, req, rules, optionalLevel)
	client := &http.Client{
		Timeout: 15 * time.Second,
		// The endpoint is configuration; a redirect would replay the credential at whatever host it names,
		// and x-api-key survives a cross-host jump in net/http. The model-list client refuses for the same
		// reason.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// Two attempts: a single network hiccup is not a judgement about the call.
	for attempt := 0; attempt < 2; attempt++ {
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return Decision{Decision: "PASS"}, 0
		}
		for name, value := range headers {
			httpReq.Header.Set(name, value)
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			// A timeout is not retried: the caller has already waited the full budget. Today's plugin behaved
			// the same way, and a second 15s wait would double the stall on a slow endpoint.
			if attempt == 0 && !isTimeout(err) {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// A transport failure is not a judgement, and not a refusal: the call passes with a reason the
			// harness can log. Only an answer the endpoint refused blocks anything (YM-410).
			detail := "endpoint unreachable"
			if isTimeout(err) {
				detail = "endpoint timed out"
			}
			return Decision{Decision: "PASS", Detail: detail, Model: model}, 0
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			// The optional request fields are an optimisation, the judgement is not. One step down the ladder
			// before the guard declares itself unavailable: otherwise a gateway that refuses response_format
			// locks every write (YM-410).
			if resp.StatusCode == http.StatusBadRequest && optionalLevel > 0 && openAICompatNPMs[res.NPM] {
				return callModel(res, model, req, rules, optionalLevel-1)
			}
			// A refused request is not an answered one. Passing it on is what let the guard judge nothing for
			// days in the plugin, with an HTTP 400 behind every PASS line (YM-410). The status is returned so
			// the caller can tell a retired model (404/403, one step down the list) from a broken credential.
			if rejectedStatuses[resp.StatusCode] {
				return Decision{
					Decision: "UNAVAILABLE",
					Detail:   fmt.Sprintf("HTTP %d from %s", resp.StatusCode, res.Host),
					Model:    model,
				}, resp.StatusCode
			}
			// A busy or broken endpoint may answer on the next try.
			if attempt == 0 {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			// A pass that was never judged says why: "the model saw no issue" and "the model was not reached"
			// must be tellable apart without parsing wording, because a caller caches the two differently.
			return Decision{
				Decision: "PASS",
				Detail:   fmt.Sprintf("HTTP %d from %s", resp.StatusCode, res.Host),
				Model:    model,
			}, resp.StatusCode
		}
		decision, parsed := parseAnswer(res, io.LimitReader(resp.Body, maxAnswerBytes))
		resp.Body.Close()
		decision.Model = model
		if parsed {
			cacheMu.Lock()
			cache[key] = cacheEntry{decision: decision, ts: time.Now()}
			cacheMu.Unlock()
			return decision, http.StatusOK
		}
		return decision, http.StatusOK
	}
	return Decision{Decision: "PASS"}, 0
}

// isTimeout reports whether a transport error was the deadline. A timeout has already spent the whole budget.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// rejectedStatuses are the answers that mean the endpoint refused this request rather than being busy: a bad
// parameter, a credential, a route, a validation. They do not fix themselves while the call runs.
var rejectedStatuses = map[int]bool{400: true, 401: true, 403: true, 404: true, 422: true}

func request(res *guardmodel.Resolution, model string, req Request, rules string, optionalLevel int) ([]byte, map[string]string, string) {
	systemPrompt, userPrompt := prompts(req, rules, subjectText(req))

	if res.APIType == guardmodel.APITypeAnthropic {
		// Anthropic carries the system prompt in its own field and the credential in x-api-key. It knows
		// neither response_format nor thinking; both would be a rejected request.
		body, _ := json.Marshal(map[string]any{
			"model":       model,
			"max_tokens":  maxGuardTokens,
			"temperature": 0,
			"system":      systemPrompt,
			"messages": []map[string]string{
				{"role": "user", "content": userPrompt},
			},
		})
		return body, map[string]string{
			"Content-Type":      "application/json",
			"x-api-key":         res.APIKey,
			"anthropic-version": "2023-06-01",
		}, res.RequestURL()
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0,
		"max_tokens":  maxGuardTokens,
	}
	if openAICompatNPMs[res.NPM] && optionalLevel >= 1 {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	if openAICompatNPMs[res.NPM] && optionalLevel >= 2 && thinkingCapableHosts[hostOnly(res.BaseURL)] {
		payload["thinking"] = map[string]string{"type": "disabled"}
	}
	body, _ := json.Marshal(payload)
	return body, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + res.APIKey,
	}, res.RequestURL()
}

// hostOnly is the host of an endpoint, which is what the capability list is keyed on.
func hostOnly(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// parseAnswer reads the decision out of either response shape. parsed is false when the answer carried no
// JSON decision at all, which is PASS with a reason rather than a judgement.
func parseAnswer(res *guardmodel.Resolution, body io.Reader) (Decision, bool) {
	var content string
	if res.APIType == guardmodel.APITypeAnthropic {
		var payload struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.NewDecoder(body).Decode(&payload) != nil || len(payload.Content) == 0 {
			return Decision{Decision: "PASS", Detail: "empty answer"}, false
		}
		content = payload.Content[0].Text
	} else {
		var payload struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.NewDecoder(body).Decode(&payload) != nil || len(payload.Choices) == 0 {
			return Decision{Decision: "PASS", Detail: "empty answer"}, false
		}
		content = payload.Choices[0].Message.Content
	}
	content = stripCodeFences(content)
	var decision Decision
	if json.Unmarshal([]byte(content), &decision) != nil {
		return Decision{Decision: "PASS", Detail: "answer is not JSON"}, false
	}
	return normalizeDecision(decision), true
}

// normalizeDecision keeps the answer inside the decision set. The prompt embeds RULES.md, so the answer is
// untrusted input: a fourth decision name invented there must not become a rejection, and a BLOCK without a
// violation is no BLOCK.
func normalizeDecision(d Decision) Decision {
	if d.Decision == "BLOCK" && len(d.Violations) > 0 {
		return d
	}
	if d.Decision == "SUGGEST" && d.Suggestion != "" {
		// A suggestion carries no violation. Only the core's own downgrades put violations on a SUGGEST, and
		// an adapter reads that as "advisory note, not a skill to activate"; a model answer may not pose as one.
		return Decision{Decision: "SUGGEST", Suggestion: d.Suggestion}
	}
	if d.Decision == "PASS" {
		return Decision{Decision: "PASS"}
	}
	return Decision{Decision: "PASS", Detail: "answer outside the decision set"}
}

// stripCodeFences removes markdown code fence markers from a model answer.
func stripCodeFences(content string) string {
	re := regexp.MustCompile("(?s)^```(?:json)?\\s*\n?(.*?)\n?```\\s*$")
	if matches := re.FindStringSubmatch(content); matches != nil {
		return strings.TrimSpace(matches[1])
	}
	return strings.TrimSpace(content)
}

// subjectText is what the model reads as the call: the described tool call plus whatever context the harness
// could add. The hook adds none; the plugin adds the recent conversation and the branch note. A nudge has no
// call to describe: there the message is the whole question.
func subjectText(req Request) string {
	if req.Purpose == PurposeNudge {
		return req.Context
	}
	text := describe(req)
	if strings.TrimSpace(req.Context) == "" {
		return text
	}
	return text + "\n" + req.Context
}

// prompts selects the template for the request's purpose. The system lines differ too: the nudge asks for a
// skill name, the guard for a judgement, and one wording cannot ask both.
func prompts(req Request, rules, subject string) (string, string) {
	if req.Purpose == PurposeNudge {
		return "You match a user message against a skill catalog. Output ONLY valid JSON. Do not reason, " +
			"explain, or think. No explanation.", buildNudgePrompt(rules, subject)
	}
	return "You analyze a proposed tool call against rules. Output ONLY valid JSON. Do not reason, " +
		"explain, or think. No explanation.", buildGuardPrompt(rules, subject, canonicalTool(req.Tool))
}

// buildGuardPrompt is the one prompt both harnesses send.
func buildGuardPrompt(rules, subject, tool string) string {
	return fmt.Sprintf(`You analyze a proposed tool call against rules. Output ONLY valid JSON.

RULES:
%s

PROPOSED TOOL CALL: %s

Response options:
%s- SUGGEST: a skill or best practice would help → {"decision":"SUGGEST","suggestion":"SkillName: short reason max 60 chars"}
- PASS: no issue → {"decision":"PASS"}

For SUGGEST, the suggestion field MUST contain the exact skill name (from the rules) followed by ': ' and a short reason. Evaluate ALL Skill Catalog rules against the task context. Check triggers for keyword matches.`, rules, subject, blockOption(tool))
}
