
package locomo

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// mustStore opens an in-memory store and registers cleanup.
// Shared across all _test.go files in this package.
func mustStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage.Open(:memory:): %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIngestSample(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}

	stats, err := IngestSample(store, samples[0])
	if err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	// Verify stats
	if stats.Project != "locomo_1" {
		t.Errorf("stats.Project = %q, want %q", stats.Project, "locomo_1")
	}
	if stats.Sessions != 2 {
		t.Errorf("stats.Sessions = %d, want 2", stats.Sessions)
	}
	if stats.Messages != 4 {
		t.Errorf("stats.Messages = %d, want 4", stats.Messages)
	}

	// Verify sessions exist in DB
	sess1, err := store.GetSession("locomo_1_session_1")
	if err != nil {
		t.Fatalf("GetSession(session_1): %v", err)
	}
	if sess1.Project != "locomo_1" {
		t.Errorf("session_1.Project = %q, want %q", sess1.Project, "locomo_1")
	}
	if sess1.MessageCount != 2 {
		t.Errorf("session_1.MessageCount = %d, want 2", sess1.MessageCount)
	}

	sess2, err := store.GetSession("locomo_1_session_2")
	if err != nil {
		t.Fatalf("GetSession(session_2): %v", err)
	}
	if sess2.MessageCount != 2 {
		t.Errorf("session_2.MessageCount = %d, want 2", sess2.MessageCount)
	}

	// Verify messages in DB with correct role mapping
	msgs, err := store.GetMessagesBySession("locomo_1_session_1")
	if err != nil {
		t.Fatalf("GetMessagesBySession: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in session_1, got %d", len(msgs))
	}

	// speaker_a -> "user", speaker_b -> "assistant"
	if msgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %q, want %q (speaker_a -> user)", msgs[0].Role, "user")
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want %q (speaker_b -> assistant)", msgs[1].Role, "assistant")
	}

	// Verify content
	if msgs[0].Content != "Hi Bob, how was your trip to Paris?" {
		t.Errorf("msg[0].Content = %q", msgs[0].Content)
	}
	if msgs[1].Content != "Paris was amazing! I went in March." {
		t.Errorf("msg[1].Content = %q", msgs[1].Content)
	}
}

func TestIngestSampleTimestamps(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	_, err = IngestSample(store, samples[0])
	if err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	// Verify timestamps come from LoCoMo data, not time.Now()
	msgs, err := store.GetMessagesBySession("locomo_1_session_1")
	if err != nil {
		t.Fatalf("GetMessagesBySession: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages found")
	}

	// First message should be from 2023-03-15 (from session_1_date_time)
	firstMsg := msgs[0]
	wantDate := "2023-03-15"
	gotDate := firstMsg.Timestamp.Format("2006-01-02")
	if gotDate != wantDate {
		t.Errorf("first message date = %q, want %q (should come from LoCoMo data, not now())", gotDate, wantDate)
	}

	// Second session should have 2023-03-20
	msgs2, err := store.GetMessagesBySession("locomo_1_session_2")
	if err != nil {
		t.Fatalf("GetMessagesBySession(session_2): %v", err)
	}
	if len(msgs2) == 0 {
		t.Fatal("no messages in session_2")
	}
	gotDate2 := msgs2[0].Timestamp.Format("2006-01-02")
	if gotDate2 != "2023-03-20" {
		t.Errorf("session_2 first message date = %q, want %q", gotDate2, "2023-03-20")
	}

	// Timestamps should NOT be close to now — they should be years in the past
	if time.Since(firstMsg.Timestamp) < 24*time.Hour {
		t.Errorf("timestamp appears to be from now() instead of LoCoMo data: %v", firstMsg.Timestamp)
	}
}

func TestIngestAll(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	allStats, err := IngestAll(store, samples)
	if err != nil {
		t.Fatalf("IngestAll: %v", err)
	}
	if len(allStats) != 1 {
		t.Fatalf("expected 1 stats entry, got %d", len(allStats))
	}
	if allStats[0].Project != "locomo_1" {
		t.Errorf("allStats[0].Project = %q, want %q", allStats[0].Project, "locomo_1")
	}
}
