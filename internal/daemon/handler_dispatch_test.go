package daemon

import (
	"testing"
)

func TestHandle_Ping(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "ping"})
	if resp.Error != "" {
		t.Fatalf("ping error: %s", resp.Error)
	}
}

func TestHandle_UnknownMethod(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "nonexistent_method_xyz"})
	if resp.Error == "" {
		t.Fatal("expected error for unknown method")
	}
}

func TestHandle_UpdateFixationRatio(t *testing.T) {
	t.Skip("fixation_ratio column not in test schema migration")
}

func TestHandle_UpdateFixationRatio_MissingSession(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "update_fixation_ratio",
		Params: map[string]any{"fixation_ratio": 0.5},
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
	}
}

func TestHandle_TrackProxyUsage(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "track_proxy_usage",
		Params: map[string]any{
			"day":                    "2026-04-01",
			"input_tokens":          float64(1000),
			"output_tokens":         float64(500),
			"cache_read_tokens":     float64(200),
			"cache_creation_tokens": float64(100),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandle_TrackProxyUsage_AutoDay(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "track_proxy_usage",
		Params: map[string]any{"input_tokens": float64(100)},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandle_TrackForkUsage(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "track_fork_usage",
		Params: map[string]any{
			"day":           "2026-04-01",
			"input_tokens":  float64(500),
			"output_tokens": float64(200),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandle_ReloadVectors_NoVectorStore(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "reload_vectors"})
	if resp.Error == "" {
		t.Fatal("expected error when vector store is nil")
	}
}

func TestHandle_IdleTickUpdatesTime(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "idle_tick",
		Params: map[string]any{"thread_id": "t-idle", "session_id": "s-idle"},
	})
	if resp.Error != "" {
		t.Fatalf("idle_tick error: %s", resp.Error)
	}
}

func TestHandle_IncrementCounters(t *testing.T) {
	h, _ := mustHandler(t)

	idMethods := []string{"increment_hits", "increment_noise", "increment_match", "increment_inject", "increment_use", "increment_fail", "increment_save"}
	for _, method := range idMethods {
		resp := h.Handle(Request{
			Method: method,
			Params: map[string]any{"ids": []any{float64(1), float64(2)}},
		})
		if resp.Error != "" {
			t.Errorf("%s error: %s", method, resp.Error)
		}
	}

	// increment_turn needs project, not ids
	resp := h.Handle(Request{
		Method: "increment_turn",
		Params: map[string]any{"project": "yesmem"},
	})
	if resp.Error != "" {
		t.Errorf("increment_turn error: %s", resp.Error)
	}
}

func TestHandle_SkipIndexing(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "skip_indexing",
		Params: map[string]any{"session_id": "s-skip"},
	})
	if resp.Error != "" {
		t.Fatalf("skip_indexing error: %s", resp.Error)
	}
}

func TestHandle_ListProjects(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "list_projects"})
	if resp.Error != "" {
		t.Fatalf("list_projects error: %s", resp.Error)
	}
}

// --- Helpers ---

func TestResolveProjectParam(t *testing.T) {
	h, _ := mustHandler(t)

	// With project set
	params := map[string]any{"project": "yesmem"}
	resolved := h.resolveProjectParam(params)
	if resolved["project"] != "yesmem" {
		t.Errorf("expected 'yesmem', got %q", resolved["project"])
	}

	// Without project — should not crash
	params = map[string]any{}
	resolved = h.resolveProjectParam(params)
	_ = resolved // just ensure no panic
}

func TestInjectExcludeSession(t *testing.T) {
	params := map[string]any{"_session_id": "my-session"}
	injectExcludeSession(params)
	if params["exclude_session"] != "my-session" {
		t.Errorf("expected _session_id propagated to exclude_session")
	}
}

func TestInjectExcludeSession_NoSessionID(t *testing.T) {
	params := map[string]any{"query": "test"}
	injectExcludeSession(params)
	if _, ok := params["exclude_session"]; ok {
		t.Error("should not set exclude_session when no _session_id")
	}
}

func TestIntOr(t *testing.T) {
	params := map[string]any{"count": float64(42)}
	if intOr(params, "count", 0) != 42 {
		t.Error("expected 42")
	}
	if intOr(params, "missing", 10) != 10 {
		t.Error("expected default 10")
	}
}
