package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// newTestStore opens an in-memory SQLite store for testing.
func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertTestLearning inserts a gotcha learning and returns its ID.
func insertTestLearning(store *storage.Store, content, category string) int64 {
	id, err := store.InsertLearning(&models.Learning{
		Category:  category,
		Content:   content,
		Confidence: 1.0,
		CreatedAt: time.Now(),
		ModelUsed: "test",
	})
	if err != nil {
		panic("insertTestLearning: " + err.Error())
	}
	return id
}

func TestEmitReminderSilentWhenNoGotcha(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	emitReminder("")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)

	if buf.Len() > 0 {
		t.Errorf("emitReminder(\"\") should produce no output when no gotcha, got: %s", buf.String())
	}
}

func TestEmitReminderOutputsWhenGotchaFound(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	emitReminder("WARNING: git push fails in sandbox")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)

	if buf.Len() == 0 {
		t.Error("emitReminder with gotcha should produce output")
	}
}

func TestBlockThreshold(t *testing.T) {
	if blockThreshold < 1 {
		t.Fatalf("blockThreshold must be >= 1, got %d", blockThreshold)
	}
}

func TestFindBlockableGotcha(t *testing.T) {
	tests := []struct {
		name      string
		matches   []matchedGotcha
		wantBlock bool
	}{
		{
			name:      "no matches",
			matches:   nil,
			wantBlock: false,
		},
		{
			name: "hook_auto_learned with fail_count at threshold and high score blocks",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: 2, Source: "hook_auto_learned"}, score: 3},
				{learning: models.Learning{ID: 2, FailCount: 5, Source: "hook_auto_learned"}, score: 5},
			},
			wantBlock: true,
		},
		{
			name: "hook_auto_learned with fail_count below threshold does not block",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: blockThreshold - 1, Source: "hook_auto_learned"}, score: 3},
			},
			wantBlock: false,
		},
		{
			name: "llm_extracted with high fail_count does NOT block",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: 100, Source: "llm_extracted"}, score: 3},
			},
			wantBlock: false,
		},
		{
			name: "user_stated with high fail_count does NOT block",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: 50, Source: "user_stated"}, score: 3},
			},
			wantBlock: false,
		},
		{
			name: "high hit_count but zero fail_count does NOT block",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, HitCount: 1000, FailCount: 0, Source: "hook_auto_learned"}, score: 3},
			},
			wantBlock: false,
		},
		{
			name: "mixed sources only blocks hook_auto_learned with sufficient fail_count and score",
			matches: []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: 500, Source: "llm_extracted"}, score: 6},
				{learning: models.Learning{ID: 2, FailCount: 5, Source: "hook_auto_learned"}, score: 4},
			},
			wantBlock: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findBlockableGotcha(tt.matches)
			if tt.wantBlock && got == nil {
				t.Error("expected a blockable gotcha, got nil")
			}
			if !tt.wantBlock && got != nil {
				t.Errorf("expected no blockable gotcha, got ID=%d", got.learning.ID)
			}
		})
	}
}

