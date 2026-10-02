package proxy

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

// Cache is keyed by (threadID, project). Each Claude Code session thread
// gets its own snapshot so a refresh on one thread cannot invalidate
// another thread's cached messages prefix and blow the Anthropic prompt cache.

func TestBriefingCache_HitOnSameThreadAndProject(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "yesmem-brief", "yesmem-cm")

	text, cm, ok := s.getCachedBriefing("tid-A", "yesmem")
	if !ok {
		t.Fatalf("expected cache hit for same thread+project, got miss")
	}
	if text != "yesmem-brief" {
		t.Errorf("text = %q, want %q", text, "yesmem-brief")
	}
	if cm != "yesmem-cm" {
		t.Errorf("codemap = %q, want %q", cm, "yesmem-cm")
	}
}

func TestBriefingCache_MissOnDifferentProject(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "yesmem-brief", "yesmem-cm")

	text, cm, ok := s.getCachedBriefing("tid-A", "erecht24api")
	if ok {
		t.Fatalf("expected cache miss for different project, got hit (text=%q cm=%q)", text, cm)
	}
	if text != "" || cm != "" {
		t.Errorf("miss should return empty strings, got text=%q cm=%q", text, cm)
	}
}

func TestBriefingCache_MissWhenEmpty(t *testing.T) {
	s := &Server{}

	text, cm, ok := s.getCachedBriefing("tid-A", "yesmem")
	if ok {
		t.Fatalf("expected cache miss for empty server, got hit")
	}
	if text != "" || cm != "" {
		t.Errorf("miss should return empty strings, got text=%q cm=%q", text, cm)
	}
}

// RED test for the per-thread scoping bug: two threads sharing one project
// must each keep their own snapshot. A set on thread B must not leak to thread A.
func TestBriefingCache_IsolatedBetweenThreads(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "A-text", "A-cm")
	s.setCachedBriefing("tid-B", "yesmem", "B-text", "B-cm")

	textA, cmA, okA := s.getCachedBriefing("tid-A", "yesmem")
	if !okA || textA != "A-text" || cmA != "A-cm" {
		t.Errorf("tid-A must keep its own entry after tid-B writes: ok=%v text=%q cm=%q",
			okA, textA, cmA)
	}
	textB, cmB, okB := s.getCachedBriefing("tid-B", "yesmem")
	if !okB || textB != "B-text" || cmB != "B-cm" {
		t.Errorf("tid-B entry wrong: ok=%v text=%q cm=%q", okB, textB, cmB)
	}
}

// Same-thread project-switch still evicts the stale entry for THAT thread.
// (A thread may switch working directory; old-project cache must not bleed in.)
func TestBriefingCache_EvictsPreviousProjectForSameThread(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "A-text", "A-cm")
	s.setCachedBriefing("tid-A", "erecht24api", "B-text", "B-cm")

	if _, _, ok := s.getCachedBriefing("tid-A", "yesmem"); ok {
		t.Errorf("old project for same thread must be evicted after project switch")
	}
	text, cm, ok := s.getCachedBriefing("tid-A", "erecht24api")
	if !ok || text != "B-text" || cm != "B-cm" {
		t.Errorf("new-project cache wrong: ok=%v text=%q cm=%q", ok, text, cm)
	}
}

func TestBriefingCache_IgnoresEmptyProject(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "real-text", "real-cm")

	s.setCachedBriefing("tid-A", "", "garbage", "garbage-cm")

	text, cm, ok := s.getCachedBriefing("tid-A", "yesmem")
	if !ok {
		t.Fatalf("yesmem cache should survive a setCachedBriefing call with empty project, got miss")
	}
	if text != "real-text" || cm != "real-cm" {
		t.Errorf("empty-project write must not overwrite: text=%q cm=%q", text, cm)
	}

	if _, _, ok := s.getCachedBriefing("tid-A", ""); ok {
		t.Errorf("empty project name must never produce a cache hit")
	}
}

func TestBriefingCache_IgnoresEmptyThreadID(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "real-text", "real-cm")

	s.setCachedBriefing("", "yesmem", "garbage", "garbage-cm")

	text, cm, ok := s.getCachedBriefing("tid-A", "yesmem")
	if !ok {
		t.Fatalf("tid-A cache should survive a setCachedBriefing call with empty threadID, got miss")
	}
	if text != "real-text" || cm != "real-cm" {
		t.Errorf("empty-threadID write must not overwrite: text=%q cm=%q", text, cm)
	}

	if _, _, ok := s.getCachedBriefing("", "yesmem"); ok {
		t.Errorf("empty threadID must never produce a cache hit")
	}
}

