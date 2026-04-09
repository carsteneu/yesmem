package extraction

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// insertTestLearningFTS inserts a learning and syncs the FTS5 index.
// In tests there is no background sync goroutine, so BM25 search
// would return zero results without an explicit sync.
func insertTestLearningFTS(store *storage.Store, content, category string) int64 {
	id := insertTestLearning(store, content, category)
	store.SyncFTSNow()
	return id
}

func TestCheckPreAdmission_ExactDuplicate(t *testing.T) {
	store := mustOpenStore(t)
	insertTestLearningFTS(store, "git push zu bitbucket.org scheitert in Claude Code Sandbox", "gotcha")

	result := CheckPreAdmission(store, &models.Learning{
		Content:  "git push zu bitbucket.org scheitert in Claude Code Sandbox",
		Category: "gotcha", Project: "testproj",
	})
	if result.Action != PreAdmissionSkip {
		t.Errorf("expected Skip for exact duplicate, got %v", result.Action)
	}
}

func TestCheckPreAdmission_SimilarContent(t *testing.T) {
	store := mustOpenStore(t)
	insertTestLearningFTS(store, "git push zu bitbucket.org scheitert in der Claude Code Sandbox wegen DNS Block", "gotcha")

	result := CheckPreAdmission(store, &models.Learning{
		Content:  "git push zu bitbucket scheitert in Claude Code Sandbox wegen DNS Block",
		Category: "gotcha", Project: "testproj",
	})
	if result.Action != PreAdmissionSkip {
		t.Errorf("expected Skip for similar content, got %v (reason: %s)", result.Action, result.Reason)
	}
}

func TestCheckPreAdmission_Independent(t *testing.T) {
	store := mustOpenStore(t)
	insertTestLearningFTS(store, "git push zu bitbucket.org scheitert in Claude Code Sandbox", "gotcha")

	result := CheckPreAdmission(store, &models.Learning{
		Content:  "Schema-Migration v0.21 braucht neue Spalten source_file und source_hash",
		Category: "decision", Project: "testproj",
	})
	if result.Action != PreAdmissionInsert {
		t.Errorf("expected Insert for independent learning, got %v", result.Action)
	}
}

func TestCheckPreAdmission_UpdateExisting(t *testing.T) {
	store := mustOpenStore(t)
	existingID := insertTestLearningFTS(store, "BM25 FTS5 nutzt OR-Matching — ein einzelnes Keyword reicht für Treffer", "decision")

	result := CheckPreAdmission(store, &models.Learning{
		Content:  "BM25 FTS5 nutzte OR-Matching — ein einzelnes Keyword reichte für einen Treffer. Fix: AND mit Min-3-Terms-Bedingung verhindert Noise und verbessert die Präzision erheblich",
		Category: "decision", Project: "testproj",
	})
	if result.Action != PreAdmissionUpdate {
		t.Errorf("expected Update for enriching learning, got %v (reason: %s)", result.Action, result.Reason)
	}
	if result.ExistingID != existingID {
		t.Errorf("expected ExistingID=%d, got %d", existingID, result.ExistingID)
	}
}

func TestPreAdmissionStats(t *testing.T) {
	store := mustOpenStore(t)

	insertTestLearningFTS(store, "User bevorzugt Deutsch in allen Antworten", "preference")
	insertTestLearningFTS(store, "git push scheitert in Sandbox", "gotcha")

	learnings := []*models.Learning{
		{Content: "User bevorzugt Deutsch in allen Antworten", Category: "preference", Project: "testproj"},
		{Content: "git push scheitert in der Claude Code Sandbox wegen DNS", Category: "gotcha", Project: "testproj"},
		{Content: "Schema-Migration v0.21 braucht source_file Spalte", Category: "decision", Project: "testproj"},
	}

	var skipped, updated, inserted int
	for _, l := range learnings {
		result := CheckPreAdmission(store, l)
		switch result.Action {
		case PreAdmissionSkip:
			skipped++
		case PreAdmissionUpdate:
			updated++
		case PreAdmissionInsert:
			inserted++
		}
	}

	// "User bevorzugt Deutsch..." is an exact duplicate → skip
	// "git push scheitert in der Claude Code Sandbox wegen DNS" enriches the
	// shorter existing "git push scheitert in Sandbox" → update (not skip)
	// "Schema-Migration v0.21 braucht source_file Spalte" is new → insert
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	if updated != 1 {
		t.Errorf("expected 1 updated, got %d", updated)
	}
	if inserted != 1 {
		t.Errorf("expected 1 inserted, got %d", inserted)
	}
}
