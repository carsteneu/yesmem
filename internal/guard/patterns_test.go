package guard

import "testing"

func TestMatchDestructivePattern_RmRfRoot(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: rm -rf /", true},
		{"Bash: rm -rf /tmp/foo", false},
		{"Bash: rm -rf ~", true},
		{"Bash: rm -rf ~/Documents/old", false},
		{"Bash: rm -rf $HOME", true},
		{"Bash: rm -rf $HOME/projects/x", false},
		{"Bash: rm -fr /", true},
		{"Bash: rm -Rf /", true},
		{`REPL: await Bash({command:"rm -rf /"})`, true},
		{"Bash: ls /", false},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v (matched=%q)", c.desc, got, c.want, matchDestructivePattern(c.desc))
		}
	}
}

func TestMatchDestructivePattern_GitForcePush(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: git push --force origin main", true},
		{"Bash: git push -f origin main", true},
		{"Bash: git push --force-with-lease origin main", true},
		{"Bash: git push --force origin master", true},
		{"Bash: git push origin feature/foo", false},
		{"Bash: git push --force origin feature/foo", false},
		{"Bash: git push", false},
		{"Bash: git push --force origin production", true},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v (matched=%q)", c.desc, got, c.want, matchDestructivePattern(c.desc))
		}
	}
}

func TestMatchDestructivePattern_GitResetHard(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: git reset --hard origin/main", true},
		{"Bash: git reset --hard main", true},
		{"Bash: git reset --hard HEAD~1", false},
		{"Bash: git reset --soft main", false},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}

func TestMatchDestructivePattern_DropTable(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: sqlite3 db.sqlite 'DROP TABLE users'", true},
		{"Bash: psql -c 'DROP DATABASE prod'", true},
		{"Bash: psql -c 'DROP SCHEMA legacy'", true},
		{"Bash: SELECT * FROM users", false},
		{"Bash: psql -c 'DELETE FROM users WHERE id=1'", false},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}

func TestMatchDestructivePattern_ForkBomb(t *testing.T) {
	if matchDestructivePattern("Bash: :(){ :|:& };:") == "" {
		t.Error("fork bomb should match")
	}
	if matchDestructivePattern("Bash: echo :(){ :|:& };:") == "" {
		t.Error("fork bomb embedded should still match")
	}
}

func TestMatchDestructivePattern_DiskDevices(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: dd if=/dev/zero of=/dev/sda", true},
		{"Bash: dd if=/dev/urandom of=/dev/nvme0n1", true},
		{"Bash: dd if=/dev/zero of=/tmp/bigfile", false},
		{"Bash: mkfs.ext4 /dev/sdb1", true},
		{"Bash: mkfs.ext4 /tmp/img.raw", false},
		{"Bash: cat /dev/urandom > /dev/sdb", true},
		{"Bash: echo foo > /tmp/x", false},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v (matched=%q)", c.desc, got, c.want, matchDestructivePattern(c.desc))
		}
	}
}

func TestMatchDestructivePattern_ChmodChownRoot(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{"Bash: chmod -R 777 /", true},
		{"Bash: chmod 777 /tmp/foo", false},
		{"Bash: chown -R nobody /", true},
		{"Bash: chown -R alice /home/alice", false},
	}
	for _, c := range cases {
		got := matchDestructivePattern(c.desc) != ""
		if got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}

func TestMatchDestructivePattern_BenignNoMatch(t *testing.T) {
	cases := []string{
		"Bash: ls",
		"Bash: git status",
		"Bash: git push origin feature/branch",
		"Bash: make test",
		"Edit: foo.go",
		"Write: foo.go",
		`REPL: await Bash({command:"ls"})`,
	}
	for _, c := range cases {
		if m := matchDestructivePattern(c); m != "" {
			t.Errorf("benign %q should not match, but matched %q", c, m)
		}
	}
}
