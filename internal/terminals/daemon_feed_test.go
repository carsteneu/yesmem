package terminals

import "testing"

func TestStripAgentPrefix(t *testing.T) {
	cases := map[string]string{
		"opencode:ses_abc": "ses_abc",
		"claude:ses_x":     "ses_x",
		"codex:s3":         "s3",
		"ses_plain":        "ses_plain",
		"opencode:":        "",
	}
	for in, want := range cases {
		if got := stripAgentPrefix(in); got != want {
			t.Fatalf("stripAgentPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchUniqueWindow(t *testing.T) {
	ws := []Window{
		{XID: "0xa", Kind: "shell"},
		{XID: "0xb", Kind: "shell"},
		{XID: "0xc", Kind: "shell"},
	}
	owners := map[string]int{"0xa": 100, "0xb": 200, "0xc": 300}

	t.Run("eindeutig", func(t *testing.T) {
		got := matchUniqueWindow(ws, owners, map[int]bool{200: true})
		if got == nil || got.XID != "0xb" {
			t.Fatalf("want 0xb, got %+v", got)
		}
	})
	t.Run("mehrdeutig", func(t *testing.T) {
		if got := matchUniqueWindow(ws, owners, map[int]bool{100: true, 300: true}); got != nil {
			t.Fatalf("want nil (ambiguous), got %+v", got)
		}
	})
	t.Run("keiner", func(t *testing.T) {
		if got := matchUniqueWindow(ws, owners, map[int]bool{999: true}); got != nil {
			t.Fatalf("want nil, got %+v", got)
		}
	})
}