func TestHashInputDeterministic(t *testing.T) {
	h1 := hashInput("cp yesmem /usr/local/bin/yesmem")
	h2 := hashInput("cp yesmem /usr/local/bin/yesmem")
	if h1 != h2 {
		t.Errorf("hashInput not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("hashInput should return 16-char hex, got %d chars: %q", len(h1), h1)
	}
	// Different input → different hash
	h3 := hashInput("mv -f yesmem-new /usr/local/bin/yesmem")
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestFindBlockableGotchaRequiresHighScore(t *testing.T) {
	// A gotcha with fail_count >= threshold but low match score should NOT block.
	// This prevents broad cross-matching (e.g., "go test ./ivf/" matching a gotcha for "go test ./embedding/").
	tests := []struct {
		name      string
		score     int
		wantBlock bool
	}{
		{"score 2 should NOT block", 2, false},
		{"score 3 should NOT block", 3, false},
		{"score 4 should block", 4, true},
		{"score 10 should block", 10, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := []matchedGotcha{
				{learning: models.Learning{ID: 1, FailCount: 10, Source: "hook_auto_learned"}, score: tt.score},
			}
			got := findBlockableGotcha(matches)
			if tt.wantBlock && got == nil {
				t.Error("expected block")
			}
			if !tt.wantBlock && got != nil {
				t.Error("expected no block")
			}
		})
	}
}

func TestSaveCountHeuristic(t *testing.T) {
	store := newTestStore(t)
	id := insertTestLearning(store, "cp schlaegt fehl weil binary offen", "gotcha")

	// Simulate: gotcha warned about "cp binary"
	idsJSON, _ := json.Marshal([]int64{id})
	store.SetProxyState("last_gotcha_ids", string(idsJSON))
	store.SetProxyState("last_gotcha_tool", "Bash")
	store.SetProxyState("last_gotcha_input_hash", hashInput("cp yesmem /usr/local/bin/yesmem"))

	// Next call: DIFFERENT command — gotcha saved us
	checkSaveCount(store, "Bash", hashInput("mv -f yesmem-new /usr/local/bin/yesmem"))

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.SaveCount != 1 {
		t.Errorf("expected save_count=1, got %d", l.SaveCount)
	}
}

func TestSaveCountNotBumpedWhenIgnored(t *testing.T) {
	store := newTestStore(t)
	id := insertTestLearning(store, "cp schlaegt fehl", "gotcha")

	idsJSON, _ := json.Marshal([]int64{id})
	store.SetProxyState("last_gotcha_ids", string(idsJSON))
	store.SetProxyState("last_gotcha_tool", "Bash")
	store.SetProxyState("last_gotcha_input_hash", hashInput("cp yesmem /usr/local/bin/"))

	// Same command — user ignored
	checkSaveCount(store, "Bash", hashInput("cp yesmem /usr/local/bin/"))

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.SaveCount != 0 {
		t.Errorf("expected save_count=0 (user ignored warning), got %d", l.SaveCount)
	}
}

func TestSaveCountClearsAfterCheck(t *testing.T) {
	store := newTestStore(t)
	id := insertTestLearning(store, "test gotcha", "gotcha")

	idsJSON, _ := json.Marshal([]int64{id})
	store.SetProxyState("last_gotcha_ids", string(idsJSON))
	store.SetProxyState("last_gotcha_tool", "Bash")
	store.SetProxyState("last_gotcha_input_hash", hashInput("old command"))

	// Trigger save
	checkSaveCount(store, "Bash", hashInput("new command"))

	// State should be cleared now
	val, _ := store.GetProxyState("last_gotcha_ids")
	if val != "" {
		t.Errorf("expected last_gotcha_ids cleared, got %q", val)
	}

	// Second call should NOT bump again
	checkSaveCount(store, "Bash", hashInput("another command"))
	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.SaveCount != 1 {
		t.Errorf("expected save_count=1 (not bumped twice), got %d", l.SaveCount)
	}
}

func TestSaveCountDifferentToolNoSave(t *testing.T) {
	store := newTestStore(t)
	id := insertTestLearning(store, "deploy gotcha", "gotcha")

	idsJSON, _ := json.Marshal([]int64{id})
	store.SetProxyState("last_gotcha_ids", string(idsJSON))
	store.SetProxyState("last_gotcha_tool", "Bash")
	store.SetProxyState("last_gotcha_input_hash", hashInput("cp binary /usr/local/bin/"))

	// Different tool type — unrelated, no save_count
	checkSaveCount(store, "Edit", hashInput("/some/file.go"))

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.SaveCount != 0 {
		t.Errorf("expected save_count=0 (different tool type), got %d", l.SaveCount)
	}
}

// TestWebFetchKeywords verifies that buildWebFetchKeywords produces domain keywords
// but NOT the tool name "webfetch" (which is noise — always present, matches everything).
func TestWebFetchKeywords(t *testing.T) {
	kw := buildWebFetchKeywords("https://www.reddit.com/r/golang")

	// "reddit.com" or "reddit" must be present so domain-tagged gotchas match.
	wantHas := []string{"www.reddit.com"}
	for _, want := range wantHas {
		found := false
		for _, k := range kw {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("buildWebFetchKeywords(reddit URL) = %v, missing %q", kw, want)
		}
	}

	// "webfetch" must NOT be present — it's noise.
	for _, k := range kw {
		if k == "webfetch" {
			t.Error("buildWebFetchKeywords must not include 'webfetch' — causes false positive matches against all WebFetch gotchas")
		}
	}
}

// TestWebFetchGotchaMatching verifies that a gotcha with entity "reddit.com"
// is matched when keywords are derived from a WebFetch call to reddit.com.
// This tests the V2 entity-matching path in check.go.
func TestWebFetchGotchaMatching(t *testing.T) {
	store := newTestStore(t)

	// Insert a V2 gotcha with reddit.com as entity (simulates a real learning)
	_, err := store.InsertLearning(&models.Learning{
		Category:   "gotcha",
		Content:    "Reddit-URLs via WebFetch sind geblockt — curl nutzen",
		Confidence: 1.0,
		CreatedAt:  time.Now(),
		ModelUsed:  "test",
		Entities:   []string{"WebFetch", "reddit.com"},
	})
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	// Keywords that RunCheck will derive from: WebFetch + reddit.com domain
	keywords := extractKeywords("WebFetch reddit.com")

	// Reload learning to get entities populated
	gotchas, err := store.GetActiveLearnings("gotcha", "", "", "")
	if err != nil || len(gotchas) == 0 {
		t.Fatalf("GetActiveLearnings: %v (len=%d)", err, len(gotchas))
	}

	// BatchLoad entities like RunCheck does
	ids := make([]int64, len(gotchas))
	for i, g := range gotchas {
		ids[i] = g.ID
	}
	entitiesMap := store.BatchLoadEntities(ids)
	for i := range gotchas {
		if ents, ok := entitiesMap[gotchas[i].ID]; ok {
			gotchas[i].Entities = ents
		}
	}

	// Verify that our gotcha is matched via entity or content matching
	matched := false
	for _, g := range gotchas {
		score := matchScore(keywords, g.Content)
		if score >= 2 || (score >= 1 && hasLongKeywordMatch(keywords, g.Content)) {
			matched = true
			break
		}
		if g.IsV2() {
			for _, entity := range g.Entities {
				entityLower := strings.ToLower(entity)
				for _, kw := range keywords {
					if strings.Contains(entityLower, kw) || strings.Contains(kw, entityLower) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
		}
		if matched {
			break
		}
	}

	if !matched {
		t.Errorf("WebFetch reddit.com gotcha (entity: reddit.com) was NOT matched by keywords %v — RunCheck would silently skip it", keywords)
	}
}

// TestRunCheckWebFetchProducesKeywords verifies that the WebFetch HookInput is parsed
// and produces keywords. Before the WebFetch case was added to RunCheck, this
// would silently return without processing any gotchas.
// This test uses buildWebFetchKeywords which is the extraction logic
// that the WebFetch case in RunCheck delegates to.
func TestRunCheckWebFetchProducesKeywords(t *testing.T) {
	rawURL := "https://www.reddit.com/r/golang/comments/foo"
	kw := buildWebFetchKeywords(rawURL)
	if len(kw) == 0 {
		t.Fatalf("buildWebFetchKeywords(%q) returned no keywords — WebFetch case is missing", rawURL)
	}
	// Domain "www.reddit.com" → must contain "reddit.com" or "reddit" keyword
	found := false
	for _, k := range kw {
		if strings.Contains(k, "reddit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildWebFetchKeywords(%q) = %v — no reddit-related keyword found", rawURL, kw)
	}
}
// TestWebFetchKeywordsNotMatchedByOldDefault verifies that BEFORE the WebFetch case
// was added, the default: return branch would have dropped the call entirely.
// This is a regression guard: WebFetch must NOT fall through to the default return.
// TestWebFetchInputStr verifies that buildWebFetchKeywords still produces
// domain keywords for gotcha matching (regression guard).
func TestWebFetchInputStr(t *testing.T) {
	keywords := buildWebFetchKeywords("https://reddit.com/r/something")

	if len(keywords) == 0 {
		t.Error("WebFetch reddit.com should produce at least one keyword for gotcha matching")
	}
	// Must contain domain keyword
	found := false
	for _, k := range keywords {
		if strings.Contains(k, "reddit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected reddit-related keyword, got %v", keywords)
	}
}

// TestWebFetchKeywordsNoFalsePositive verifies that a reddit gotcha does NOT
// match unrelated WebFetch calls. The tool name "webfetch" must not be a keyword
// since it always matches any WebFetch gotcha — making it useless as discriminator.
func TestWebFetchKeywordsNoFalsePositive(t *testing.T) {
	// Keywords from a non-reddit WebFetch call
	kw := buildWebFetchKeywords("https://example.com/page")

	// "webfetch" must NOT be a keyword — it's noise (always present for any WebFetch)
	for _, k := range kw {
		if k == "webfetch" {
			t.Errorf("buildWebFetchKeywords should not include tool name 'webfetch' as keyword — causes false positives against all WebFetch gotchas")
		}
	}

	// A reddit gotcha should NOT match
	redditGotcha := "WebFetch error: `WebFetch www.reddit.com` → Claude Code is unable to fetch from www.reddit.com"
	score := matchScore(kw, redditGotcha)
	if score >= 2 || (score >= 1 && hasLongKeywordMatch(kw, redditGotcha)) {
		t.Errorf("non-reddit WebFetch (example.com) should NOT match reddit gotcha via keyword matching, score=%d, keywords=%v", score, kw)
	}
}

func TestSaveCountNoPreviousGotcha(t *testing.T) {
	store := newTestStore(t)

	// No previous state set — should be a no-op (no panic, no error)
	checkSaveCount(store, "Bash", hashInput("some command"))

	// Verify no state was left behind
	val, _ := store.GetProxyState("last_gotcha_ids")
	if val != "" {
		t.Errorf("expected empty state, got %q", val)
	}
}

func TestSaveCountMultipleIDs(t *testing.T) {
	store := newTestStore(t)
	id1 := insertTestLearning(store, "gotcha one about cp", "gotcha")
	id2 := insertTestLearning(store, "gotcha two about binary", "gotcha")

	idsJSON, _ := json.Marshal([]int64{id1, id2})
	store.SetProxyState("last_gotcha_ids", string(idsJSON))
	store.SetProxyState("last_gotcha_tool", "Bash")
	store.SetProxyState("last_gotcha_input_hash", hashInput("cp binary /usr/local/bin/"))

	// Different command → both get save_count bumped
	checkSaveCount(store, "Bash", hashInput("mv -f binary-new /usr/local/bin/"))

	l1, _ := store.GetLearning(id1)
	l2, _ := store.GetLearning(id2)
	if l1.SaveCount != 1 {
		t.Errorf("id1: expected save_count=1, got %d", l1.SaveCount)
	}
	if l2.SaveCount != 1 {
		t.Errorf("id2: expected save_count=1, got %d", l2.SaveCount)
	}
}

func TestDecayAffectsMatchingDecision(t *testing.T) {
	keywords := []string{"proxy", "cache"}
	content := "proxy cache invalidation bug"
	score := matchScore(keywords, content)
	if score < 2 {
		t.Fatalf("precondition: matchScore should be >= 2, got %d", score)
	}

	tests := []struct {
		name        string
		injectCount int
		useCount    int
		saveCount   int
		wantMatch   bool
	}{
		{"fresh gotcha passes", 5, 0, 0, true},
		{"high-waste gotcha blocked", 1232, 0, 0, false},
		{"moderate-waste gotcha blocked", 100, 0, 0, false},
		{"good precision passes", 100, 15, 0, true},
		{"saves recover precision", 100, 0, 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eff := float64(score) * injectionDecay(tt.injectCount, tt.useCount, tt.saveCount)
			matched := eff >= 2.0
			if matched != tt.wantMatch {
				t.Errorf("score=%d decay=%.2f effective=%.2f matched=%v, want %v",
					score, injectionDecay(tt.injectCount, tt.useCount, tt.saveCount), eff, matched, tt.wantMatch)
			}
		})
	}
}

func TestDecayDoesNotBlockStrongMatch(t *testing.T) {
	keywords := []string{"proxy", "cache", "invalidation", "bug", "fix", "workaround", "timeout"}
	content := "proxy cache invalidation bug fix workaround for timeout issue"
	score := matchScore(keywords, content)
	if score < 7 {
		t.Fatalf("precondition: matchScore should be >= 7, got %d", score)
	}
	eff := float64(score) * injectionDecay(1232, 0, 0)
	if eff < 2.0 {
		t.Errorf("a very strong match (score=%d) should still pass even with max decay: effective=%.2f", score, eff)
	}
}
