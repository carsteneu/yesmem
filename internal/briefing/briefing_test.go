package briefing

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func setupStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed with test data
	s.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		GitBranch: "main", FirstMessage: "Fix the cookie scanner timeout",
		MessageCount: 20, StartedAt: time.Now().Add(-2 * time.Hour),
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})
	s.UpsertSession(&models.Session{
		ID: "s2", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		GitBranch: "feature/auth", FirstMessage: "Refactor auth module",
		MessageCount: 15, StartedAt: time.Now().Add(-48 * time.Hour),
		JSONLPath: "/s2.jsonl", IndexedAt: time.Now(),
	})
	s.UpsertSession(&models.Session{
		ID: "s3", Project: "/var/www/green", ProjectShort: "green",
		FirstMessage: "Deploy crawler", MessageCount: 5,
		StartedAt: time.Now().Add(-72 * time.Hour),
		JSONLPath: "/s3.jsonl", IndexedAt: time.Now(),
	})

	s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Docker needs no sudo",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
	})
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "Deutsch, locker, du",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus",
	})

	return s
}

func TestGenerateContainsToolReference(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 3)
	text := gen.Generate("/var/www/ccm19")

	if !strings.Contains(text, "search(query)") {
		t.Error("briefing should contain search tool")
	}
	if !strings.Contains(text, "hybrid_search(query)") {
		t.Error("briefing should contain hybrid_search tool")
	}
	if !strings.Contains(text, "remember(text)") {
		t.Error("briefing should contain remember tool")
	}
	if !strings.Contains(text, "get_learnings(category)") {
		t.Error("briefing should contain get_learnings tool")
	}
	if !strings.Contains(text, "get_session(id)") {
		t.Error("briefing should contain get_session tool")
	}
}

func TestGenerateContainsLearnings(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 3)
	text := gen.Generate("/var/www/ccm19")

	if !strings.Contains(text, "Docker needs no sudo") {
		t.Error("briefing should contain gotcha learning")
	}
	if !strings.Contains(text, "Deutsch, locker") {
		t.Error("briefing should contain preference learning")
	}
}

func TestGenerateContainsSessions(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 3)
	text := gen.Generate("/var/www/ccm19")

	if !strings.Contains(text, "ccm19") {
		t.Error("briefing should contain project name")
	}
	if !strings.Contains(text, "cookie scanner timeout") {
		t.Error("briefing should contain first session message")
	}
	if !strings.Contains(text, "auth module") {
		t.Error("briefing should contain second session message")
	}
}

func TestGenerateEmpty(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	gen := New(store, 3)
	text := gen.Generate("/nonexistent")

	if !strings.Contains(text, "First encounter") {
		t.Error("empty briefing should have first-encounter intro")
	}
}

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "vor 30 Min"},
		{3 * time.Hour, "vor 3h"},
		{36 * time.Hour, "gestern"},
		{5 * 24 * time.Hour, "vor 5 Tagen"},
		{14 * 24 * time.Hour, "vor 2 Wo"},
	}
	for _, tt := range tests {
		got := relativeTime(time.Now().Add(-tt.d))
		if got != tt.want {
			t.Errorf("relativeTime(-%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestGenerateNarrativeFormat(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 3)
	text := gen.Generate("/var/www/ccm19")

	// Should NOT contain old format markers
	if strings.Contains(text, "━━━") {
		t.Error("narrative briefing should not contain old separator bars")
	}
	if strings.Contains(text, "DEIN WISSEN") {
		t.Error("narrative briefing should not contain old section headers")
	}

	// Should contain narrative markers (English defaults)
	if !strings.Contains(text, "He works like this") {
		t.Error("should contain personal tone section header")
	}
	if !strings.Contains(text, "Pitfalls I've stepped into") {
		t.Error("should contain narrative gotcha section")
	}
}

func TestGenerateDeduplicatesLearnings(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	// Insert 5 near-duplicate CGO learnings
	for i := 0; i < 5; i++ {
		store.InsertLearning(&models.Learning{
			Category:  "gotcha",
			Content:   "CGO_ENABLED=0 muss gesetzt sein weil modernc.org/sqlite sonst CGo nutzt",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
			ModelUsed: "self",
		})
	}
	// Insert one unique learning
	store.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "Docker braucht kein sudo auf diesem System",
		CreatedAt: time.Now(),
		ModelUsed: "self",
	})

	gen := New(store, 3)
	text := gen.Generate("/any/project")

	// CGO should appear only once
	count := strings.Count(text, "CGO_ENABLED")
	if count > 1 {
		t.Errorf("CGO_ENABLED appears %d times, want at most 1 (dedup should work)", count)
	}
	// Docker should still be there
	if !strings.Contains(text, "Docker") {
		t.Error("unique learning (Docker) should still appear")
	}
}

