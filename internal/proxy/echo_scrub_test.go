package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Marker fixture mirroring the proxy's injected prefix (BuildMeta format).
const echoedMetaBlock = "[Mi 2026-05-12 12:00:00] [msg:10] [+5s]\n[think-reminder] Think!\n[skill-eval] Skills!\n"

func TestEchoScrubber_MarkerDeltaConsumedThenContent(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [msg:10] [+5s]\n")); got != nil {
		t.Fatalf("marker-only delta should be consumed (nil), got %q", got)
	}
	got := e.Write([]byte("[think-reminder] Think!\nreal content here"))
	if string(got) != "real content here" {
		t.Errorf("got %q, want %q", got, "real content here")
	}
	// done: everything after passes through untouched
	if got := e.Write([]byte(" more")); string(got) != " more" {
		t.Errorf("post-content delta got %q, want %q", got, " more")
	}
}

func TestEchoScrubber_MarkerAndContentSameDelta(t *testing.T) {
	e := newEchoScrubber()
	got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [msg:10] [+5s]\nreal answer"))
	if string(got) != "real answer" {
		t.Errorf("got %q, want %q", got, "real answer")
	}
}

func TestEchoScrubber_ChunkSplitMarker(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [ms")); got != nil {
		t.Fatalf("split marker fragment should hold (nil), got %q", got)
	}
	got := e.Write([]byte("g:10] [+5s]\nanswer"))
	if string(got) != "answer" {
		t.Errorf("got %q, want %q", got, "answer")
	}
}

func TestEchoScrubber_NoMarkers(t *testing.T) {
	e := newEchoScrubber()
	in := "Hello there\nfriend content"
	got := e.Write([]byte(in))
	if string(got) != in {
		t.Errorf("plain content mangled: got %q, want %q", got, in)
	}
}

func TestEchoScrubber_ContentStartsWithBracketNotStripped(t *testing.T) {
	e := newEchoScrubber()
	in := "[Note: something genuinely useful]\nmore"
	if got := e.Write([]byte(in)); string(got) != in {
		t.Errorf("bracket content wrongly stripped: got %q, want %q", got, in)
	}
}

func TestEchoScrubber_ContentStartsWithBracketSplitThenNotStripped(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("[Not")); got != nil {
		t.Fatalf("'[..' should hold, got %q", got)
	}
	in := "e: something]\nmore"
	want := "[Note: something]\nmore" // '[Note: ...]' is content, preserved in full
	if got := e.Write([]byte(in)); string(got) != want {
		t.Errorf("bracket content mangled: got %q, want %q", got, want)
	}
}

func TestEchoScrubber_NoFalsePositive_MsgBracketInContent(t *testing.T) {
	e := newEchoScrubber()
	in := "[1] See [msg:2] for details\nmore"
	if got := e.Write([]byte(in)); string(got) != in {
		t.Errorf("content with a later [msg: must pass through untouched: got %q, want %q", got, in)
	}
}

func TestEchoScrubber_NoFalsePositive_MsgBracketSplitInContent(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("[Not")); got != nil {
		t.Fatalf("'[..' should hold, got %q", got)
	}
	in := "e] check [msg:10] later\nmore"
	want := "[Note] check [msg:10] later\nmore"
	if got := e.Write([]byte(in)); string(got) != want {
		t.Errorf("split content with a later [msg: mangled: got %q, want %q", got, want)
	}
}

func TestEchoScrubber_SystemReminderBlock(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("<system-reminder>\n")); got != nil {
		t.Fatalf("block opener should hold, got %q", got)
	}
	if got := e.Write([]byte("MANDATORY! do the thing\nremember stuff\n")); got != nil {
		t.Fatalf("block body should hold, got %q", got)
	}
	if got := e.Write([]byte("</system-reminder>\nreal output")); string(got) != "real output" {
		t.Errorf("got %q, want %q", got, "real output")
	}
}

func TestEchoScrubber_MetaLineWithInlineContent(t *testing.T) {
	e := newEchoScrubber()
	got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [msg:10] [+5s]Hi there"))
	if string(got) != "Hi there" {
		t.Errorf("got %q, want %q", got, "Hi there")
	}
}

