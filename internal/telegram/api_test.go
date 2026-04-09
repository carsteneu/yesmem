package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123/getUpdates" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{
				{
					"update_id": 1001,
					"message": map[string]any{
						"message_id": 1,
						"from":       map[string]any{"id": 42, "first_name": "Test"},
						"chat":       map[string]any{"id": 42},
						"text":       "hello bot",
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewClient("123")
	client.BaseURL = srv.URL + "/bot123"

	updates, err := client.GetUpdates(0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].Message.Text != "hello bot" {
		t.Errorf("expected 'hello bot', got %q", updates[0].Message.Text)
	}
	if updates[0].Message.From.ID != 42 {
		t.Errorf("expected user ID 42, got %d", updates[0].Message.From.ID)
	}
}

func TestSendMessage(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer srv.Close()

	client := NewClient("123")
	client.BaseURL = srv.URL + "/bot123"

	err := client.SendMessage(42, "hello user")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if received["text"] != "hello user" {
		t.Errorf("expected 'hello user', got %v", received["text"])
	}
}

func TestSplitMessage(t *testing.T) {
	short := "hello"
	chunks := splitMessage(short, 4096)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}

	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'a'
	}
	chunks = splitMessage(string(long), 4096)
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(chunks))
	}
	if len(chunks[0]) != 4096 {
		t.Errorf("first chunk should be 4096, got %d", len(chunks[0]))
	}
}
