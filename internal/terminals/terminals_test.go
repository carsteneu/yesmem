package terminals

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClassifyEmulator(t *testing.T) {
	cases := []struct {
		name string
		cls  []string
		want string
	}{
		{"ghostty", []string{"ghostty", "com.mitchellh.ghostty"}, "ghostty"},
		{"ghostty-single", []string{"com.mitchellh.ghostty"}, "ghostty"},
		{"warp", []string{"dev.warp.Warp", "dev.warp.Warp"}, "warp"},
		{"warp-single", []string{"warp"}, "warp"},
		{"unknown", []string{"SciTE", "SciTE"}, ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyEmulator(c.cls); got != c.want {
				t.Fatalf("ClassifyEmulator(%v) = %q, want %q", c.cls, got, c.want)
			}
		})
	}
}

func TestParseWmctrlLine(t *testing.T) {
	line := "0x05600004  0  80  210 1920 1160  chieftp32  OC | Moin! Projekt"
	w, ok := ParseWmctrlLine(line)
	if !ok {
		t.Fatal("expected parse ok")
	}
	// Felder: id desktop x y w h host title...
	if w.XID != "0x5600004" || w.Workspace != 0 || w.X != 80 || w.Y != 210 || w.W != 1920 || w.H != 1160 {
		t.Fatalf("geom parse wrong: %+v", w)
	}
	if w.Title != "OC | Moin! Projekt" {
		t.Fatalf("title = %q", w.Title)
	}
	if _, ok := ParseWmctrlLine("# header stuff"); ok {
		t.Fatal("header should not parse")
	}
	if _, ok := ParseWmctrlLine("too short"); ok {
		t.Fatal("short line should not parse")
	}
}

func TestUpsertAndPrune(t *testing.T) {
	snap := &Snapshot{}
	UpsertWindow(snap, Window{XID: "0xa", Emulator: "ghostty", Workspace: 1, SessionID: "s1"})
	UpsertWindow(snap, Window{XID: "0xb", Emulator: "warp", Workspace: 2})
	UpsertWindow(snap, Window{XID: "0xa", Workspace: 3, Maximized: true}) // update eintrag 0xa

	if len(snap.Windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(snap.Windows))
	}
	if snap.Windows[0].Workspace != 3 || !snap.Windows[0].Maximized {
		t.Fatalf("upsert did not replace: %+v", snap.Windows[0])
	}

	// Prune: 0xa lebt noch, 0xb Fenster weg → verschwindet; Eintrag ohne XID bleibt.
	UpsertWindow(snap, Window{Emulator: "ghostty", Kind: "shell", WorkDir: "/home/chief"})
	PruneMissing(snap, []string{"0xa"})
	if len(snap.Windows) != 2 {
		t.Fatalf("after prune want 2 (0xa + no-xid), got %d", len(snap.Windows))
	}
	for _, w := range snap.Windows {
		if w.XID != "0xa" && w.XID != "" {
			t.Fatalf("stale xid survived prune: %+v", w)
		}
	}
}

func TestJSONRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SnapshotFile)
	snap := &Snapshot{
		Version: 1,
		SavedAt: time.Now().Format(time.RFC3339),
		Windows: []Window{
			{XID: "0x1", Emulator: "ghostty", Workspace: 4, X: 10, Y: 20, W: 800, H: 600,
				Kind: "opencode", SessionID: "ses_abc", WorkDir: "/home/chief/x", TouchedAt: "2026-08-25T18:00:00+02:00"},
		},
	}
	if err := SaveSnapshot(path, snap); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("roundtrip windows = %d", len(got.Windows))
	}
	w := got.Windows[0]
	if w.XID != "0x1" || w.Kind != "opencode" || w.SessionID != "ses_abc" || w.WorkDir != "/home/chief/x" || w.X != 10 || w.H != 600 {
		t.Fatalf("roundtrip mismatch: %+v", w)
	}
}

func TestLoadSnapshotMissing(t *testing.T) {
	got, err := LoadSnapshot(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should return empty snapshot, got err %v", err)
	}
	if got == nil || len(got.Windows) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", got)
	}
}

func TestResumeCommand(t *testing.T) {
	cases := []struct {
		kind, sid, want string
	}{
		{"opencode", "ses_abc", "opencode --session ses_abc"},
		{"claude", "u-2026-xyz", "claude --resume u-2026-xyz"},
		{"shell", "", ""},
		{"", "ses_x", ""},
		{"opencode", "", "opencode"},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.kind, c.sid); got != c.want {
			t.Fatalf("ResumeCommand(%q,%q) = %q, want %q", c.kind, c.sid, got, c.want)
		}
	}
}

func TestLaunchArgs(t *testing.T) {
	tests := []struct {
		name string
		w    Window
		want []string
	}{
		{
			"opencode session -> -e",
			Window{Kind: "opencode", SessionID: "ses_abc"},
			[]string{"-e", "opencode", "--session", "ses_abc"},
		},
		{
			"shell with workdir -> +new-window",
			Window{Kind: "shell", WorkDir: "/home/chief/x"},
			[]string{"+new-window", "--working-directory=/home/chief/x"},
		},
		{
			"shell no workdir -> +new-window",
			Window{Kind: "shell"},
			[]string{"+new-window"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := launchArgs(tc.w)
			if len(got) != len(tc.want) {
				t.Fatalf("launchArgs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("launchArgs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
