package proxy

import (
	"encoding/json"
	"io"
	"log"
	"testing"
)

func TestCapsCache_GetSetRoundTrip(t *testing.T) {
	c := NewCapsCache()
	if _, ok := c.Get("thread-1"); ok {
		t.Fatal("fresh cache must not return hit")
	}
	c.Set("thread-1", []byte(`[{"id":1}]`))
	got, ok := c.Get("thread-1")
	if !ok {
		t.Fatal("expected cache hit after Set")
	}
	if string(got) != `[{"id":1}]` {
		t.Errorf("cache returned %q, want %q", got, `[{"id":1}]`)
	}
}

func TestCapsCache_InvalidateRemovesEntry(t *testing.T) {
	c := NewCapsCache()
	c.Set("thread-1", []byte(`[{"id":1}]`))
	c.Invalidate("thread-1")
	if _, ok := c.Get("thread-1"); ok {
		t.Error("entry must be removed after Invalidate")
	}
}

func TestCapsCache_InvalidateOnlyAffectsTargetThread(t *testing.T) {
	c := NewCapsCache()
	c.Set("thread-A", []byte(`[{"id":1}]`))
	c.Set("thread-B", []byte(`[{"id":2}]`))
	c.Invalidate("thread-A")
	if _, ok := c.Get("thread-A"); ok {
		t.Error("thread-A must be invalidated")
	}
	if _, ok := c.Get("thread-B"); !ok {
		t.Error("thread-B must survive invalidation of thread-A")
	}
}

func TestCachedQueryFn_CachesGetActiveCapabilities(t *testing.T) {
	cache := NewCapsCache()
	calls := 0
	upstream := func(method string, params map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`[{"id":1,"meta":{"cap_name":"x"}}]`), nil
	}
	queryFn := cachedQueryFn(cache, "thread-xyz", upstream)

	r1, err := queryFn("get_active_caps", nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	r2, err := queryFn("get_active_caps", nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(r1) != string(r2) {
		t.Errorf("cached response differs from first: %s vs %s", r1, r2)
	}
	if calls != 1 {
		t.Errorf("upstream called %d times; want 1 (cache hit on second call)", calls)
	}
}

func TestCachedQueryFn_InvalidateForcesReQuery(t *testing.T) {
	cache := NewCapsCache()
	calls := 0
	upstream := func(method string, params map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`[{"id":1}]`), nil
	}
	queryFn := cachedQueryFn(cache, "thread-xyz", upstream)

	queryFn("get_active_caps", nil)
	cache.Invalidate("thread-xyz")
	queryFn("get_active_caps", nil)

	if calls != 2 {
		t.Errorf("after invalidate, upstream should be called again; calls=%d want 2", calls)
	}
}

func TestCachedQueryFn_NonCapabilitiesCallBypassesCache(t *testing.T) {
	cache := NewCapsCache()
	calls := 0
	upstream := func(method string, params map[string]any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}
	queryFn := cachedQueryFn(cache, "thread-xyz", upstream)

	queryFn("whoami", nil)
	queryFn("whoami", nil)

	if calls != 2 {
		t.Errorf("non-capabilities calls must bypass cache; calls=%d want 2", calls)
	}
	if _, ok := cache.Get("thread-xyz"); ok {
		t.Error("non-capabilities response must not be cached")
	}
}

func TestInvalidateThreadCaches_InvalidatesBothCapsCacheAndFrozenStubs(t *testing.T) {
	// Sawtooth refreeze (frozenStubs.Invalidate) is the natural refresh point
	// for capabilities — when the frozen prefix is rebuilt, the cached caps
	// rendering for that thread must also be dropped so the next inject picks
	// up any activate/deactivate changes.
	s := &Server{
		frozenStubs: NewFrozenStubs(),
		capsCache:   NewCapsCache(),
	}
	s.capsCache.Set("thread-A", []byte(`cached-caps-response`))
	s.frozenStubs.Store("thread-A", []any{}, 0, nil, 0, 0)
	if !s.frozenStubs.Has("thread-A") {
		t.Fatal("precondition: frozenStubs must have thread-A entry")
	}

	s.invalidateThreadCaches("thread-A", "", "")

	if _, ok := s.capsCache.Get("thread-A"); ok {
		t.Error("capsCache entry for thread-A must be invalidated")
	}
	if s.frozenStubs.Has("thread-A") {
		t.Error("frozenStubs entry for thread-A must be invalidated")
	}
}

