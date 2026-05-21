package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// seedSearchMessages creates one session and three messages dated 04-27, 04-28, 04-29.
// All messages share a unique FTS-friendly token "needle_xyz" so the FTS query is identical.
func seedSearchMessages(t *testing.T, s *Store) {
	t.Helper()
	sess := &models.Session{
		ID:        "test-search-ctx",
		Project:   "/proj",
		StartedAt: time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		IndexedAt: time.Now().UTC(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	mk := func(seq int, ts time.Time, body string) models.Message {
		return models.Message{
			SessionID:   sess.ID,
			Role:        "user",
			MessageType: "text",
			Content:     body,
			Timestamp:   ts,
			Sequence:    seq,
		}
	}
	msgs := []models.Message{
		mk(1, time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC), "needle_xyz day27"),
		mk(2, time.Date(2026, 4, 28, 19, 57, 16, 0, time.UTC), "needle_xyz day28"),
		mk(3, time.Date(2026, 4, 29, 8, 30, 0, 0, time.UTC), "needle_xyz day29"),
	}
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
}

func loadContents(t *testing.T, s *Store, hits []MessageSearchResult) {
	t.Helper()
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	contents, err := s.GetMessageContents(ids)
	if err != nil {
		t.Fatalf("get message contents: %v", err)
	}
	for i := range hits {
		hits[i].Content = contents[hits[i].ID]
	}
}

func contentsByDay(hits []MessageSearchResult) map[string]bool {
	out := map[string]bool{}
	for _, h := range hits {
		switch {
		case h.Content == "needle_xyz day27":
			out["27"] = true
		case h.Content == "needle_xyz day28":
			out["28"] = true
		case h.Content == "needle_xyz day29":
			out["29"] = true
		}
	}
	return out
}

func TestSearchMessagesCtx_EmptySinceBefore_ReturnsAll(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesCtx("needle_xyz", "", "", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := contentsByDay(hits)
	if !got["27"] || !got["28"] || !got["29"] {
		t.Fatalf("expected all 3 days, got %v (n=%d)", got, len(hits))
	}
}

func TestSearchMessagesCtx_SinceFiltersOutEarlier(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesCtx("needle_xyz", "2026-04-28", "", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := contentsByDay(hits)
	if got["27"] {
		t.Fatalf("since=2026-04-28 must exclude 04-27, got %v", got)
	}
	if !got["28"] || !got["29"] {
		t.Fatalf("since=2026-04-28 must include 04-28 and 04-29, got %v", got)
	}
}

func TestSearchMessagesCtx_BeforeFiltersOutLater(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesCtx("needle_xyz", "", "2026-04-29", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := contentsByDay(hits)
	if got["29"] {
		t.Fatalf("before=2026-04-29 must exclude 04-29, got %v", got)
	}
	if !got["27"] || !got["28"] {
		t.Fatalf("before=2026-04-29 must include 04-27 and 04-28, got %v", got)
	}
}

func TestSearchMessagesCtx_BothBoundsNarrowToOne(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesCtx("needle_xyz", "2026-04-28", "2026-04-29", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := contentsByDay(hits)
	if got["27"] || got["29"] {
		t.Fatalf("expected only 04-28, got %v", got)
	}
	if !got["28"] {
		t.Fatalf("expected 04-28 in results, got %v", got)
	}
}

func TestSearchMessages_BackwardCompat_DelegatesNoFilter(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessages("needle_xyz", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := contentsByDay(hits)
	if !got["27"] || !got["28"] || !got["29"] {
		t.Fatalf("legacy SearchMessages must return all days, got %v", got)
	}
}

func TestSearchMessagesDeepCtx_FiltersByDate(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesDeepCtx("needle_xyz", false, false, "2026-04-28", "2026-04-29", 100)
	if err != nil {
		t.Fatalf("search deep: %v", err)
	}
	loadContents(t, s, hits)
	got := contentsByDay(hits)
	if got["27"] || got["29"] {
		t.Fatalf("expected only 04-28, got %v", got)
	}
	if !got["28"] {
		t.Fatalf("expected 04-28 in results, got %v", got)
	}
}

func TestSearchMessagesDeepCtx_BackwardCompatLegacyDeepNoFilter(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()
	seedSearchMessages(t, s)

	hits, err := s.SearchMessagesDeep("needle_xyz", false, false, 100)
	if err != nil {
		t.Fatalf("search deep: %v", err)
	}
	loadContents(t, s, hits)
	got := contentsByDay(hits)
	if !got["27"] || !got["28"] || !got["29"] {
		t.Fatalf("legacy SearchMessagesDeep must return all days, got %v", got)
	}
}

// TestSearchMessagesDeepCtx_NarrowWindowSparseTextDoesNotCollapse exercises the
// post-fetch type-filter risk: when a tight date window is dominated by
// excluded message types (e.g. tool_use) and the text hit is ranked low by FTS,
// the over-fetched batch must still let the text hit through.
func TestSearchMessagesDeepCtx_NarrowWindowSparseTextDoesNotCollapse(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	sess := &models.Session{
		ID:        "collapse-session",
		Project:   "/proj",
		StartedAt: time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
		IndexedAt: time.Now().UTC(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	mk := func(seq int, mtype, body string) models.Message {
		return models.Message{
			SessionID:   sess.ID,
			Role:        "assistant",
			MessageType: mtype,
			Content:     body,
			Timestamp:   time.Date(2026, 4, 28, 12, seq, 0, 0, time.UTC),
			Sequence:    seq,
		}
	}

	// Insert 30 excluded-type rows BEFORE the single text row so any
	// post-fetch limit < 30 collapses without seeing the text hit.
	var msgs []models.Message
	for i := 1; i <= 30; i++ {
		msgs = append(msgs, mk(i, "tool_use", "needle_collapse tool"))
	}
	msgs = append(msgs, mk(31, "text", "needle_collapse text"))
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	hits, err := s.SearchMessagesDeepCtx("needle_collapse", false, false,
		"2026-04-28", "2026-04-29", 1)
	if err != nil {
		t.Fatalf("search deep: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected text hit, got empty (type filter collapsed under limit*N)")
	}
	if hits[0].MessageType != "text" {
		t.Fatalf("expected text hit, got message_type=%q", hits[0].MessageType)
	}
}