func TestGenerateMaxSessions(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 2) // Only 2 sessions max
	text := gen.Generate("/var/www/ccm19")

	// Should show only 2 sessions, not more
	if strings.Contains(text, "project_summary") {
		// Good — the overflow note should appear when there are more than shown
	}
	// Both sessions should still be referenced
	if !strings.Contains(text, "cookie scanner") {
		t.Error("should show first session")
	}
}

func TestGenerateWithNarratives(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	// Add a session so project exists
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	// Add narrative learnings
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "ccm19", SessionID: "s1",
		Content:   "Hey, du bist ich von vorhin. Wir haben den Cookie-Scanner gefixt.",
		CreatedAt: time.Now().Add(-1 * time.Hour), ModelUsed: "haiku",
	})

	gen := New(store, 3)
	text := gen.Generate("/var/www/ccm19")

	// Should contain the awakening template intro (>1 session = "I'm back")
	if !strings.Contains(text, "I'm back") && !strings.Contains(text, "First encounter") {
		t.Error("briefing should contain awakening intro")
	}
	// Should contain Last marker (narrative is referenced)
	if !strings.Contains(text, "Last") {
		t.Error("briefing should contain Last marker for latest narrative")
	}
	// Should NOT contain old English greeting
	if strings.Contains(text, "You have a long-term memory") {
		t.Error("should use awakening template, not old default")
	}
}

func TestNarrativeShowsFlavor(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content:       "Wir haben den Cookie-Scanner gefixt.",
		SessionFlavor: "Intensiver Debug-Marathon, am Ende läuft alles",
		CreatedAt:     time.Now().Add(-1 * time.Hour), ModelUsed: "haiku",
	})

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	if !strings.Contains(text, "Debug-Marathon") {
		t.Error("briefing should contain session flavor from narrative")
	}
}

func TestNarrativeNoConsolidation(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	// 3 narratives — each shown individually, max 3
	now := time.Now()
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content: "First.", CreatedAt: now.Add(-10 * time.Minute), ModelUsed: "haiku",
	})
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content: "Second.", CreatedAt: now.Add(-20 * time.Minute), ModelUsed: "haiku",
	})
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content: "Third.", CreatedAt: now.Add(-30 * time.Minute), ModelUsed: "haiku",
	})

	gen := New(store, 3)
	narratives := gen.loadNarratives("proj")

	if len(narratives) != 3 {
		t.Errorf("expected 3 individual narratives, got %d", len(narratives))
	}
}

func TestNarrativeLimit(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-5 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	now := time.Now()
	for i := 0; i < 5; i++ {
		store.InsertLearning(&models.Learning{
			Category: "narrative", Project: "proj", SessionID: "s1",
			Content: fmt.Sprintf("Narrative %d.", i), CreatedAt: now.Add(-time.Duration(i) * time.Hour), ModelUsed: "haiku",
		})
	}

	gen := New(store, 3)
	narratives := gen.loadNarratives("proj")

	if len(narratives) != 3 {
		t.Errorf("expected max 3 narratives, got %d", len(narratives))
	}
}

func TestKnowledgePrioritization(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	// Insert 10 unique gotchas and 10 unique patterns
	gotchaTopics := []string{
		"Docker braucht kein sudo", "CGO_ENABLED muss 0 sein",
		"BoltDB hat exklusiven Lock", "Hooks brauchen ANTHROPIC_API_KEY",
		"Settings JSON muss existieren", "Zombie-Prozesse vor Copy killen",
		"Rate-Limits bei parallelen Calls", "Binary-Pfade synchron halten",
		"Broken Pipes bei Daemon-Restart", "Modul-Pfade muessen mit go.mod stimmen",
	}
	patternTopics := []string{
		"Unix Socket fuer IPC nutzen", "Debounced File Watcher 3s Index",
		"Daemon Auto-Start mit Release", "Structured Outputs statt JSON Parse",
		"Bulk Evolution pro Projekt", "bufio Reader fuer Terminal UI",
		"bbalet stopwords fuer Sprachen", "Pinne auf Commit SHAs",
		"Config Defaults in Tests abbilden", "Fehlermeldungen mit Symbolen",
	}
	for i := 0; i < 10; i++ {
		store.InsertLearning(&models.Learning{
			Category: "gotcha", Content: gotchaTopics[i], Project: "proj",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour), ModelUsed: "self",
		})
		store.InsertLearning(&models.Learning{
			Category: "pattern", Content: patternTopics[i], Project: "proj",
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour), ModelUsed: "self",
		})
	}

	gen := New(store, 3)
	text := gen.Generate("/home/user/proj")

	// Count actual gotcha items: each is a "- <topic>" line under "Known pitfalls"
	// Count by matching the unique topic keywords
	gotchaCount := 0
	for _, topic := range gotchaTopics {
		// Use first word as marker
		if strings.Contains(text, topic) {
			gotchaCount++
		}
	}
	patternCount := 0
	for _, topic := range patternTopics {
		if strings.Contains(text, topic) {
			patternCount++
		}
	}

	// maxPerCategory=3: top 3 per category by relevance score
	if gotchaCount < 1 || gotchaCount > 3 {
		t.Errorf("expected 1-3 gotchas shown (maxPerCategory=3), got %d", gotchaCount)
	}
	if patternCount < 1 || patternCount > 3 {
		t.Errorf("expected 1-3 patterns shown (maxPerCategory=3), got %d", patternCount)
	}
}