func TestEchoScrubber_MiddleMarkersUntouched(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [msg:1] [+0s]\nStart")); string(got) != "Start" {
		t.Fatalf("got %q, want Start", got)
	}
	in := "\n\n[msg:999] not a marker here\nend"
	if got := e.Write([]byte(in)); string(got) != in {
		t.Errorf("mid-stream marker-like content mangled: got %q, want %q", got, in)
	}
}

func TestEchoScrubber_CapFlushes(t *testing.T) {
	e := newEchoScrubber()
	e.cap = 16
	big := bytes.Repeat([]byte("x"), 64)
	got := e.Write(big)
	if len(got) != 64 {
		t.Errorf("cap flush: got %d bytes, want 64", len(got))
	}
}

func TestEchoScrubber_CapFlushesMarkerWithoutHang(t *testing.T) {
	e := newEchoScrubber()
	e.cap = 16
	// Unclosed bracket marker that never completes — cap must force content
	got := e.Write([]byte("[Mi 2026-05-12 12:00:00] [msg:"))
	if len(got) != len("[Mi 2026-05-12 12:00:00] [msg:") {
		t.Errorf("expected cap flush of incomplete marker, got %q", got)
	}
}

func TestEchoScrubber_EmptyChunk(t *testing.T) {
	e := newEchoScrubber()
	if got := e.Write(nil); got != nil {
		t.Errorf("nil chunk should pass through, got %q", got)
	}
}

// --- Integration: OpenAI streaming round trip with an echoed marker prefix ---

func TestOpenAIHandler_StreamingScrubsEchoedMarker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		chunks := []string{
			`{"id":"x","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`{"id":"x","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"[Mi 2026-05-12 12:00:00] [msg:10] [+5s]\n[think-reminder] Think!\n"},"finish_reason":null}]}`,
			`{"id":"x","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"Great answer"},"finish_reason":null}]}`,
			`{"id":"x","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			fl.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer upstream.Close()

	s := &Server{
		cfg: Config{
			TargetURL:       upstream.URL,
			OpenAITargetURL: upstream.URL,
		},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	oaiReq := OpenAIChatRequest{Model: "gpt-5.4", Messages: []OpenAIMessage{{Role: "user", Content: "Hello"}}, Stream: true}
	body, _ := json.Marshal(oaiReq)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-123")
	w := httptest.NewRecorder()

	s.handleOpenAICompletions(w, req)

	respBody := w.Body.String()
	if strings.Contains(respBody, "[msg:10]") || strings.Contains(respBody, "[think-reminder]") {
		t.Errorf("echoed marker leaked to client:\n%s", respBody)
	}
	if !strings.Contains(respBody, "Great answer") {
		t.Errorf("real content missing:\n%s", respBody)
	}
	if !strings.Contains(respBody, "[DONE]") {
		t.Errorf("no [DONE] in response")
	}
}

// --- Integration: Anthropic streaming round trip with an echoed marker prefix ---

func TestAnthropicForwardScrubsEchoedMarker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		events := []string{
			`{"type":"message_start","message":{"id":"m1","role":"assistant","content":[]}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[Mi 2026-05-12 12:00:00] [msg:10] [+5s]\n[think-reminder] Think!\n"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Here is the answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "event: data\n")
			fmt.Fprintf(w, "data: %s\n\n", ev)
			fl.Flush()
		}
	}))
	defer upstream.Close()

	s := &Server{
		cfg:        Config{TargetURL: upstream.URL},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":64,"messages":[{"role":"user","content":"Hello"}]}`)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	w := httptest.NewRecorder()

	s.forwardWithAnnotation(w, req, body, 0, nil, "", "", "", 0)

	respBody := w.Body.String()
	if strings.Contains(respBody, "[msg:10]") || strings.Contains(respBody, "[think-reminder]") {
		t.Errorf("echoed marker leaked to client:\n%s", respBody)
	}
	if !strings.Contains(respBody, "Here is the answer") {
		t.Errorf("real content missing:\n%s", respBody)
	}
}
