package engine

import (
	"slices"
	"testing"
)

// Ein Agent hat mehr als eine Kennung: die Daemon-UUID seiner Zeile und die
// Backend-Session, unter der das Modell seinen Plan schreibt. Der Aufrufer nennt
// die Kennungen in der Reihenfolge, in der sie gelten; der primaere Thread
// schliesst die Liste, weil er der Schluessel des Urteils ist und nicht die
// Vorliebe, welcher Plan der aktuelle ist.
func TestPlanThreadCandidates(t *testing.T) {
	cases := []struct {
		name string
		ctx  Ctx
		want []string
	}{
		{
			name: "the primary thread alone is the list",
			ctx:  Ctx{PlanThread: "b"},
			want: []string{"b"},
		},
		{
			name: "the caller's order leads and the primary closes it",
			ctx:  Ctx{PlanThread: "b", PlanThreads: []string{"a"}},
			want: []string{"a", "b"},
		},
		{
			name: "a list that already names the primary keeps its order",
			ctx:  Ctx{PlanThread: "b", PlanThreads: []string{"b", "a"}},
			want: []string{"b", "a"},
		},
		{
			name: "a list without a primary keeps its order",
			ctx:  Ctx{PlanThreads: []string{"a", "b"}},
			want: []string{"a", "b"},
		},
		{
			name: "empty entries are dropped",
			ctx:  Ctx{PlanThread: "", PlanThreads: []string{"", "b", ""}},
			want: []string{"b"},
		},
		{
			name: "an empty context names no thread",
			ctx:  Ctx{},
			want: nil,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.ctx.PlanThreadCandidates(); !slices.Equal(got, test.want) {
				t.Errorf("PlanThreadCandidates() = %v, want %v", got, test.want)
			}
		})
	}
}