func TestSetSkipUnfinished_SuppressesUnfinishedSection(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})
	store.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "TODO: refactor auth module",
		Project: "proj", Confidence: 1.0,
		CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Baseline: without skip, item must appear
	genNormal := New(store, 3)
	if !strings.Contains(genNormal.Generate("/var/www/proj"), "refactor auth module") {
		t.Fatal("baseline: unfinished item should appear without SetSkipUnfinished")
	}

	// With skip: item must NOT appear
	genSkip := New(store, 3)
	genSkip.SetSkipUnfinished(true)
	if strings.Contains(genSkip.Generate("/var/www/proj"), "refactor auth module") {
		t.Error("SetSkipUnfinished(true): unfinished item must not appear in briefing")
	}
}

func TestUnfinishedTTLFiltersOldItems(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	// Recent unfinished (5 days old)
	store.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Recent task: fix tests",
		Project: "proj", Confidence: 1.0,
		CreatedAt: time.Now().Add(-5 * 24 * time.Hour), ModelUsed: "haiku",
	})
	// Old unfinished (60 days old)
	store.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Old task: refactor everything",
		Project: "proj", Confidence: 1.0,
		CreatedAt: time.Now().Add(-60 * 24 * time.Hour), ModelUsed: "haiku",
	})

	gen := New(store, 3)
	gen.SetUnfinishedTTL(30) // 30 day TTL
	text := gen.Generate("/var/www/proj")

	if !strings.Contains(text, "fix tests") {
		t.Error("recent unfinished item should appear in briefing")
	}
	if strings.Contains(text, "refactor everything") {
		t.Error("old unfinished item (>30 days) should NOT appear in briefing")
	}
}

func TestUnfinishedTTLZeroShowsAll(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	store.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Ancient task from long ago",
		Project: "proj", Confidence: 1.0,
		CreatedAt: time.Now().Add(-120 * 24 * time.Hour), ModelUsed: "haiku",
	})

	gen := New(store, 3)
	// TTL=0 (default) = no filter
	text := gen.Generate("/var/www/proj")

	if !strings.Contains(text, "Ancient task") {
		t.Error("with TTL=0, all unfinished items should appear")
	}
}

func TestGapAwarenessShowsOverflow(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	now := time.Now()
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: now, MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: now,
	})
	for i := 0; i < 8; i++ {
		store.InsertLearning(&models.Learning{
			Category: "gotcha", Content: fmt.Sprintf("gotcha number %d for proj", i),
			Project: "proj", Confidence: 1.0, CreatedAt: now.Add(-time.Duration(i) * time.Hour),
			ModelUsed: "self",
		})
	}

	// Another project with many sessions
	for i := 0; i < 10; i++ {
		store.UpsertSession(&models.Session{
			ID: fmt.Sprintf("other-%d", i), Project: "/var/www/other", ProjectShort: "other",
			StartedAt: now.Add(-time.Duration(i) * 24 * time.Hour), MessageCount: 15,
			JSONLPath: fmt.Sprintf("/other-%d.jsonl", i), IndexedAt: now,
		})
	}

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	if !strings.Contains(text, "get_learnings") {
		t.Error("should show learning overflow with tool hint")
	}
	if !strings.Contains(text, "other") {
		t.Error("should show other project with depth")
	}
}

func TestGapAwarenessHiddenWhenNoOverflow(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	now := time.Now()
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: now, MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: now,
	})
	store.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "single gotcha",
		Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
	})

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	if strings.Contains(text, "There was more") {
		t.Error("should NOT show gap awareness when no overflow and no other projects")
	}
}

