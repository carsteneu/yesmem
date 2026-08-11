package setup

import "testing"

func TestStatusDatePrefixHandlesShortValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: ""},
		{name: "short", value: "2026-08", want: "2026-08"},
		{name: "date", value: "2026-08-01", want: "2026-08-01"},
		{name: "timestamp", value: "2026-08-01T12:00:00Z", want: "2026-08-01"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := statusDatePrefix(test.value); got != test.want {
				t.Fatalf("statusDatePrefix(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}
