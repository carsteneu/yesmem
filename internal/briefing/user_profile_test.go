package briefing

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestUserProfileExcludesPersonaDirective(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	// Add a session so project exists
	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	// Add a narrative for awakening section
	store.InsertLearning(&models.Learning{
		Category: "narrative", Project: "proj", SessionID: "s1",
		Content:       "Wir haben den Scanner gefixt.",
		SessionFlavor: "Debug-Session mit Scanner-Fix",
		CreatedAt:     time.Now().Add(-1 * time.Hour), ModelUsed: "haiku",
	})

	// Add persona directive
	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "default",
		Directive:   "Du bist ein pragmatischer Entwickler-Assistent.",
		TraitsHash:  "abc123",
		GeneratedAt: time.Now(),
		ModelUsed:   "opus",
	})

	// Add user profile
	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "user_profile",
		Directive:   "Ein erfahrener Go-Entwickler der TDD praktiziert.",
		TraitsHash:  "profilehash",
		GeneratedAt: time.Now(),
		ModelUsed:   "opus",
	})

	gen := New(store, 3)
	text := gen.Generate("/var/www/proj")

	// User profile must be present
	if !strings.Contains(text, "erfahrener Go-Entwickler") {
		t.Fatalf("user profile not found in briefing:\n%s", text)
	}

	// Persona directive must NOT be present (mutually exclusive)
	if strings.Contains(text, "pragmatischer Entwickler") {
		t.Errorf("persona directive should not appear when user profile is active:\n%s", text)
	}

	// Awakening must appear before user profile
	awakeningIdx := strings.Index(text, "Scanner-Fix")
	profileIdx := strings.Index(text, "erfahrener Go-Entwickler")
	if awakeningIdx == -1 {
		t.Fatalf("awakening narrative not found in briefing:\n%s", text)
	}
	if awakeningIdx > profileIdx {
		t.Errorf("awakening (pos %d) should appear before user profile (pos %d)", awakeningIdx, profileIdx)
	}
}

func TestUserProfileEmptyWhenNotSet(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now().Add(-2 * time.Hour), MessageCount: 20,
		JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})

	gen := New(store, 3)
	profile := gen.loadUserProfile()

	if profile != "" {
		t.Errorf("expected empty profile when not set, got %q", profile)
	}
}

func TestLoadUserProfile(t *testing.T) {
	store, _ := storage.Open(":memory:")
	defer store.Close()

	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "user_profile",
		Directive:   "Ein pragmatischer Architekt.",
		TraitsHash:  "hash123",
		GeneratedAt: time.Now(),
		ModelUsed:   "opus",
	})

	gen := New(store, 3)
	profile := gen.loadUserProfile()

	if profile != "Ein pragmatischer Architekt." {
		t.Errorf("expected profile text, got %q", profile)
	}
}