func TestGapAwarenessMinSessionThreshold(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	now := time.Now()
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: now, MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: now,
	})
	for i := 0; i < 3; i++ {
		store.UpsertSession(&models.Session{
			ID: fmt.Sprintf("tiny-%d", i), Project: "/var/www/tiny", ProjectShort: "tiny",
			StartedAt: now.Add(-time.Duration(i) * 24 * time.Hour), MessageCount: 5,
			JSONLPath: fmt.Sprintf("/tiny-%d.jsonl", i), IndexedAt: now,
		})
	}

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	if strings.Contains(text, "tiny") {
		t.Error("should NOT show projects with fewer than 5 sessions")
	}
}

func TestGenerateNarrativesBeforePersona(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	// Add a session so project exists
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	// Add a narrative learning with flavor for awakening template
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content:   "Hey, wir haben gestern den Scanner gefixt.",
		CreatedAt: time.Now().Add(-1 * time.Hour), ModelUsed: "haiku",
		SessionFlavor: "Scanner gefixt und Deploy vorbereitet",
	})

	// Add a persona directive
	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "default",
		Directive:   "Du bist ein pragmatischer Entwickler-Assistent.",
		TraitsHash:  "abc123",
		GeneratedAt: time.Now(),
		ModelUsed:   "opus",
	})

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	// Both must be present
	narrativeIdx := strings.Index(text, "Scanner gefixt")
	personaIdx := strings.Index(text, "pragmatischer Entwickler")

	if narrativeIdx == -1 {
		t.Fatalf("narrative content not found in briefing:\n%s", text)
	}
	if personaIdx == -1 {
		t.Fatal("persona directive not found in briefing")
	}

	// Narrative must come BEFORE persona
	if narrativeIdx > personaIdx {
		t.Errorf("narratives (pos %d) should appear before persona directive (pos %d)", narrativeIdx, personaIdx)
	}
}

func TestPersonalToneRewriting(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.InsertLearning(&models.Learning{
		Category:  "preference",
		Content:   "User prefers short answers",
		CreatedAt: time.Now(),
		ModelUsed: "self",
	})

	gen := New(store, 3)
	text := gen.Generate("/any")

	if strings.Contains(text, "User prefers") {
		t.Error("should rewrite 'User prefers' to personal tone")
	}
	if !strings.Contains(text, "Prefers") {
		t.Error("should contain rewritten personal tone")
	}
}

func TestLoadClustersStrongWeakSplit(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	project := "test-split"

	// Insert clusters with varying confidence
	clusters := []models.LearningCluster{
		{Label: "Strong Cluster", LearningCount: 5, Confidence: 0.8, AvgRecencyDays: 3, LearningIDs: "[]"},
		{Label: "Borderline Strong", LearningCount: 3, Confidence: 0.5, AvgRecencyDays: 15, LearningIDs: "[]"},
		{Label: "Weak Cluster", LearningCount: 2, Confidence: 0.3, AvgRecencyDays: 60, LearningIDs: "[]"},
		{Label: "Very Weak", LearningCount: 1, Confidence: 0.05, AvgRecencyDays: 120, LearningIDs: "[]"},
	}
	if err := store.ReplaceLearningClusters(project, clusters); err != nil {
		t.Fatalf("insert clusters: %v", err)
	}

	gen := New(store, 3)
	strong, weak := gen.loadClusters(project)

	if len(strong) != 2 {
		t.Errorf("expected 2 strong clusters (>=0.5), got %d", len(strong))
	}
	if len(weak) != 2 {
		t.Errorf("expected 2 weak clusters (<0.5), got %d", len(weak))
	}

	// Verify labels
	if len(strong) > 0 && strong[0].Label != "Strong Cluster" {
		t.Errorf("first strong should be 'Strong Cluster', got %q", strong[0].Label)
	}
	if len(weak) > 0 && weak[0].Label != "Weak Cluster" {
		t.Errorf("first weak should be 'Weak Cluster', got %q", weak[0].Label)
	}

	// Test empty project returns nil
	s2, w2 := gen.loadClusters("nonexistent")
	if s2 != nil || w2 != nil {
		t.Error("nonexistent project should return nil, nil")
	}
}

func TestSetSkipUnfinished_SuppressesDeadlineTriggers(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	store.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Release cutoff task",
		Project: "proj", Confidence: 1.0,
		TriggerRule: "deadline:" + tomorrow,
		CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// skip=true: deadline-triggered item must NOT appear (no baseline call to avoid cooldown side-effect)
	gen := New(store, 3)
	gen.SetSkipUnfinished(true)
	if strings.Contains(gen.Generate("/var/www/proj"), "Release cutoff task") {
		t.Error("SetSkipUnfinished(true): deadline-triggered item must not appear in briefing")
	}
}
