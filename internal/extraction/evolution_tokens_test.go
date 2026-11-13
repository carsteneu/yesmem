package extraction

import "testing"

// The bulk evolution call asked for no output budget, so it ran on the 8192-token
// default while it hands the model up to fifty learnings and expects a JSON verdict on
// each pair. On 2026-11-13 that produced "chat: empty response" and "unexpected end of
// JSON input" for greenTile and ccm19_webpage, three in a row, and the run aborted —
// every abort leaves a project's duplicates and contradictions unconsolidated until
// the next night. The budget is the fix, so it is what this test pins.
func TestRunEvolutionAsksForEnoughOutputRoom(t *testing.T) {
	store := mustOpenStore(t)
	insertTestLearning(store, "the daemon retries a socket bind after a restart", "pattern")
	insertTestLearning(store, "sandbox denies network egress to the checkout", "pattern")

	recorder := &evolutionTokenRecorder{}
	e := &Extractor{client: recorder}
	e.runEvolution(store, nil, nil, 0)

	if recorder.maxTokens != evolutionBulkMaxTokens {
		t.Fatalf("bulk evolution asked for %d output tokens, want %d: the default 8192 truncates a fifty-learning verdict",
			recorder.maxTokens, evolutionBulkMaxTokens)
	}
}

// evolutionTokenRecorder records the output budget the extractor asks for. It answers
// with an empty verdict, which is enough: the question here is the request, not the parse.
type evolutionTokenRecorder struct {
	maxTokens int
}

func (f *evolutionTokenRecorder) Complete(system, userMsg string, opts ...CallOption) (string, error) {
	return "", nil
}

func (f *evolutionTokenRecorder) CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	o := callOpts{maxTokens: 8192}
	for _, opt := range opts {
		opt(&o)
	}
	f.maxTokens = o.maxTokens
	return `{"supersede":[]}`, nil
}

func (f *evolutionTokenRecorder) Name() string  { return "fake" }
func (f *evolutionTokenRecorder) Model() string { return "fake-model" }