// Evicting thread A must not touch thread B.
func TestBriefingCache_InvalidatePerThread(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "A-text", "A-cm")
	s.setCachedBriefing("tid-B", "yesmem", "B-text", "B-cm")

	s.invalidateBriefingForThread("tid-A")

	if _, _, ok := s.getCachedBriefing("tid-A", "yesmem"); ok {
		t.Errorf("tid-A should be evicted by invalidateBriefingForThread")
	}
	textB, cmB, okB := s.getCachedBriefing("tid-B", "yesmem")
	if !okB || textB != "B-text" || cmB != "B-cm" {
		t.Errorf("tid-B must survive tid-A invalidation: ok=%v text=%q cm=%q",
			okB, textB, cmB)
	}
}

// refreshBriefing uses the (threadID, project, projectDir) tuple; only the
// given thread's cache entry should be updated.
func TestRefreshBriefing_UpdatesOnlyThatThread(t *testing.T) {
	s := &Server{
		logger: log.New(io.Discard, "", 0),
		briefingLoader: func(project, projectDir, threadID string) briefingData {
			return briefingData{Text: "fresh-" + project, CodeMap: "fresh-cm-" + project}
		},
	}
	s.setCachedBriefing("tid-A", "yesmem", "old-A", "old-cm-A")
	s.setCachedBriefing("tid-B", "yesmem", "old-B", "old-cm-B")

	s.refreshBriefing("tid-A", "yesmem", "/tmp/yesmem")

	textA, cmA, _ := s.getCachedBriefing("tid-A", "yesmem")
	if textA != "fresh-yesmem" || cmA != "fresh-cm-yesmem" {
		t.Errorf("tid-A should be refreshed: text=%q cm=%q", textA, cmA)
	}
	textB, cmB, _ := s.getCachedBriefing("tid-B", "yesmem")
	if textB != "old-B" || cmB != "old-cm-B" {
		t.Errorf("tid-B must be untouched by refresh on tid-A: text=%q cm=%q", textB, cmB)
	}
}

// TestComposeBriefingText_AppendsRenderedNarrative verifies the narrative-in-briefing
// integration: when Narrative has accumulated state (Render returns non-empty), the
// composed briefing text contains both the base text and the rendered narrative so
// the single cached turn-pair carries both. Refresh on sawtooth re-runs composition,
// in-between requests reuse the cached byte-stable text.
func TestComposeBriefingText_AppendsRenderedNarrative(t *testing.T) {
	n := NewNarrative()
	n.Update([]any{
		map[string]any{"role": "user", "content": "implement feature X"},
	})

	narr := n.Render()
	if narr == "" {
		t.Fatalf("precondition failed: narrative rendered empty, cannot exercise append path")
	}

	got := composeBriefingText("BASE BRIEFING", n)

	if !strings.Contains(got, "BASE BRIEFING") {
		t.Errorf("composed text missing base: %q", got)
	}
	if !strings.Contains(got, narr) {
		t.Errorf("composed text missing narrative: %q", got)
	}
}

// TestComposeBriefingText_SkipsEmptyNarrative guards against an empty narrative
// tail breaking the cached-briefing byte-stability: if Render returns "", the
// base text is returned unchanged (no trailing whitespace, no separator).
func TestComposeBriefingText_SkipsEmptyNarrative(t *testing.T) {
	n := NewNarrative()

	got := composeBriefingText("BASE BRIEFING", n)

	if got != "BASE BRIEFING" {
		t.Errorf("expected base unchanged when narrative empty, got: %q", got)
	}
}

// TestComposeBriefingText_NilNarrativeSafe documents the nil-guard contract used
// during bootstrap and in tests that construct Server without a narrative field.
func TestComposeBriefingText_NilNarrativeSafe(t *testing.T) {
	got := composeBriefingText("BASE", nil)
	if got != "BASE" {
		t.Errorf("nil narrative should return base unchanged, got: %q", got)
	}
}

func briefingTestRequest() map[string]any {
	return map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
}

// ageBriefingEntry moves a thread's cache entry into the past, as if it was
// loaded `age` ago.
func ageBriefingEntry(s *Server, threadID string, age time.Duration) {
	s.briefingMu.Lock()
	defer s.briefingMu.Unlock()
	e := s.briefingCache[threadID]
	e.loadedAt = time.Now().Add(-age)
	s.briefingCache[threadID] = e
}

