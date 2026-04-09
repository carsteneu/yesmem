package storage

import (
	"testing"
)

func TestTrackTokenUsage_WithCache(t *testing.T) {
	s := newTestStore(t)

	if err := s.TrackTokenUsage("thread-1", 1000, 200, 800, 100); err != nil {
		t.Fatal(err)
	}

	in, out, err := s.GetTokenUsage("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if in != 1000 {
		t.Errorf("input = %d, want 1000", in)
	}
	if out != 200 {
		t.Errorf("output = %d, want 200", out)
	}

	// Verify cache columns via raw query
	var cacheRead, cacheWrite int
	err = s.db.QueryRow("SELECT cache_read_tokens, cache_write_tokens FROM token_usage WHERE thread_id = ?", "thread-1").Scan(&cacheRead, &cacheWrite)
	if err != nil {
		t.Fatal(err)
	}
	if cacheRead != 800 {
		t.Errorf("cache_read = %d, want 800", cacheRead)
	}
	if cacheWrite != 100 {
		t.Errorf("cache_write = %d, want 100", cacheWrite)
	}

	// Second call: values accumulate
	if err := s.TrackTokenUsage("thread-1", 500, 100, 400, 50); err != nil {
		t.Fatal(err)
	}

	err = s.db.QueryRow("SELECT input_tokens, output_tokens, cache_read_tokens, cache_write_tokens FROM token_usage WHERE thread_id = ?", "thread-1").Scan(&in, &out, &cacheRead, &cacheWrite)
	if err != nil {
		t.Fatal(err)
	}
	if in != 1500 {
		t.Errorf("input = %d, want 1500 (accumulated)", in)
	}
	if out != 300 {
		t.Errorf("output = %d, want 300 (accumulated)", out)
	}
	if cacheRead != 1200 {
		t.Errorf("cache_read = %d, want 1200 (accumulated)", cacheRead)
	}
	if cacheWrite != 150 {
		t.Errorf("cache_write = %d, want 150 (accumulated)", cacheWrite)
	}
}
