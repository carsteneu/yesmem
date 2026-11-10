package guardmodel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// modelList is a stand-in for GET /v1/models that counts how often it was asked.
func modelList(t *testing.T, entries ...map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, `{"error":"no key"}`, http.StatusUnauthorized)
			return
		}
		data := make([]map[string]string, 0, len(entries))
		data = append(data, entries...)
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func haiku(id, created string) map[string]string {
	return map[string]string{"id": id, "created_at": created}
}

func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YESMEM_GUARD_MODELS_URL", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	t.Setenv("YESMEM_GUARD_URL", "")
	t.Setenv("YESMEM_GUARD_KEY", "")
	t.Setenv("YESMEM_GUARD_NPM", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	return home
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestHaikusNewestFirst(t *testing.T) {
	home := tempHome(t)
	server, _ := modelList(t,
		haiku("claude-haiku-4-5-20251001", "2025-10-15T00:00:00Z"),
		haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"),
		haiku("claude-sonnet-5", "2026-10-08T00:00:00Z"),
	)
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	got := Haikus("sk-ant-test", "https://api.anthropic.com", home)

	// Only haiku ids, and the newer one first: the sonnet entry is not a guard candidate.
	want := []string{"claude-haiku-5-5", "claude-haiku-4-5-20251001"}
	if len(got) != len(want) {
		t.Fatalf("haikus = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("haikus = %v, want %v", got, want)
		}
	}
}

func TestHaikusAreCachedFor24Hours(t *testing.T) {
	home := tempHome(t)
	server, calls := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	Haikus("sk-ant-test", "https://api.anthropic.com", home)
	Haikus("sk-ant-test", "https://api.anthropic.com", home)

	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("model list asked %d times, want 1: a per-call request would cost a timeout on every tool call", n)
	}

	raw, err := os.ReadFile(CachePath(home))
	if err != nil {
		t.Fatalf("cache file: %v", err)
	}
	var cache struct {
		FetchedAt string   `json:"fetched_at"`
		Models    []string `json:"models"`
	}
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatalf("cache json: %v", err)
	}
	if len(cache.Models) != 1 || cache.Models[0] != "claude-haiku-5-5" {
		t.Fatalf("cached models = %v", cache.Models)
	}
	fetched, err := time.Parse(time.RFC3339, cache.FetchedAt)
	if err != nil {
		t.Fatalf("fetched_at %q is not RFC3339: %v", cache.FetchedAt, err)
	}
	if time.Since(fetched) > time.Minute {
		t.Fatalf("fetched_at = %s, want now", cache.FetchedAt)
	}
}

func TestHaikusRefetchWhenCacheIsStale(t *testing.T) {
	home := tempHome(t)
	server, calls := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)
	stale := time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	writeFileT(t, CachePath(home), `{"fetched_at":"`+stale+`","models":["claude-haiku-1"]}`)

	got := Haikus("sk-ant-test", "https://api.anthropic.com", home)

	if len(got) != 1 || got[0] != "claude-haiku-5-5" {
		t.Fatalf("haikus = %v, want the refetched one", got)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("model list asked %d times, want 1", n)
	}
}

