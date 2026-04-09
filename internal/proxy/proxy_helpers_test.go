package proxy

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDeriveThreadID_StableWhenBillingHeaderChanges(t *testing.T) {
	// system[0] = billing header with changing cch= hash
	// system[2] = main prompt with stable "Primary working directory:"
	mkReq := func(cch string) map[string]any {
		return map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.86; cch=" + cch + ";"},
				map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI."},
				map[string]any{"type": "text", "text": "# Environment\n - Primary working directory: /home/user/myproject\n - Platform: linux"},
			},
			"messages": []any{
				map[string]any{"role": "user", "content": "hello"},
			},
		}
	}

	id1 := DeriveThreadID(mkReq("627d9"))
	id2 := DeriveThreadID(mkReq("c2bc9"))
	id3 := DeriveThreadID(mkReq("aaaaa"))

	if id1 == "" {
		t.Fatal("DeriveThreadID returned empty string")
	}
	if id1 != id2 {
		t.Errorf("threadID changed when cch= changed: %s vs %s", id1, id2)
	}
	if id1 != id3 {
		t.Errorf("threadID changed when cch= changed: %s vs %s", id1, id3)
	}
}

func TestDeriveThreadID_StableWhenNarrativeRequestCounterChanges(t *testing.T) {
	mkReq := func(reqNum string) map[string]any {
		return map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "x-anthropic-billing-header: cch=abc;"},
				map[string]any{"type": "text", "text": "# Environment\n - Primary working directory: /home/user/proj\n"},
				map[string]any{"type": "text", "text": "[yesmem-narrative]\nSession-Kontext (auto-generiert, Request " + reqNum + "):\nZiel: debugging"},
			},
			"messages": []any{
				map[string]any{"role": "user", "content": "test"},
			},
		}
	}

	id1 := DeriveThreadID(mkReq("17"))
	id2 := DeriveThreadID(mkReq("18"))
	id3 := DeriveThreadID(mkReq("99"))

	if id1 == "" {
		t.Fatal("DeriveThreadID returned empty string")
	}
	if id1 != id2 {
		t.Errorf("threadID changed with request counter: %s vs %s", id1, id2)
	}
	if id1 != id3 {
		t.Errorf("threadID changed with request counter: %s vs %s", id1, id3)
	}
}

func TestDeriveThreadID_UniquePerSession(t *testing.T) {
	mkReq := func(sessionID string) map[string]any {
		meta := map[string]any{}
		if sessionID != "" {
			userID, _ := json.Marshal(map[string]string{"session_id": sessionID})
			meta["user_id"] = string(userID)
		}
		return map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "# Environment\n - Primary working directory: /home/user/myproject\n"},
			},
			"messages": []any{
				map[string]any{"role": "user", "content": "hello"},
			},
			"metadata": meta,
		}
	}

	idA := DeriveThreadID(mkReq("session-aaa"))
	idB := DeriveThreadID(mkReq("session-bbb"))

	if idA == "" || idB == "" {
		t.Fatal("DeriveThreadID returned empty string")
	}
	if idA == idB {
		t.Fatalf("two sessions in same project got same threadID: %s", idA)
	}

	// Same session must be stable
	idA2 := DeriveThreadID(mkReq("session-aaa"))
	if idA != idA2 {
		t.Fatalf("same session unstable: %s vs %s", idA, idA2)
	}
}

func TestDeriveThreadID_StableWithoutSessionID(t *testing.T) {
	// Requests without metadata should still work (fallback)
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "# Environment\n - Primary working directory: /home/user/proj\n"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "test"},
		},
	}

	id1 := DeriveThreadID(req)
	id2 := DeriveThreadID(req)

	if id1 == "" {
		t.Fatal("DeriveThreadID returned empty without metadata")
	}
	if id1 != id2 {
		t.Fatalf("unstable without metadata: %s vs %s", id1, id2)
	}
}

