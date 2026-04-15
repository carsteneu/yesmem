package proxy

import "testing"

func TestTimestampStore_StoreAndGet(t *testing.T) {
	ts := NewTimestampStore()
	ts.Store("t1", 5, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42", Delta: "4s"})

	meta, ok := ts.Get("t1", 5)
	if !ok {
		t.Fatal("expected stored entry")
	}
	if meta.Timestamp != "Di 2026-04-14 20:15:42" || meta.Delta != "4s" {
		t.Errorf("got ts=%q delta=%q", meta.Timestamp, meta.Delta)
	}
}

func TestTimestampStore_GetMiss(t *testing.T) {
	ts := NewTimestampStore()
	_, ok := ts.Get("t1", 999)
	if ok {
		t.Fatal("expected miss")
	}
}

func TestTimestampStore_ThreadIsolation(t *testing.T) {
	ts := NewTimestampStore()
	ts.Store("t1", 1, &TimestampMeta{Timestamp: "ts-t1"})
	ts.Store("t2", 1, &TimestampMeta{Timestamp: "ts-t2"})

	m1, _ := ts.Get("t1", 1)
	m2, _ := ts.Get("t2", 1)
	if m1.Timestamp == m2.Timestamp {
		t.Fatal("threads should be isolated")
	}
}

func TestTimestampStore_PersistAndLoad(t *testing.T) {
	var stored string
	ts1 := NewTimestampStore()
	ts1.SetPersistFunc(func(key, value string) { stored = value })
	ts1.Store("t1", 1, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42", Delta: "4s"})
	ts1.Store("t1", 2, &TimestampMeta{Timestamp: "Di 2026-04-14 20:16:00", Delta: "18s"})
	ts1.Persist("t1")

	if stored == "" {
		t.Fatal("persist func should have been called")
	}

	ts2 := NewTimestampStore()
	ts2.SetLoadFunc(func(key string) (string, bool) { return stored, true })
	ts2.Load("t1")

	meta, ok := ts2.Get("t1", 1)
	if !ok || meta.Timestamp != "Di 2026-04-14 20:15:42" {
		t.Errorf("load failed: ok=%v meta=%+v", ok, meta)
	}
	meta2, ok := ts2.Get("t1", 2)
	if !ok || meta2.Delta != "18s" {
		t.Errorf("load failed for entry 2: ok=%v", ok)
	}
}

func TestBuildMeta_WithTimestamp(t *testing.T) {
	meta := &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42", Delta: "4s"}
	got := BuildMeta(5, meta)
	want := "[Di 2026-04-14 20:15:42] [msg:5] [+4s]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildMeta_WithoutTimestamp(t *testing.T) {
	got := BuildMeta(3, nil)
	want := "[msg:3]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildMeta_TimestampNoDelta(t *testing.T) {
	meta := &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42"}
	got := BuildMeta(1, meta)
	want := "[Di 2026-04-14 20:15:42] [msg:1]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInjectTimestamps_PositionBased(t *testing.T) {
	ts := NewTimestampStore()
	// msg:3 = 3rd message overall (which is a user msg with stored ts)
	ts.Store("t1", 3, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42", Delta: "4s"})

	msgs := []any{
		map[string]any{"role": "user", "content": "first question"},                  // msg:1
		map[string]any{"role": "assistant", "content": "answer"},                     // msg:2 (skip)
		map[string]any{"role": "user", "content": "second question"},                 // msg:3 — has stored ts
		map[string]any{"role": "assistant", "content": "answer 2"},                   // msg:4 (skip)
		map[string]any{"role": "user", "content": "third question"},                  // msg:5 — current
	}

	n := InjectTimestamps(ts, "t1", msgs, 4, 0, 0) // endIdx=4: exclude current, offset=0, stubs=0
	if n != 4 {
		t.Fatalf("expected 4 injections (2 user + 2 assistant), got %d", n)
	}

	// msg:1 — no timestamp, just [msg:1]
	text1 := msgs[0].(map[string]any)["content"].(string)
	if text1 != "[msg:1] first question" {
		t.Errorf("msg:1 got %q", text1)
	}

	// msg:3 — has stored timestamp
	text2 := msgs[2].(map[string]any)["content"].(string)
	want2 := "[Di 2026-04-14 20:15:42] [msg:3] [+4s] second question"
	if text2 != want2 {
		t.Errorf("msg:3 got %q, want %q", text2, want2)
	}
}

func TestInjectTimestamps_ReAnnotatesExisting(t *testing.T) {
	ts := NewTimestampStore()
	ts.Store("t1", 1, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42"})

	msgs := []any{
		map[string]any{"role": "user", "content": "[msg:999] already annotated with wrong N"},
	}

	n := InjectTimestamps(ts, "t1", msgs, 1, 0, 0) // stubs=0 → fresh tail → re-annotate
	if n != 1 {
		t.Fatalf("expected 1 re-annotation, got %d", n)
	}
	text := msgs[0].(map[string]any)["content"].(string)
	want := "[Di 2026-04-14 20:15:42] [msg:1] already annotated with wrong N"
	if text != want {
		t.Errorf("got %q, want %q", text, want)
	}
}

func TestInjectTimestamps_ContentBlockArray(t *testing.T) {
	ts := NewTimestampStore()
	ts.Store("t1", 1, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42"})

	msgs := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "question with blocks"},
		}},
	}

	n := InjectTimestamps(ts, "t1", msgs, 1, 0, 0)
	if n != 1 {
		t.Fatalf("expected 1 injection, got %d", n)
	}

	blocks := msgs[0].(map[string]any)["content"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	want := "[Di 2026-04-14 20:15:42] [msg:1] question with blocks"
	if text != want {
		t.Errorf("got %q, want %q", text, want)
	}
}

func TestCountUserMessages(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "q1"},
		map[string]any{"role": "assistant", "content": "a1"},
		map[string]any{"role": "user", "content": "q2"},
		map[string]any{"role": "user", "content": "q3"},
	}
	if n := CountUserMessages(msgs, 4); n != 3 {
		t.Errorf("expected 3, got %d", n)
	}
	if n := CountUserMessages(msgs, 2); n != 1 {
		t.Errorf("expected 1 (up to idx 2), got %d", n)
	}
}

func TestInjectTimestamps_WithOffset(t *testing.T) {
	ts := NewTimestampStore()
	// msg:501 = offset(500) + array position 0 + 1
	ts.Store("t1", 501, &TimestampMeta{Timestamp: "Di 2026-04-14 20:15:42", Delta: "4s"})

	msgs := []any{
		map[string]any{"role": "user", "content": "fresh question 1"},   // msg:501
		map[string]any{"role": "assistant", "content": "answer"},        // msg:502 (skip)
		map[string]any{"role": "user", "content": "fresh question 2"},   // msg:503
	}

	n := InjectTimestamps(ts, "t1", msgs, 2, 500, 0) // offset=500, endIdx=2, stubs=0
	if n != 2 {
		t.Fatalf("expected 2 injections (user + assistant), got %d", n)
	}

	text := msgs[0].(map[string]any)["content"].(string)
	want := "[Di 2026-04-14 20:15:42] [msg:501] [+4s] fresh question 1"
	if text != want {
		t.Errorf("got %q, want %q", text, want)
	}
}