func TestHaikusCacheAFailureAsEmpty(t *testing.T) {
	home := tempHome(t)
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, `{"error":"overloaded"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	if got := Haikus("sk-ant-test", "https://api.anthropic.com", home); len(got) != 0 {
		t.Fatalf("haikus = %v, want none", got)
	}
	Haikus("sk-ant-test", "https://api.anthropic.com", home)

	// The failure is remembered for the same window: otherwise every tool call retries a dead endpoint.
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("model list asked %d times, want 1", n)
	}
}

// The credential must not follow a redirect: the host it names is not the host the key belongs to.
func TestHaikusDoNotFollowARedirect(t *testing.T) {
	home := tempHome(t)
	var leaked int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" {
			atomic.AddInt32(&leaked, 1)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{}})
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	t.Setenv("YESMEM_GUARD_MODELS_URL", redirect.URL)

	if got := Haikus("sk-ant-test", "https://api.anthropic.com", home); len(got) != 0 {
		t.Fatalf("haikus = %v, want none from a redirect", got)
	}
	if n := atomic.LoadInt32(&leaked); n != 0 {
		t.Fatalf("the API key reached the redirect target %d times", n)
	}
}

func TestResolveModelFollowsTheChain(t *testing.T) {
	server, calls := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	home := tempHome(t)
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)
	registry := Registry{"anthropic": {
		API:    "https://api.anthropic.com",
		NPM:    AnthropicNPM,
		Models: map[string]ModelEntry{"claude-haiku-4-5-20251001": {ReleaseDate: "2025-10-15"}},
	}}

	t.Run("env wins and asks nothing", func(t *testing.T) {
		t.Setenv("YESMEM_GUARD_MODEL", "claude-haiku-9")
		model, fallbacks, source := ResolveModel("", "sk-ant-test", "https://api.anthropic.com", registry, home)
		if model != "claude-haiku-9" || source != SourceEnv || len(fallbacks) != 0 {
			t.Fatalf("model=%s fallbacks=%v source=%s, want the env model", model, fallbacks, source)
		}
		if n := atomic.LoadInt32(calls); n != 0 {
			t.Fatalf("model list asked %d times with an explicit model, want 0", n)
		}
	})

	t.Run("rule_guard.model wins over the account list", func(t *testing.T) {
		model, _, source := ResolveModel("claude-haiku-7-7", "sk-ant-test", "https://api.anthropic.com", registry, home)
		if model != "claude-haiku-7-7" || source != SourceConfig {
			t.Fatalf("model=%s source=%s, want the configured model", model, source)
		}
	})

	t.Run("the account list provides one fallback step", func(t *testing.T) {
		model, fallbacks, source := ResolveModel("", "sk-ant-test", "https://api.anthropic.com", registry, home)
		if model != "claude-haiku-5-5" || source != SourceAccount {
			t.Fatalf("model=%s source=%s, want the account's newest haiku", model, source)
		}
		if len(fallbacks) != 0 {
			t.Fatalf("fallbacks = %v, want none: the fixture lists a single haiku", fallbacks)
		}
	})

	t.Run("without a reachable list the registry decides", func(t *testing.T) {
		t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")
		other := t.TempDir()
		t.Setenv("HOME", other)
		model, _, source := ResolveModel("", "sk-ant-test", "https://api.anthropic.com", registry, other)
		if model != "claude-haiku-4-5-20251001" || source != SourceRegistry {
			t.Fatalf("model=%s source=%s, want the registry haiku", model, source)
		}
	})

	t.Run("with nothing at all the constant holds", func(t *testing.T) {
		t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")
		other := t.TempDir()
		model, _, source := ResolveModel("", "sk-ant-test", "https://api.anthropic.com", Registry{}, other)
		if model != FallbackModel || source != SourceFallback {
			t.Fatalf("model=%s source=%s, want %s", model, source, FallbackModel)
		}
	})
}

func TestTransport(t *testing.T) {
	cases := []struct {
		npm, base, want string
	}{
		{"@ai-sdk/anthropic", "https://api.anthropic.com", TransportAnthropic},
		{"", "https://api.anthropic.com", TransportAnthropic},
		{"@ai-sdk/anthropic", "https://gateway.example.com", TransportAnthropic},
		// A contradictory entry follows the host: api.anthropic.com serves only the messages format, so the
		// OpenAI shape would be a 404 whatever the entry declares.
		{"@ai-sdk/openai-compatible", "https://api.anthropic.com", TransportAnthropic},
		{"@ai-sdk/openai", "https://api.openai.com/v1", TransportOpenAI},
		{"@ai-sdk/openai-compatible", "https://llm.example.com", TransportOpenAI},
		{"", "https://api.deepseek.com", TransportOpenAI},
		{"", "", TransportOpenAI},
		{"", "not a url", TransportOpenAI},
	}
	for _, tc := range cases {
		if got := Transport(tc.npm, tc.base); got != tc.want {
			t.Errorf("Transport(%q, %q) = %s, want %s", tc.npm, tc.base, got, tc.want)
		}
	}
}
