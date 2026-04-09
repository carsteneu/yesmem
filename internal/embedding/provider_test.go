package embedding

import (
	"context"
	"testing"
)

func TestProviderInterface(t *testing.T) {
	// Provider muss Texte zu Vektoren konvertieren
	var p Provider
	_ = p // Kompiliert nicht wenn Interface nicht existiert
}

func TestNoneProvider(t *testing.T) {
	p := NewNoneProvider()
	ctx := context.Background()

	vectors, err := p.Embed(ctx, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if vectors != nil {
		t.Fatal("NoneProvider should return nil vectors")
	}
	if p.Dimensions() != 0 {
		t.Fatal("NoneProvider should have 0 dimensions")
	}
	if p.Enabled() {
		t.Fatal("NoneProvider should not be enabled")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("NoneProvider.Close() should not error: %v", err)
	}
}

func TestNoneProviderImplementsInterface(t *testing.T) {
	var _ Provider = NewNoneProvider()
}
