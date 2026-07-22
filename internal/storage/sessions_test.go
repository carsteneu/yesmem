package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func insertSessionForResolve(t *testing.T, s *Store, id, project, projectShort string, startedAt time.Time) {
	t.Helper()
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, project, projectShort, startedAt.Format(time.RFC3339), startedAt.Format(time.RFC3339), "/"+id+".jsonl",
	)
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func TestResolveProjectShort_AbsolutePathIdentity(t *testing.T) {
	s := mustOpen(t)
	in := "/var/www/html/ccm19/main/cookie-consent-management"
	got := s.ResolveProjectShort(in)
	if got != in {
		t.Errorf("ResolveProjectShort(%q) = %q, want identity", in, got)
	}
}

func TestResolveProjectShort_Empty(t *testing.T) {
	s := mustOpen(t)
	if got := s.ResolveProjectShort(""); got != "" {
		t.Errorf("ResolveProjectShort(\"\") = %q, want empty", got)
	}
}

func TestResolveProjectShortStrict_AbsoluteReturnsCleaned(t *testing.T) {
	s := mustOpen(t)
	got, err := s.ResolveProjectShortStrict("/var/www/html/ccm19/main/cookie-consent-management/", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/var/www/html/ccm19/main/cookie-consent-management"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProjectShortStrict_UniqueShortResolvesWithFullpathData(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	// Real data shape after v0.65: project_short = full path
	insertSessionForResolve(t, s, "s1", "/home/user/yesmem", "/home/user/yesmem", base)

	got, err := s.ResolveProjectShortStrict("yesmem", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/yesmem" {
		t.Errorf("got %q, want /home/user/yesmem", got)
	}
}

// TestResolveProjectShortStrict_AmbiguousShortReturnsError verifies that
// ambiguous short names (matching multiple full paths) now return an
// AmbiguousProjectError with all candidates instead of silently falling
// back to the first alphabetical candidate.
func TestResolveProjectShortStrict_AmbiguousShortReturnsError(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/var/www/html/ccm19/cookie-consent-management", "/var/www/html/ccm19/cookie-consent-management", base)
	insertSessionForResolve(t, s, "s2", "/var/www/html/ccm19/main/cookie-consent-management", "/var/www/html/ccm19/main/cookie-consent-management", base.Add(time.Minute))
	insertSessionForResolve(t, s, "s3", "/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management", "/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management", base.Add(2*time.Minute))

	got, err := s.ResolveProjectShortStrict("cookie-consent-management", "")
	if err == nil {
		t.Fatalf("expected AmbiguousProjectError for ambiguous short name, got %q", got)
	}
	ambErr, ok := err.(*AmbiguousProjectError)
	if !ok {
		t.Fatalf("expected *AmbiguousProjectError, got %T: %v", err, err)
	}
	if len(ambErr.Candidates) != 3 {
		t.Errorf("expected 3 candidates, got %d: %v", len(ambErr.Candidates), ambErr.Candidates)
	}
}

func TestResolveProjectShortStrict_UnknownShortPassthrough(t *testing.T) {
	s := mustOpen(t)
	got, err := s.ResolveProjectShortStrict("unknown-project", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "unknown-project" {
		t.Errorf("got %q, want passthrough", got)
	}
}

func TestResolveProjectShortStrict_CwdExactMatch(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/yesmem", "/home/user/memory/yesmem", base)
	insertSessionForResolve(t, s, "s2", "/home/user/other/yesmem", "/home/user/other/yesmem", base.Add(time.Minute))

	// cwd matches first candidate exactly
	got, err := s.ResolveProjectShortStrict("yesmem", "/home/user/memory/yesmem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/memory/yesmem" {
		t.Errorf("got %q, want /home/user/memory/yesmem", got)
	}
}

func TestResolveProjectShortStrict_CwdAncestorMatch(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/yesmem", "/home/user/memory/yesmem", base)
	insertSessionForResolve(t, s, "s2", "/home/user/other/yesmem", "/home/user/other/yesmem", base.Add(time.Minute))

	// cwd is a subdirectory (worktree) of the first candidate
	got, err := s.ResolveProjectShortStrict("yesmem", "/home/user/memory/yesmem/.worktrees/yesloop-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/memory/yesmem" {
		t.Errorf("got %q, want /home/user/memory/yesmem", got)
	}
}

func TestResolveProjectShortStrict_CwdAncestorExactBoundary(t *testing.T) {
	// Guard: /home/user/memory/yes must NOT be ancestor of /home/user/memory/yesmem
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/yes", "/home/user/memory/yes", base)
	insertSessionForResolve(t, s, "s2", "/home/user/memory/yesmem", "/home/user/memory/yesmem", base.Add(time.Minute))

	// "yes" only matches /home/user/memory/yes (basename), not /home/user/memory/yesmem
	got, err := s.ResolveProjectShortStrict("yes", "/home/user/memory/yesmem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/memory/yes" {
		t.Errorf("got %q, want /home/user/memory/yes", got)
	}
}

func TestResolveProjectShortStrict_CwdLongestAncestor(t *testing.T) {
	// When a worktree project shares the same basename as its parent,
	// a caller inside the worktree should resolve to the worktree (longest match).
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/yesmem", "/home/user/memory/yesmem", base)
	insertSessionForResolve(t, s, "s2", "/home/user/memory/yesmem/.worktrees/yesmem", "/home/user/memory/yesmem/.worktrees/yesmem", base.Add(time.Minute))

	// cwd inside the worktree
	got, err := s.ResolveProjectShortStrict("yesmem", "/home/user/memory/yesmem/.worktrees/yesmem/src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/memory/yesmem/.worktrees/yesmem" {
		t.Errorf("got %q, want /home/user/memory/yesmem/.worktrees/yesmem (longest ancestor)", got)
	}
}

func TestResolveProjectShortStrict_CwdAncestorFalsePositiveGuard(t *testing.T) {
	// Guard: basename boundary — "proj" must NOT match "proj-other" as ancestor
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/proj", "/home/user/memory/proj", base)
	insertSessionForResolve(t, s, "s2", "/home/user/memory/proj-other", "/home/user/memory/proj-other", base.Add(time.Minute))

	got, err := s.ResolveProjectShortStrict("proj", "/home/user/memory/proj/.worktrees/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/memory/proj" {
		t.Errorf("got %q, want /home/user/memory/proj", got)
	}
}

// TestResolveProjectShortStrict_AmbiguousNoCwdMatch verifies that ambiguous
// short names with a cwd matching no candidate return an AmbiguousProjectError.
func TestResolveProjectShortStrict_AmbiguousNoCwdMatch(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/memory/yesmem", "/home/user/memory/yesmem", base)
	insertSessionForResolve(t, s, "s2", "/home/user/other/yesmem", "/home/user/other/yesmem", base.Add(time.Minute))

	// cwd doesn't match either candidate → AmbiguousProjectError
	got, err := s.ResolveProjectShortStrict("yesmem", "/unrelated/path")
	if err == nil {
		t.Fatalf("expected AmbiguousProjectError for unresolved cwd mismatch, got %q", got)
	}
	if _, ok := err.(*AmbiguousProjectError); !ok {
		t.Fatalf("expected *AmbiguousProjectError, got %T: %v", err, err)
	}
}

func TestResolveProjectShortStrict_ZeroCandidates(t *testing.T) {
	s := mustOpen(t)
	// No sessions in DB → 0 candidates
	got, err := s.ResolveProjectShortStrict("nonexistent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "nonexistent" {
		t.Errorf("got %q, want passthrough 'nonexistent'", got)
	}
}

func TestResolveProjectShortStrict_EmptyName(t *testing.T) {
	s := mustOpen(t)
	got, err := s.ResolveProjectShortStrict("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestProjectShortFromPath_NoCollisionFromModels(t *testing.T) {
	a := models.ProjectShortFromPath("/var/www/html/ccm19/cookie-consent-management")
	b := models.ProjectShortFromPath("/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management")
	if a == b {
		t.Fatalf("expected distinct project identifiers, both = %q", a)
	}
}

func TestMigrationV066_AgentsProjectBackfill(t *testing.T) {
	s := mustOpen(t)

	// Insert sessions with full paths (post-v0.65 data shape)
	_, err := s.db.Exec(`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path)
		VALUES ('s1', '/home/user/memory/yesmem', '/home/user/memory/yesmem', datetime('now'), datetime('now'), '/s1.jsonl')`)
	if err != nil {
		t.Fatalf("insert session s1: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path)
		VALUES ('s2', '/home/user/other/yesmem', '/home/user/other/yesmem', datetime('now'), datetime('now'), '/s2.jsonl')`)
	if err != nil {
		t.Fatalf("insert session s2: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path)
		VALUES ('s3', '/home/user/unique-proj', '/home/user/unique-proj', datetime('now'), datetime('now'), '/s3.jsonl')`)
	if err != nil {
		t.Fatalf("insert session s3: %v", err)
	}

	// Insert agents with short-name projects (the pre-v0.66 state)
	_, err = s.db.Exec(`INSERT INTO agents (id, project, section, status) VALUES ('a1', 'yesmem', 'test1', 'running')`)
	if err != nil {
		t.Fatalf("insert agent a1: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO agents (id, project, section, status) VALUES ('a2', 'unique-proj', 'test2', 'running')`)
	if err != nil {
		t.Fatalf("insert agent a2: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO agents (id, project, section, status) VALUES ('a3', '/home/user/already/full', 'test3', 'running')`)
	if err != nil {
		t.Fatalf("insert agent a3: %v", err)
	}

	// Run the migration
	migration := `UPDATE agents SET project = (
		SELECT s.project FROM sessions s
		WHERE s.project LIKE '/%' AND s.project LIKE '%/' || agents.project
		LIMIT 1
	) WHERE agents.project NOT LIKE '/%'
	AND 1 = (
		SELECT COUNT(DISTINCT s2.project) FROM sessions s2
		WHERE s2.project LIKE '/%' AND s2.project LIKE '%/' || agents.project
	)`
	_, err = s.db.Exec(migration)
	if err != nil {
		t.Fatalf("migration v0.66: %v", err)
	}

	// a1 'yesmem' → ambiguous (2 matches) → should NOT be updated
	var proj string
	s.db.QueryRow("SELECT project FROM agents WHERE id = 'a1'").Scan(&proj)
	if proj != "yesmem" {
		t.Errorf("agent a1: got %q, want 'yesmem' (ambiguous, should not backfill)", proj)
	}

	// a2 'unique-proj' → unique match → should be updated
	s.db.QueryRow("SELECT project FROM agents WHERE id = 'a2'").Scan(&proj)
	if proj != "/home/user/unique-proj" {
		t.Errorf("agent a2: got %q, want '/home/user/unique-proj'", proj)
	}

	// a3 already full path → should not be changed
	s.db.QueryRow("SELECT project FROM agents WHERE id = 'a3'").Scan(&proj)
	if proj != "/home/user/already/full" {
		t.Errorf("agent a3: got %q, want '/home/user/already/full'", proj)
	}

	// Idempotency: run migration again, no changes
	_, err = s.db.Exec(migration)
	if err != nil {
		t.Fatalf("migration v0.66 (2nd run): %v", err)
	}
	s.db.QueryRow("SELECT project FROM agents WHERE id = 'a2'").Scan(&proj)
	if proj != "/home/user/unique-proj" {
		t.Errorf("agent a2 after 2nd run: got %q, want '/home/user/unique-proj'", proj)
	}
}

func TestResolveProjectShortStrict_AmbiguousErrorContainsCandidates(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/alpha/proj", "/home/user/alpha/proj", base)
	insertSessionForResolve(t, s, "s2", "/home/user/beta/proj", "/home/user/beta/proj", base.Add(time.Minute))

	_, err := s.ResolveProjectShortStrict("proj", "")
	if err == nil {
		t.Fatal("expected AmbiguousProjectError")
	}
	ambErr, ok := err.(*AmbiguousProjectError)
	if !ok {
		t.Fatalf("expected *AmbiguousProjectError, got %T: %v", err, err)
	}
	if ambErr.Name != "proj" {
		t.Errorf("error name = %q, want 'proj'", ambErr.Name)
	}
	if len(ambErr.Candidates) != 2 {
		t.Errorf("expected 2 candidates, got %d: %v", len(ambErr.Candidates), ambErr.Candidates)
	}
	if ambErr.Candidates[0] != "/home/user/alpha/proj" {
		t.Errorf("candidates[0] = %q, want /home/user/alpha/proj", ambErr.Candidates[0])
	}
	if ambErr.Candidates[1] != "/home/user/beta/proj" {
		t.Errorf("candidates[1] = %q, want /home/user/beta/proj", ambErr.Candidates[1])
	}
}

func TestResolveProjectShortStrict_BasenameIsolation(t *testing.T) {
	// Different basenames must not collide: "apple" must match only /path/to/apple,
	// not /path/to/apple-pie or /path/to/pineapple.
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/code/apple", "/home/user/code/apple", base)
	insertSessionForResolve(t, s, "s2", "/home/user/code/apple-pie", "/home/user/code/apple-pie", base.Add(time.Minute))
	insertSessionForResolve(t, s, "s3", "/home/user/code/pineapple", "/home/user/code/pineapple", base.Add(2*time.Minute))

	// "apple" should match only /home/user/code/apple (unique)
	got, err := s.ResolveProjectShortStrict("apple", "")
	if err != nil {
		t.Fatalf("unexpected error for unique basename 'apple': %v", err)
	}
	if got != "/home/user/code/apple" {
		t.Errorf("got %q, want /home/user/code/apple", got)
	}
}

func TestResolveProjectShortStrict_CwdResolvesAmbiguity(t *testing.T) {
	// When cwd matches one candidate, no error — direct resolution.
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/var/www/client-a/cms", "/var/www/client-a/cms", base)
	insertSessionForResolve(t, s, "s2", "/var/www/client-b/cms", "/var/www/client-b/cms", base.Add(time.Minute))

	got, err := s.ResolveProjectShortStrict("cms", "/var/www/client-a/cms")
	if err != nil {
		t.Fatalf("expected cwd tiebreaker to resolve ambiguity, got error: %v", err)
	}
	if got != "/var/www/client-a/cms" {
		t.Errorf("got %q, want /var/www/client-a/cms", got)
	}
}

func TestResolveProjectShortStrict_CwdResolvesAmbiguityViaAncestor(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/var/www/client-a/cms", "/var/www/client-a/cms", base)
	insertSessionForResolve(t, s, "s2", "/var/www/client-b/cms", "/var/www/client-b/cms", base.Add(time.Minute))

	// cwd is a subdirectory (worktree) of client-a
	got, err := s.ResolveProjectShortStrict("cms", "/var/www/client-a/cms/.worktrees/feature")
	if err != nil {
		t.Fatalf("expected cwd ancestor tiebreaker to resolve, got error: %v", err)
	}
	if got != "/var/www/client-a/cms" {
		t.Errorf("got %q, want /var/www/client-a/cms", got)
	}
}
