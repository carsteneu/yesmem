package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The rule cases (missing row, parent mismatch, wrong agent type, duplicated id)
// live in internal/verify/claimtypes/yesmem_claims_test.go, where the rule is
// decided. What is tested here is the adapter: that opencode's two schema
// generations are both reached and that a failed lookup is not read as verified.

// evidenceDBPath returns a writable dummy db path — the default queryOpencodeSession
// is stubbed, but VerifySubagentEvidence still stats the file to guard against
// infra outages.
func evidenceDBPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "opencode.db")
	if err := os.WriteFile(p, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// evidenceFake returns a queryOpencodeSession stub mapping session ids to
// (parent_id, agent) rows. Missing ids return found=false (agent fault), not
// an error (infra fault).
func evidenceFake(rows map[string][2]string) func(dbPath, sessionID string) (bool, string, string, error) {
	return func(dbPath, sessionID string) (bool, string, string, error) {
		row, ok := rows[sessionID]
		if !ok {
			return false, "", "", nil
		}
		return true, row[0], row[1], nil
	}
}

func withEvidenceRows(t *testing.T, rows map[string][2]string) {
	t.Helper()
	orig := queryOpencodeSession
	queryOpencodeSession = evidenceFake(rows)
	t.Cleanup(func() { queryOpencodeSession = orig })
}

func evidenceFixtureRows() map[string][2]string {
	return map[string][2]string{
		"ses_cold": {"ses_parent", "reviewer"},
		"ses_cons": {"ses_parent", "general"},
		"ses_sec":  {"ses_parent", "reviewer"},
	}
}

func evidenceFixtureEvidence() map[string]string {
	return map[string]string{
		"cold-review": "ses_cold",
		"consequence": "ses_cons",
		"security":    "ses_sec",
	}
}

func TestVerifySubagentEvidence_AllChecksPass(t *testing.T) {
	withEvidenceRows(t, evidenceFixtureRows())
	errs := VerifySubagentEvidence(evidenceDBPath(t), "ses_parent", evidenceFixtureEvidence())
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
}

// The lookup error is not skipped any more (YM-304): an id the gate could not
// verify must not count as verified, because the DONE claim rests on that check.
// The old expectation here was "infra error must not fail the agent"; the closed
// direction heals itself — the guard unpauses an agent whose scratchpad becomes
// compliant — while the open one let an unproven review block through.
func TestVerifySubagentEvidence_InfraErrorFailsVerification(t *testing.T) {
	withEvidenceRows(t, evidenceFixtureRows())
	orig := queryOpencodeSession
	queryOpencodeSession = func(dbPath, sessionID string) (bool, string, string, error) {
		return false, "", "", context.DeadlineExceeded
	}
	t.Cleanup(func() { queryOpencodeSession = orig })
	if errs := VerifySubagentEvidence(evidenceDBPath(t), "ses_parent", evidenceFixtureEvidence()); len(errs) == 0 {
		t.Fatal("a failed lookup was accepted as verified, want a finding per unverifiable key")
	}
}

func TestVerifySubagentEvidence_EmptyDBPathSkipsVerification(t *testing.T) {
	withEvidenceRows(t, evidenceFixtureRows())
	if errs := VerifySubagentEvidence("", "ses_parent", evidenceFixtureEvidence()); len(errs) != 0 {
		t.Fatalf("empty dbPath must skip verification, got: %v", errs)
	}
}

func TestVerifySubagentEvidence_EmptyParentSkipsVerification(t *testing.T) {
	withEvidenceRows(t, evidenceFixtureRows())
	if errs := VerifySubagentEvidence(evidenceDBPath(t), "", evidenceFixtureEvidence()); len(errs) != 0 {
		t.Fatalf("empty parent session id must skip verification, got: %v", errs)
	}
}

func TestVerifySubagentEvidence_NoEvidenceSkipsVerification(t *testing.T) {
	withEvidenceRows(t, evidenceFixtureRows())
	if errs := VerifySubagentEvidence(evidenceDBPath(t), "ses_parent", nil); len(errs) != 0 {
		t.Fatalf("empty evidence must skip verification, got: %v", errs)
	}
}

// TestVerifySubagentEvidence_AgainstSQLite is the integration proof: the
// default queryOpencodeSession talks to a real opencode-style sqlite DB via
// the sqlite3 CLI — the same access pattern as opencodeSessionDir.
func TestVerifySubagentEvidence_AgainstSQLite(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	sql := "CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, agent TEXT, title TEXT, time_created INTEGER);" +
		"INSERT INTO session (id, parent_id, agent, title, time_created) VALUES ('ses_parent', '', 'opencode', 'loop', 1);" +
		"INSERT INTO session (id, parent_id, agent, title, time_created) VALUES ('ses_cold', 'ses_parent', 'reviewer', 'cold', 2);" +
		"INSERT INTO session (id, parent_id, agent, title, time_created) VALUES ('ses_cons', 'ses_parent', 'general', 'cons', 3);" +
		"INSERT INTO session (id, parent_id, agent, title, time_created) VALUES ('ses_sec', 'ses_parent', 'reviewer', 'sec', 4);"
	if out, err := exec.Command("sqlite3", dbPath, sql).CombinedOutput(); err != nil {
		t.Fatalf("failed to build fixture db: %v: %s", err, out)
	}

	if errs := VerifySubagentEvidence(dbPath, "ses_parent", evidenceFixtureEvidence()); len(errs) != 0 {
		t.Fatalf("expected no errors against fixture db, got: %v", errs)
	}

	evidence := evidenceFixtureEvidence()
	evidence["security"] = "ses_unknown"
	errs := VerifySubagentEvidence(dbPath, "ses_parent", evidence)
	if len(errs) != 1 || errs[0].Field != "security" {
		t.Fatalf("expected 1 error for security, got: %v", errs)
	}
}

// TestVerifySubagentEvidence_AgainstSQLiteV2 is the regression guard for the
// v2 schema drift: the production opencode.db carries BOTH tables — the legacy
// `session` table (frozen, last written by 1.x) and `session_v2` (current) —
// and v2 subagent sessions exist only in `session_v2`. Because evidence
// verification only runs once the agent's own session id was captured, fixing
// the capture without this fallback would make the DONE-guard reject every v2
// agent's Phase 5 block with "not found in opencode.db".
func TestVerifySubagentEvidence_AgainstSQLiteV2(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	sql := "CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, agent TEXT, title TEXT, time_created INTEGER);" +
		// The legacy table holds an unrelated old session — it must not leak in.
		"INSERT INTO session (id, parent_id, agent, title, time_created) VALUES ('ses_legacy', '', 'opencode', 'old', 1);" +
		"CREATE TABLE session_v2 (id TEXT PRIMARY KEY, parent_id TEXT, agent TEXT, title TEXT, time_created INTEGER);" +
		"INSERT INTO session_v2 (id, parent_id, agent, title, time_created) VALUES ('ses_parent', '', 'build', 'loop', 1);" +
		"INSERT INTO session_v2 (id, parent_id, agent, title, time_created) VALUES ('ses_cold', 'ses_parent', 'reviewer', 'cold', 2);" +
		"INSERT INTO session_v2 (id, parent_id, agent, title, time_created) VALUES ('ses_cons', 'ses_parent', 'general', 'cons', 3);" +
		"INSERT INTO session_v2 (id, parent_id, agent, title, time_created) VALUES ('ses_sec', 'ses_parent', 'reviewer', 'sec', 4);"
	if out, err := exec.Command("sqlite3", dbPath, sql).CombinedOutput(); err != nil {
		t.Fatalf("failed to build fixture db: %v: %s", err, out)
	}

	if errs := VerifySubagentEvidence(dbPath, "ses_parent", evidenceFixtureEvidence()); len(errs) != 0 {
		t.Fatalf("v2 sessions must verify without errors, got: %v", errs)
	}

	// The parent check and the allowed-agent check must still bite on v2 rows.
	evidence := evidenceFixtureEvidence()
	evidence["security"] = "ses_unknown"
	errs := VerifySubagentEvidence(dbPath, "ses_parent", evidence)
	if len(errs) != 1 || errs[0].Field != "security" {
		t.Fatalf("expected 1 error for the missing v2 session, got: %v", errs)
	}
	if errs := VerifySubagentEvidence(dbPath, "ses_wrong_parent", evidenceFixtureEvidence()); len(errs) != 3 {
		t.Fatalf("expected 3 parent-mismatch errors on v2 rows, got: %v", errs)
	}
	// The legacy table is still consulted: ses_legacy exists there with a
	// different parent, so it is found and rejected — not reported as missing.
	legacy := map[string]string{"cold-review": "ses_legacy", "consequence": "ses_legacy", "security": "ses_legacy"}
	for _, fe := range VerifySubagentEvidence(dbPath, "ses_parent", legacy) {
		if strings.Contains(fe.Detail, "not found") {
			t.Errorf("a legacy-table session must be found (its parent is checked), got: %s", fe.Detail)
		}
	}
}
