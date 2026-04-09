package embedding

import "context"

// Provider ist das Interface für alle Embedding-Backends.
type Provider interface {
	// Embed konvertiert Texte zu Vektoren.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions gibt die Vektor-Dimensionalität zurück (z.B. 384 für MiniLM).
	Dimensions() int

	// Enabled gibt zurück ob Vector Search aktiv ist.
	Enabled() bool

	// Close gibt Ressourcen frei.
	Close() error
}

// NoneProvider ist der Fallback wenn Vector Search deaktiviert ist.
type NoneProvider struct{}

func NewNoneProvider() *NoneProvider {
	return &NoneProvider{}
}

func (n *NoneProvider) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, nil
}

func (n *NoneProvider) Dimensions() int { return 0 }
func (n *NoneProvider) Enabled() bool   { return false }
func (n *NoneProvider) Close() error    { return nil }