func TestInvalidateThreadCaches_OnlyAffectsTargetThread(t *testing.T) {
	s := &Server{
		frozenStubs: NewFrozenStubs(),
		capsCache:   NewCapsCache(),
	}
	s.capsCache.Set("thread-A", []byte(`A`))
	s.capsCache.Set("thread-B", []byte(`B`))
	s.frozenStubs.Store("thread-A", []any{}, 0, nil, 0, 0)
	s.frozenStubs.Store("thread-B", []any{}, 0, nil, 0, 0)

	s.invalidateThreadCaches("thread-A", "", "")

	if _, ok := s.capsCache.Get("thread-B"); !ok {
		t.Error("thread-B capsCache must survive invalidation of thread-A")
	}
	if !s.frozenStubs.Has("thread-B") {
		t.Error("thread-B frozenStubs must survive invalidation of thread-A")
	}
}

func TestInvalidateThreadCaches_RefreshesBriefingWhenProjectSet(t *testing.T) {
	s := &Server{
		logger:    log.New(io.Discard, "", 0),
		capsCache: NewCapsCache(),
	}
	s.setCachedBriefing("t-1", "yesmem", "OLD-text", "OLD-cm")

	calls := 0
	var gotProj, gotDir string
	s.briefingLoader = func(proj, projectDir, threadID string) briefingData {
		calls++
		gotProj, gotDir = proj, projectDir
		return briefingData{Text: "NEW-text", CodeMap: "NEW-cm"}
	}

	s.invalidateThreadCaches("t-1", "yesmem", "/home/user/memory/yesmem")

	if calls != 1 {
		t.Errorf("expected briefingLoader called once, got %d", calls)
	}
	if gotProj != "yesmem" || gotDir != "/home/user/memory/yesmem" {
		t.Errorf("loader args: proj=%q dir=%q", gotProj, gotDir)
	}
	text, cm, ok := s.getCachedBriefing("t-1", "yesmem")
	if !ok || text != "NEW-text" || cm != "NEW-cm" {
		t.Errorf("cache not refreshed: ok=%v text=%q cm=%q", ok, text, cm)
	}
}

func TestInvalidateThreadCaches_SkipsBriefingWhenProjectEmpty(t *testing.T) {
	s := &Server{
		logger:    log.New(io.Discard, "", 0),
		capsCache: NewCapsCache(),
	}

	calls := 0
	s.briefingLoader = func(proj, projectDir, threadID string) briefingData {
		calls++
		return briefingData{}
	}

	s.invalidateThreadCaches("t-2", "", "")

	if calls != 0 {
		t.Errorf("empty project must not trigger briefing loader, got %d calls", calls)
	}
}

func TestInvalidateThreadCaches_PreservesBriefingOnEmptyLoaderResult(t *testing.T) {
	s := &Server{
		logger:    log.New(io.Discard, "", 0),
		capsCache: NewCapsCache(),
	}
	s.setCachedBriefing("t-3", "yesmem", "OLD-text", "OLD-cm")

	s.briefingLoader = func(proj, projectDir, threadID string) briefingData {
		return briefingData{}
	}

	s.invalidateThreadCaches("t-3", "yesmem", "/home/user/memory/yesmem")

	text, cm, ok := s.getCachedBriefing("t-3", "yesmem")
	if !ok || text != "OLD-text" || cm != "OLD-cm" {
		t.Errorf("transient loader failure must preserve existing cache: ok=%v text=%q cm=%q", ok, text, cm)
	}
}
