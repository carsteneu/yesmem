package bloom

import (
	"testing"
)

func TestAddAndQuery(t *testing.T) {
	mgr := New()

	mgr.AddSession("s1", []string{"nginx", "config", "port", "8080", "deployment"})
	mgr.AddSession("s2", []string{"docker", "compose", "redis", "deployment"})
	mgr.AddSession("s3", []string{"php", "migration", "database"})

	// "nginx" should match s1 only
	matches := mgr.MayContain("nginx")
	if !contains(matches, "s1") {
		t.Error("expected s1 to match 'nginx'")
	}
	if contains(matches, "s3") {
		t.Error("s3 should not match 'nginx'")
	}

	// "deployment" should match s1 and s2
	matches = mgr.MayContain("deployment")
	if len(matches) < 2 {
		t.Errorf("expected at least 2 matches for 'deployment', got %d", len(matches))
	}

	// "nonexistent" should match nothing
	matches = mgr.MayContain("zzz_nonexistent_zzz")
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for nonexistent term, got %d", len(matches))
	}
}

func TestMayContainMultiple(t *testing.T) {
	mgr := New()

	mgr.AddSession("s1", []string{"nginx", "config", "port"})
	mgr.AddSession("s2", []string{"nginx", "docker", "deploy"})
	mgr.AddSession("s3", []string{"php", "artisan", "migrate"})

	// "nginx" + "docker" should narrow to s2
	matches := mgr.MayContainAll([]string{"nginx", "docker"})
	if len(matches) != 1 || matches[0] != "s2" {
		t.Errorf("expected [s2], got %v", matches)
	}
}

func TestSessionCount(t *testing.T) {
	mgr := New()
	if mgr.SessionCount() != 0 {
		t.Error("empty manager should have 0 sessions")
	}

	mgr.AddSession("s1", []string{"a", "b"})
	mgr.AddSession("s2", []string{"c"})
	if mgr.SessionCount() != 2 {
		t.Errorf("expected 2, got %d", mgr.SessionCount())
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
