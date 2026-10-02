package capfile

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const capDeletePath = "../../caps/bundled-caps/cap_delete/CAP.md"

func loadCapDelete(t *testing.T) *CapFile {
	t.Helper()
	data, err := os.ReadFile(capDeletePath)
	if err != nil {
		t.Fatalf("read %s: %v", capDeletePath, err)
	}
	cf, err := Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", capDeletePath, err)
	}
	return cf
}

func capDeleteBody(t *testing.T) string {
	t.Helper()
	cf := loadCapDelete(t)
	sc := cf.FindScript("cap_delete")
	if sc == nil {
		t.Fatal("cap_delete: no 'cap_delete' script")
	}
	return sc.Body
}

// The v0.55 migration renamed session_active_capabilities -> session_active_caps
// and capability_name -> cap_name (internal/storage/schema.go:420-421). A delete
// keyed on the old name fails at prepare time, so an activated entry is never
// removed.
func TestCapDelete_UsesRenamedCapNameColumn(t *testing.T) {
	body := capDeleteBody(t)
	if strings.Contains(body, "capability_name") {
		t.Error("cap_delete references capability_name; the column has been cap_name since schema v0.55")
	}
	if !strings.Contains(body, "DELETE FROM session_active_caps WHERE cap_name=") {
		t.Error("cap_delete does not delete from session_active_caps keyed on cap_name")
	}
}

// caps.db is the daemon's cap store (internal/storage/cap_store.go OpenCapsDB:
// dir + "/caps.db"); capabilities.db is the legacy file name. The cap_<name>__*
// data tables and cap_store_meta live in caps.db, and session_active_caps exists
// in both yesmem.db and caps.db (storage/session_caps.go capDB()).
func TestCapDelete_TargetsCapsDB(t *testing.T) {
	body := capDeleteBody(t)
	if !strings.Contains(body, "caps.db") {
		t.Error("cap_delete does not reference caps.db, the daemon's cap store")
	}
	if !strings.Contains(body, "cap_store_meta") {
		t.Error("cap_delete no longer cleans cap_store_meta")
	}
}

// stripComments drops // line comments so a SQL assertion cannot be tripped by
// prose in a comment (which happened while writing these tests). The handler
// builds every path as dir + '/name', so no string literal contains '//'.
func stripComments(js string) string {
	lines := strings.Split(js, "\n")
	for i, line := range lines {
		if j := strings.Index(line, "//"); j >= 0 {
			lines[i] = line[:j]
		}
	}
	return strings.Join(lines, "\n")
}

// The handler can only see failures through the captured output, and plain
// sqlite3 can commit a half-applied transaction. -bail plus a completion sentinel
// makes a failed run unambiguous.
func TestCapDelete_FailsLoudlyOnSqlError(t *testing.T) {
	body := capDeleteBody(t)
	if !strings.Contains(body, "-bail") {
		t.Error("cap_delete does not run sqlite3 with -bail, so a SQL error can leave a half-applied delete committed")
	}
	if !strings.Contains(body, "__OK__") {
		t.Error("cap_delete has no completion sentinel, so it cannot tell a clean run from a failed one")
	}
	if !strings.Contains(body, "knowledge_gaps") {
		t.Error("cap_delete dropped the knowledge_gaps.resolved_by safety check")
	}

	code := stripComments(body)
	// stderr must stay separate: the daemon's sh() throws with stderr, so merging
	// it into stdout replaces the SQL error with a bare "exit code N".
	if strings.Contains(code, "2>&1") {
		t.Error("cap_delete merges stderr into stdout, which hides the reason a statement failed")
	}
	// '_' is a wildcard in the old pattern syntax, so a pattern for cap_delete also
	// matches a sibling cap named capXdelete — the id lookup must match exactly.
	if strings.Contains(code, "LIKE") {
		t.Error("cap_delete selects the learnings row with a wildcard pattern, which over-matches names differing by '_'")
	}
	// A throw from the cap-store stage must not escape unreported after the
	// yesmem.db half has committed.
	if !strings.Contains(body, "try {") || !strings.Contains(body, "partial:") {
		t.Error("cap_delete does not report a partially applied delete when the cap-store stage throws")
	}
}

// The lookup is restricted to cap-ish categories, so an unrelated row that
// happens to start with the same prefix is never swept up.
func TestCapDelete_RestrictsCategory(t *testing.T) {
	code := stripComments(capDeleteBody(t))
	if !strings.Contains(code, "category IN") {
		t.Error("cap_delete deletes every row with the name prefix, regardless of category")
	}
	if !strings.Contains(code, "cap_proposed") {
		t.Error("cap_delete does not include staged proposal rows for the cap")
	}
}

// Cap names are lower case by construction (storage.ValidateCapName) and the
// cap_<name>__ prefix match is case-sensitive, so an upper-case argument would
// delete the learnings row but leave every data table behind.
func TestCapDelete_NormalisesCapName(t *testing.T) {
	body := capDeleteBody(t)
	if !strings.Contains(body, "toLowerCase()") {
		t.Error("cap_delete does not normalise cap_name, so a mixed-case name silently skips the data tables")
	}
	if !strings.Contains(body, "found:") {
		t.Error("cap_delete does not report whether a learnings row matched, so a missing cap looks like a success")
	}
	if !strings.Contains(body, "SyncCapsFromDisk") {
		t.Error("cap_delete does not warn that a CAP.md left on disk is re-imported at the next daemon start")
	}
}

// CAP.md under caps/bundled-caps/ is the go:embed source of truth. The frontmatter
// version is a dedup key that WriteCapToDisk compares against the version already
// on disk (cap_sync.go: "skip if diskCF.Version >= cf.Version"), so it must exceed
// the highest deployed one — the live install sits at 545.
func TestCapDelete_VersionExceedsDeployed(t *testing.T) {
	cf := loadCapDelete(t)
	if cf.Version <= 545 {
		t.Errorf("version = %d, want > 545 (the version deployed on this install); a lower number lets the DB write the old body back over disk", cf.Version)
	}
}

// TestCapDelete_SqlMatchesRealSchema runs the fragment the cap actually issues
// against a fixture carrying the post-v0.55 schema (mirrored from
// internal/storage/schema.go:420-421), and shows the pre-rename column failing.
// It pins the cap's assumption; it does not re-derive the column from the schema
// source, so a rename there still needs the comment string here updated.
func TestCapDelete_SqlMatchesRealSchema(t *testing.T) {
	body := capDeleteBody(t)
	const frag = "DELETE FROM session_active_caps WHERE cap_name="
	if !strings.Contains(body, frag) {
		t.Fatalf("cap_delete body does not contain %q", frag)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE session_active_caps (
		thread_id    TEXT NOT NULL,
		cap_name     TEXT NOT NULL,
		activated_at INTEGER NOT NULL,
		last_used_at INTEGER,
		PRIMARY KEY (thread_id, cap_name)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_active_caps VALUES ('th1','fixture_cap',1,NULL)`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(frag + "'fixture_cap';"); err != nil {
		t.Fatalf("the cap's delete statement fails against the real schema: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM session_active_caps WHERE cap_name='fixture_cap'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("activated row not removed: %d left", n)
	}

	if _, err := db.Exec(`DELETE FROM session_active_caps WHERE capability_name='fixture_cap';`); err == nil {
		t.Error("expected the pre-v0.55 column to fail against this schema, got no error")
	}
}