// Regression: a failed first load (e.g. daemon i/o timeout right after deploy)
// was cached as a valid empty entry, so the thread never got a briefing again.
// Within the backoff window no reload happens (a hung daemon must not block
// every request for the full RPC timeout); after it, the load is retried.
func TestInjectBriefingTurn_RetriesAfterFailedLoad(t *testing.T) {
	calls := 0
	s := &Server{
		logger: log.New(io.Discard, "", 0),
		briefingLoader: func(project, projectDir, threadID string) briefingData {
			calls++
			if calls == 1 {
				return briefingData{} // simulated daemon timeout
			}
			return briefingData{Text: "BRIEF-TEXT", CodeMap: "BRIEF-CM"}
		},
	}

	if s.injectBriefingTurn(briefingTestRequest(), 1, "yesmem", "tid-A") {
		t.Fatalf("first request: no injection expected on failed load")
	}
	if s.injectBriefingTurn(briefingTestRequest(), 2, "yesmem", "tid-A") {
		t.Fatalf("second request within backoff: no injection expected")
	}
	if calls != 1 {
		t.Fatalf("within backoff the loader must not be called again, got %d calls", calls)
	}

	ageBriefingEntry(s, "tid-A", briefingRetryBackoff+time.Second)

	req := briefingTestRequest()
	if !s.injectBriefingTurn(req, 3, "yesmem", "tid-A") {
		t.Fatalf("after backoff the briefing must be reloaded and injected")
	}
	if calls != 2 {
		t.Errorf("expected exactly one retry, got %d loader calls", calls)
	}
	msgs := req["messages"].([]any)
	first, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(first, "BRIEF-TEXT") {
		t.Errorf("injected turn missing briefing text: %q", first)
	}
	if _, cm, ok := s.getCachedBriefing("tid-A", "yesmem"); !ok || cm != "BRIEF-CM" {
		t.Errorf("retry must cache code map too: ok=%v cm=%q", ok, cm)
	}
}

// A successfully loaded briefing stays cached for the thread's lifetime —
// expiring it would change the message prefix and bust the prompt cache.
func TestInjectBriefingTurn_SuccessfulLoadDoesNotExpire(t *testing.T) {
	calls := 0
	s := &Server{
		logger: log.New(io.Discard, "", 0),
		briefingLoader: func(project, projectDir, threadID string) briefingData {
			calls++
			return briefingData{Text: "BRIEF-TEXT"}
		},
	}

	if !s.injectBriefingTurn(briefingTestRequest(), 1, "yesmem", "tid-A") {
		t.Fatalf("first request: injection expected")
	}
	ageBriefingEntry(s, "tid-A", briefingRetryBackoff+time.Hour)
	if !s.injectBriefingTurn(briefingTestRequest(), 2, "yesmem", "tid-A") {
		t.Fatalf("second request: injection from cache expected")
	}
	if calls != 1 {
		t.Errorf("successful briefing must not be reloaded, got %d loader calls", calls)
	}
}

// Only a fully empty entry (the shape of a failed daemon load) is retried.
// An entry with a code map but empty text is a valid daemon answer; reloading
// it every backoff period could change the injected code-map bytes and bust
// the prompt cache.
func TestInjectBriefingTurn_CodeMapOnlyEntryDoesNotExpire(t *testing.T) {
	calls := 0
	s := &Server{
		logger: log.New(io.Discard, "", 0),
		briefingLoader: func(project, projectDir, threadID string) briefingData {
			calls++
			return briefingData{CodeMap: "CM-ONLY"}
		},
	}

	s.injectBriefingTurn(briefingTestRequest(), 1, "yesmem", "tid-A")
	ageBriefingEntry(s, "tid-A", briefingRetryBackoff+time.Second)
	s.injectBriefingTurn(briefingTestRequest(), 2, "yesmem", "tid-A")

	if calls != 1 {
		t.Errorf("code-map-only entry must not be reloaded, got %d loader calls", calls)
	}
}

// A late failed load (e.g. a 30s daemon timeout finishing after a concurrent
// refresh stored a good briefing) must not downgrade the entry to empty.
func TestBriefingCache_EmptyWriteDoesNotReplaceGoodEntry(t *testing.T) {
	s := &Server{}
	s.setCachedBriefing("tid-A", "yesmem", "GOOD", "GOOD-CM")

	s.setCachedBriefing("tid-A", "yesmem", "", "")

	text, cm, ok := s.getCachedBriefing("tid-A", "yesmem")
	if !ok || text != "GOOD" || cm != "GOOD-CM" {
		t.Errorf("empty write must not replace good entry: ok=%v text=%q cm=%q", ok, text, cm)
	}
}
