package hooks

import "testing"

func TestParseNavCommand_Grep(t *testing.T) {
	tool, files, ok := ParseNavCommand(`grep -n "func Run" internal/proxy/proxy.go`)
	if !ok {
		t.Fatal("should detect grep")
	}
	if tool != "grep" {
		t.Errorf("tool = %q, want grep", tool)
	}
	if len(files) != 1 || files[0] != "internal/proxy/proxy.go" {
		t.Errorf("files = %v, want [internal/proxy/proxy.go]", files)
	}
}

func TestParseNavCommand_GrepRecursive(t *testing.T) {
	tool, files, ok := ParseNavCommand(`grep -rn "pattern" internal/proxy/`)
	if !ok {
		t.Fatal("should detect recursive grep")
	}
	if tool != "grep" {
		t.Errorf("tool = %q, want grep", tool)
	}
	if len(files) != 1 || files[0] != "internal/proxy/" {
		t.Errorf("files = %v, want [internal/proxy/]", files)
	}
}

func TestParseNavCommand_Rg(t *testing.T) {
	_, _, ok := ParseNavCommand(`rg "func.*Check" internal/hooks/`)
	if !ok {
		t.Fatal("should detect rg")
	}
}

func TestParseNavCommand_SedRange(t *testing.T) {
	tool, files, ok := ParseNavCommand(`sed -n "100,150p" internal/proxy/proxy.go`)
	if !ok {
		t.Fatal("should detect sed range")
	}
	if tool != "sed" {
		t.Errorf("tool = %q, want sed", tool)
	}
	if len(files) != 1 || files[0] != "internal/proxy/proxy.go" {
		t.Errorf("files = %v, want [internal/proxy/proxy.go]", files)
	}
}

func TestParseNavCommand_Cat(t *testing.T) {
	tool, files, ok := ParseNavCommand(`cat internal/hooks/check.go`)
	if !ok {
		t.Fatal("should detect cat")
	}
	if tool != "cat" {
		t.Errorf("tool = %q, want cat", tool)
	}
	if len(files) != 1 || files[0] != "internal/hooks/check.go" {
		t.Errorf("files = %v", files)
	}
}

func TestParseNavCommand_HeadWithN(t *testing.T) {
	_, files, ok := ParseNavCommand(`head -50 main.go`)
	if !ok {
		t.Fatal("should detect head")
	}
	if len(files) != 1 || files[0] != "main.go" {
		t.Errorf("files = %v, want [main.go]", files)
	}
}

func TestParseNavCommand_ExcludedPaths(t *testing.T) {
	excluded := []string{
		`grep "ERROR" /var/log/syslog`,
		`cat /tmp/test.txt`,
		`grep "foo" /etc/hosts`,
		`cat /proc/cpuinfo`,
	}
	for _, cmd := range excluded {
		_, _, ok := ParseNavCommand(cmd)
		if ok {
			t.Errorf("should exclude: %s", cmd)
		}
	}
}

func TestParseNavCommand_ExcludedExtensions(t *testing.T) {
	excluded := []string{
		`cat server.log`,
		`grep "error" output.txt`,
	}
	for _, cmd := range excluded {
		_, _, ok := ParseNavCommand(cmd)
		if ok {
			t.Errorf("should exclude: %s", cmd)
		}
	}
}

func TestParseNavCommand_NonNavCommands(t *testing.T) {
	nonNav := []string{
		`ls -la internal/proxy/`,
		`find . -name "*.go"`,
		`wc -l main.go`,
		`echo "hello"`,
		`go test ./internal/proxy/ -count=1`,
		`make deploy`,
		`git status`,
	}
	for _, cmd := range nonNav {
		_, _, ok := ParseNavCommand(cmd)
		if ok {
			t.Errorf("should not match non-nav command: %s", cmd)
		}
	}
}

func TestParseNavCommand_PipedGrep(t *testing.T) {
	_, _, ok := ParseNavCommand(`grep -n "func" proxy.go | head -5`)
	if !ok {
		t.Fatal("should detect grep even with pipe")
	}
}

func TestSuggestYesmemTool_Grep(t *testing.T) {
	s := SuggestYesmemTool("grep", "internal/proxy/proxy.go", "yesmem")
	if s == "" {
		t.Fatal("should return suggestion for grep")
	}
	if !contains(s, "search_code") {
		t.Errorf("grep suggestion should mention search_code, got: %s", s)
	}
	if !contains(s, "proxy.go") {
		t.Errorf("grep suggestion should include filename, got: %s", s)
	}
	if !contains(s, "yesmem") {
		t.Errorf("grep suggestion should include project, got: %s", s)
	}
}