func TestExtractSessionID_HeaderTakesPrecedence(t *testing.T) {
	userID, _ := json.Marshal(map[string]string{"session_id": "body-session"})
	req := map[string]any{
		"metadata": map[string]any{"user_id": string(userID)},
	}
	got := extractSessionID(req, "header-session")
	if got != "header-session" {
		t.Fatalf("expected header-session, got %s", got)
	}
}

func TestExtractSessionID_FallbackToBody(t *testing.T) {
	userID, _ := json.Marshal(map[string]string{"session_id": "body-session"})
	req := map[string]any{
		"metadata": map[string]any{"user_id": string(userID)},
	}
	got := extractSessionID(req, "")
	if got != "body-session" {
		t.Fatalf("expected body-session, got %s", got)
	}
}

func TestExtractSessionID_HeaderOnlyNoBody(t *testing.T) {
	req := map[string]any{}
	got := extractSessionID(req, "header-only")
	if got != "header-only" {
		t.Fatalf("expected header-only, got %s", got)
	}
}

func TestExtractSessionID_BothEmpty(t *testing.T) {
	req := map[string]any{}
	got := extractSessionID(req, "")
	if got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestEstimateTotalTokens_WithPreviousActual(t *testing.T) {
	s := &Server{}
	s.sawtoothTrigger = NewSawtoothTrigger(0, 200000, 80000)

	msgs := make([]any, 30)
	for i := range msgs {
		msgs[i] = map[string]any{"role": "user", "content": fmt.Sprintf("message %d with some content padding text", i)}
	}

	overhead := 5000

	// No previous actual → falls back to countMessageTokens
	total := s.estimateTotalTokens("t1", msgs, overhead)
	if total <= 0 {
		t.Fatal("should return positive estimate on first request")
	}

	// Simulate API response: 100k actual with 20 messages
	s.sawtoothTrigger.UpdateAfterResponse("t1", 100000, 20)

	// Now with 30 messages (10 new): should be ~100k + delta for 10 new messages
	totalWithActual := s.estimateTotalTokens("t1", msgs, overhead)

	// Must be > 100k (actual + delta for new messages)
	if totalWithActual <= 100000 {
		t.Fatalf("expected > 100k, got %d", totalWithActual)
	}

	// Must be much less than a full recount
	fullRecount := s.countMessageTokens(msgs) + overhead
	deltaFromActual := totalWithActual - 100000
	if deltaFromActual > fullRecount {
		t.Fatalf("actual-based estimate (%d) should produce smaller delta (%d) than full recount (%d)", totalWithActual, deltaFromActual, fullRecount)
	}
}

func TestEstimateTotalTokens_FallbackOnFirstRequest(t *testing.T) {
	s := &Server{}
	s.sawtoothTrigger = NewSawtoothTrigger(0, 200000, 80000)

	msgs := []any{
		map[string]any{"role": "user", "content": "hello world"},
	}

	// No previous actual → full count
	total := s.estimateTotalTokens("t1", msgs, 1000)
	expected := s.countMessageTokens(msgs) + 1000
	if total != expected {
		t.Fatalf("expected %d (full count fallback), got %d", expected, total)
	}
}

func TestEstimateTotalTokens_FallbackOnMessageShrink(t *testing.T) {
	s := &Server{}
	s.sawtoothTrigger = NewSawtoothTrigger(0, 200000, 80000)

	// Simulate: last response had 40 messages
	s.sawtoothTrigger.UpdateAfterResponse("t1", 200000, 40)

	// Now only 20 messages (e.g. new session or collapse)
	msgs := make([]any, 20)
	for i := range msgs {
		msgs[i] = map[string]any{"role": "user", "content": "x"}
	}

	total := s.estimateTotalTokens("t1", msgs, 1000)
	expected := s.countMessageTokens(msgs) + 1000
	if total != expected {
		t.Fatalf("shrink case: expected %d (full count fallback), got %d", expected, total)
	}
}
