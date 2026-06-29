package storage

import (
	"strings"
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
	got, err := s.ResolveProjectShortStrict("/var/www/html/ccm19/main/cookie-consent-management/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/var/www/html/ccm19/main/cookie-consent-management"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProjectShortStrict_UniqueShortResolves(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/home/user/yesmem", "yesmem", base)

	got, err := s.ResolveProjectShortStrict("yesmem")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/home/user/yesmem" {
		t.Errorf("got %q, want /home/user/yesmem", got)
	}
}

func TestResolveProjectShortStrict_AmbiguousShortErrors(t *testing.T) {
	s := mustOpen(t)
	base := time.Now()
	insertSessionForResolve(t, s, "s1", "/var/www/html/ccm19/cookie-consent-management", "cookie-consent-management", base)
	insertSessionForResolve(t, s, "s2", "/var/www/html/ccm19/main/cookie-consent-management", "cookie-consent-management", base.Add(time.Minute))
	insertSessionForResolve(t, s, "s3", "/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management", "cookie-consent-management", base.Add(2*time.Minute))

	_, err := s.ResolveProjectShortStrict("cookie-consent-management")
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	amb, ok := err.(*AmbiguousProjectError)
	if !ok {
		t.Fatalf("expected *AmbiguousProjectError, got %T: %v", err, err)
	}
	if amb.Short != "cookie-consent-management" {
		t.Errorf("Short = %q", amb.Short)
	}
	if len(amb.Candidates) != 3 {
		t.Errorf("expected 3 candidates, got %d: %v", len(amb.Candidates), amb.Candidates)
	}
	msg := err.Error()
	for _, want := range []string{
		"/var/www/html/ccm19/cookie-consent-management",
		"/var/www/html/ccm19/main/cookie-consent-management",
		"/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestResolveProjectShortStrict_UnknownShortPassthrough(t *testing.T) {
	s := mustOpen(t)
	got, err := s.ResolveProjectShortStrict("unknown-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "unknown-project" {
		t.Errorf("got %q, want passthrough", got)
	}
}

func TestProjectShortFromPath_NoCollisionFromModels(t *testing.T) {
	a := models.ProjectShortFromPath("/var/www/html/ccm19/cookie-consent-management")
	b := models.ProjectShortFromPath("/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management")
	if a == b {
		t.Fatalf("expected distinct project identifiers, both = %q", a)
	}
}