func TestSuggestYesmemTool_Sed(t *testing.T) {
	s := SuggestYesmemTool("sed", "internal/proxy/proxy.go", "yesmem")
	if !contains(s, "get_code_snippet") {
		t.Errorf("sed suggestion should mention get_code_snippet, got: %s", s)
	}
	if !contains(s, "proxy.go") {
		t.Errorf("sed suggestion should include filename, got: %s", s)
	}
}

func TestSuggestYesmemTool_Cat(t *testing.T) {
	s := SuggestYesmemTool("cat", "internal/proxy/proxy.go", "yesmem")
	if !contains(s, "get_file_symbols") {
		t.Errorf("cat suggestion should mention get_file_symbols, got: %s", s)
	}
	if !contains(s, "proxy.go") {
		t.Errorf("cat suggestion should include filename, got: %s", s)
	}
}

func TestCheckCodeNav_IndexedFile(t *testing.T) {
	reason, block := CheckCodeNav(
		`grep -n "func Run" internal/proxy/proxy.go`,
		"/home/user/project",
		"myproject",
		"session-123",
		func(project, path string) bool { return true },
		false,
	)
	if !block {
		t.Fatal("should block when file is indexed")
	}
	if reason == "" {
		t.Error("should return reason")
	}
}

func TestCheckCodeNav_NonIndexedFile(t *testing.T) {
	_, block := CheckCodeNav(
		`grep -n "func Run" internal/proxy/proxy.go`,
		"/home/user/project",
		"myproject",
		"session-123",
		func(project, path string) bool { return false },
		false,
	)
	if block {
		t.Error("should not block when file is not indexed")
	}
}

func TestCheckCodeNav_Dismissed(t *testing.T) {
	_, block := CheckCodeNav(
		`grep -n "func Run" internal/proxy/proxy.go`,
		"/home/user/project",
		"myproject",
		"session-123",
		func(project, path string) bool { return true },
		true,
	)
	if block {
		t.Error("should not block when dismissed")
	}
}

func TestCheckCodeNav_NonNavCommand(t *testing.T) {
	_, block := CheckCodeNav(
		`go test ./internal/proxy/`,
		"/home/user/project",
		"myproject",
		"session-123",
		func(project, path string) bool { return true },
		false,
	)
	if block {
		t.Error("should not block non-nav commands")
	}
}

func TestParseREPLNavCommands_ShGrep(t *testing.T) {
	cmds := ParseREPLNavCommands(`o.x = await sh("grep -n 'func Run' internal/proxy/proxy.go")`)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0] != `grep -n 'func Run' internal/proxy/proxy.go` {
		t.Errorf("got %q", cmds[0])
	}
}

func TestParseREPLNavCommands_RgShorthand(t *testing.T) {
	cmds := ParseREPLNavCommands(`o.result = await rg("pattern", "internal/proxy/")`)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0] != `rg "pattern" internal/proxy/` {
		t.Errorf("got %q", cmds[0])
	}
}

func TestParseREPLNavCommands_CatShorthand(t *testing.T) {
	cmds := ParseREPLNavCommands(`o.file = await cat("internal/hooks/check.go", 1, 50)`)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0] != `cat internal/hooks/check.go` {
		t.Errorf("got %q", cmds[0])
	}
}

func TestParseREPLNavCommands_Multiple(t *testing.T) {
	code := `o.a = await rg("func", "internal/proxy/")
o.b = await cat("main.go", 1, 100)
o.c = await sh("go test ./...")`
	cmds := ParseREPLNavCommands(code)
	if len(cmds) != 2 {
		t.Fatalf("expected 2 nav commands (rg + cat, not go test), got %d: %v", len(cmds), cmds)
	}
}

func TestParseREPLNavCommands_NoNav(t *testing.T) {
	cmds := ParseREPLNavCommands(`o.deploy = await sh("make deploy 2>&1 | tail -5", 60000)`)
	if len(cmds) != 0 {
		t.Errorf("expected 0 nav commands, got %d: %v", len(cmds), cmds)
	}
}

func TestParseREPLNavCommands_ShRg(t *testing.T) {
	cmds := ParseREPLNavCommands(`o.x = await sh("cd /project && rg 'pattern' internal/proxy/")`)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
