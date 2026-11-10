package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinOpencodeMajor replaces the version probe. Setup must never depend on the
// opencode installed on the host, so every test pins the major it exercises.
// It pins the built-in-plugin probe to "absent" as well: every test that
// reaches the install path goes through this helper, so none of them reads the
// host's opencode build.
func pinOpencodeMajor(t *testing.T, major int) {
	t.Helper()
	prev := opencodeVersionFn
	opencodeVersionFn = func() int { return major }
	t.Cleanup(func() { opencodeVersionFn = prev })
	pinOpencodeBuiltin(t, false)
}

// pinOpencodeBuiltin replaces the built-in-plugin probe.
func pinOpencodeBuiltin(t *testing.T, builtin bool) {
	t.Helper()
	pinOpencodeBuiltinProbe(t, "/pinned/opencode", builtin)
}

// pinOpencodeBuiltinProbe pins the probe's answer and the binary it names.
func pinOpencodeBuiltinProbe(t *testing.T, bin string, builtin bool) {
	t.Helper()
	prev := opencodeBuiltinPluginFn
	opencodeBuiltinPluginFn = func() (string, bool) { return bin, builtin }
	t.Cleanup(func() { opencodeBuiltinPluginFn = prev })
}

// useRealOpencodeBuiltin undoes the built-in pin for the tests that exercise
// the probe itself. It has to run after pinOpencodeMajor.
func useRealOpencodeBuiltin(t *testing.T) {
	t.Helper()
	prev := opencodeBuiltinPluginFn
	opencodeBuiltinPluginFn = detectBuiltinOpencodePlugin
	t.Cleanup(func() { opencodeBuiltinPluginFn = prev })
}

// v1InstallDir is a literal expectation on purpose: the tests must not derive
// what they assert from v1PluginPersistentRelPath, the code under test.
func v1InstallDir(home string) string {
	return filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem")
}

func v2InstallDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "plugins", "opencode-yesmem")
}

func TestParseOpencodeMajorVersion(t *testing.T) {
	tests := []struct {
		out  string
		want int
	}{
		{"2.0.29-patched.1\n", 2},
		{"2.0.29\n", 2},
		{"1.16.0\n", 1},
		{"0.6.3\n", 0},
		{"2", 2},
		{"", opencodeVersionUnknown},
		{"garbage\n", opencodeVersionUnknown},
		{"v2.0.0\n", 2},
		{"-1.0.0\n", opencodeVersionUnknown},
	}
	for _, tc := range tests {
		if got := parseOpencodeMajorVersion(tc.out); got != tc.want {
			t.Errorf("parseOpencodeMajorVersion(%q) = %d, want %d", tc.out, got, tc.want)
		}
	}
}

// D17: opencode v1 is no longer supported. Setup reports the requirement and
// writes nothing, instead of installing the removed v1 plugin. A real 0.x host
// is pre-v2 like a 1.x host, so it gets the same message; only an unreadable
// version probe is treated as "unknown".
func TestInstallOpencodePlugin_BelowV2GetsMessageNotAnInstall(t *testing.T) {
	for _, major := range []int{0, 1} {
		t.Run(fmt.Sprintf("opencode %d.x", major), func(t *testing.T) {
			pinOpencodeMajor(t, major)
			home := filepath.Join(t.TempDir(), "home")

			_, _, err := InstallOpencodePlugin(home)
			if err == nil {
				t.Fatal("expected a message, got nil")
			}
			want := fmt.Sprintf("opencode %d.x is no longer supported", major)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message does not name the unsupported version: %v", err)
			}
			for _, p := range []string{
				v1InstallDir(home),
				v2InstallDir(home),
				filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts"),
			} {
				if _, statErr := os.Lstat(p); !os.IsNotExist(statErr) {
					t.Errorf("pre-v2 host got %s written (lstat err=%v)", p, statErr)
				}
			}
			if _, statErr := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.jsonc")); statErr == nil {
				if data := readFileOrFail(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc")); strings.Contains(string(data), "yesmem") {
					t.Errorf("plugin entry written for a pre-v2 host: %s", data)
				}
			}
		})
	}
}

// An undetectable version is not a v1 host: the probe failed, the host may be
// anything. v2 is the only plugin left, so it is installed; the v1 artifacts
// still go away, since opencode v2 loads .ts files next to plugin directories.
func TestInstallOpencodePlugin_UnknownMajorInstallsV2AndCleansV1(t *testing.T) {
	pinOpencodeMajor(t, opencodeVersionUnknown)
	home := filepath.Join(t.TempDir(), "home")
	v1Index := writeV1Artifacts(t, home)

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v2InstallDir(home), "index.ts")); err != nil {
		t.Errorf("v2 plugin not installed for an unknown major: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v1InstallDir(home), "index.ts")); !os.IsNotExist(err) {
		t.Errorf("v1 sources survived (stat err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts")); !os.IsNotExist(err) {
		t.Errorf("v1 symlink survived (lstat err=%v)", err)
	}
	if data := readFileOrFail(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc")); strings.Contains(string(data), v1Index) {
		t.Errorf("v1 plugin entry survived in the config: %s", data)
	}
}

func TestInstallOpencodePlugin_UsesV2FromMajorTwo(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	// A host that ran the v1 install before the upgrade.
	if err := os.MkdirAll(v1InstallDir(home), 0755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	dir := v2InstallDir(home)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("v2 plugin dir not installed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("v2 plugin dir is empty")
	}
	for _, e := range entries {
		name := e.Name()
		// The manifest is generated rather than embedded: opencode reads the plugin's name and
		// version from it.
		if name != "package.json" && !strings.HasSuffix(name, ".ts") {
			t.Errorf("unexpected file installed into the v2 plugin dir: %s", name)
		}
		if !isOpencodePluginRuntimeFile(name) {
			t.Errorf("test scaffolding installed into the v2 plugin dir: %s", name)
		}
	}
	// The entrypoint opencode v2 resolves for a plugin directory.
	for _, name := range []string{"index.ts", "rpc.ts", "rule_guard.ts", "code_nav.ts", "session.ts", "shell.ts", "package.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	// A v1 source tree next to a v2 install is dead weight; nothing loads it.
	if _, err := os.Stat(v1InstallDir(home)); !os.IsNotExist(err) {
		t.Errorf("v1 sources survived the v2 install (stat err=%v)", err)
	}
}

func TestInstallOpencodePlugin_V2RemovesStaleFiles(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"messages_test.ts", "old_hook.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("// old"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	for _, name := range []string{"messages_test.ts", "old_hook.ts"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("stale file %s survived the v2 install (stat err=%v)", name, err)
		}
	}
}

// opencode v2 also discovers .ts files in <config>/plugins, so a leftover v1
// symlink would load the v1 hook API into the v2 runtime. The v1 config entry
// is dropped, other plugin entries stay.
func TestInstallOpencodePlugin_V2RemovesV1Artifacts(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")

	v1Index := filepath.Join(v1InstallDir(home), "index.ts")
	if err := os.MkdirAll(filepath.Dir(v1Index), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v1Index, []byte("// v1 plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	pluginsDir := filepath.Join(home, ".config", "opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(pluginsDir, "yesmem.ts")
	if err := os.Symlink(v1Index, symlinkPath); err != nil {
		t.Fatal(err)
	}
	// A dev-tree entry is only ours while it sits next to the running binary
	// (isDevTreeEntry). This one does not, so the v2 install has to leave it
	// alone: it may belong to another project in the same home.
	devIndex := filepath.Join(home, "projects", "otherapp", "plugins", "opencode-yesmem", "index.ts")
	writeOpencodeJSONC(t, home, `{
	  "$schema": "https://opencode.ai/config.json",
	  "plugin": ["`+v1Index+`", "`+devIndex+`", "superpowers@git+https://github.com/obra/superpowers.git"]
	}`)

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}

	if _, err := os.Lstat(symlinkPath); !os.IsNotExist(err) {
		t.Errorf("v1 symlink yesmem.ts survived the v2 install (lstat err=%v)", err)
	}
	data := readFileOrFail(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"))
	if strings.Contains(string(data), v1Index) {
		t.Errorf("v1 plugin entry survived in the config: %s", data)
	}
	if !strings.Contains(string(data), devIndex) {
		t.Errorf("a foreign dev-tree entry was dropped: %s", data)
	}
	if !strings.Contains(string(data), "superpowers@git+https://github.com/obra/superpowers.git") {
		t.Errorf("foreign plugin entry was dropped: %s", data)
	}
}

// A v2 install never rewrites a config it cannot parse. The plugin files are
// installed and the stale entry is reported as an error instead of being
// clobbered away with the rest of the file.
func TestInstallOpencodePlugin_V2ToleratesUnparseableConfig(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	writeOpencodeJSONC(t, home, "{ // comments\n \"$schema\": \"https://opencode.ai/config.json\"\n}")
	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	before := readFileOrFail(t, cfgPath)

	_, _, err := InstallOpencodePlugin(home)
	if err == nil {
		t.Fatal("expected an error naming the unparseable config, got nil")
	}
	if !strings.Contains(err.Error(), "opencode.jsonc") {
		t.Errorf("error does not name the config: %v", err)
	}

	if after := readFileOrFail(t, cfgPath); string(before) != string(after) {
		t.Errorf("config rewritten despite the parse failure: %s", after)
	}
	if _, err := os.Stat(filepath.Join(v2InstallDir(home), "index.ts")); err != nil {
		t.Errorf("v2 plugin not installed: %v", err)
	}
}

// A config yesmem cannot parse keeps the v1 sources: deleting them would leave
// the surviving entry pointing at a path that no longer exists. The symlink is
// a separate artifact and still goes.
func TestInstallOpencodePlugin_KeepsV1SourcesWhenConfigIsUnparseable(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	v1Index := writeV1Artifacts(t, home)
	writeOpencodeJSONC(t, home, "{ /* hand-written */\n \"$schema\": \"https://opencode.ai/config.json\"\n}")

	_, _, err := InstallOpencodePlugin(home)
	if err == nil {
		t.Fatal("expected an error for the unparseable config, got nil")
	}

	if _, statErr := os.Stat(v1Index); statErr != nil {
		t.Errorf("v1 sources deleted while the config still names them: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts")); !os.IsNotExist(statErr) {
		t.Errorf("v1 symlink survived (lstat err=%v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(v2InstallDir(home), "index.ts")); statErr != nil {
		t.Errorf("v2 plugin not installed: %v", statErr)
	}
}

// The v2 plugin lives in opencode's plugin directory, next to plugins the user
// added. Uninstall removes yesmem's entries and leaves the rest alone; the old
// RemoveAll on the whole directory would delete every foreign plugin.
func TestRemoveOpencodePlugin_KeepsForeignPlugins(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	pluginsDir := filepath.Join(home, ".config", "opencode", "plugins")
	for _, dir := range []string{"opencode-yesmem", "yesmem-window", "my-plugin"} {
		if err := os.MkdirAll(filepath.Join(pluginsDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginsDir, dir, "index.ts"), []byte("// plugin"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "yesmem.ts"), []byte("// symlink stand-in"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := removeOpencodePlugin(home); err != nil {
		t.Fatalf("removeOpencodePlugin: %v", err)
	}

	for _, gone := range []string{"opencode-yesmem", "yesmem-window", "yesmem.ts"} {
		if _, err := os.Lstat(filepath.Join(pluginsDir, gone)); !os.IsNotExist(err) {
			t.Errorf("yesmem entry %s survived uninstall (lstat err=%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "my-plugin", "index.ts")); err != nil {
		t.Errorf("foreign plugin was deleted by uninstall: %v", err)
	}
}

// The v1 source tree is yesmem's own and goes away, wherever the version
// dispatch last pointed.
func TestRemoveOpencodePlugin_RemovesV1SourceTree(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(v1InstallDir(home), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(v2InstallDir(home), 0755); err != nil {
		t.Fatal(err)
	}

	if err := removeOpencodePlugin(home); err != nil {
		t.Fatalf("removeOpencodePlugin: %v", err)
	}
	for _, dir := range []string{v1InstallDir(home), v2InstallDir(home)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s survived uninstall (stat err=%v)", dir, err)
		}
	}
}

// `yesmem status` must see a v2 install. The v2 plugin is a discovered
// directory, not a "plugin" entry, so the config scan alone reports "missing"
// for a working installation.
func TestReadOpencodeConfigState_V2DirectoryCountsAsConfigured(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	writeOpencodeJSONC(t, home, `{"$schema": "https://opencode.ai/config.json"}`)
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("// v2"), 0644); err != nil {
		t.Fatal(err)
	}

	if state := readOpencodeConfigState(home); !state.PluginConfigured {
		t.Error("v2 plugin directory not reported as configured")
	}
}

func TestReadOpencodeConfigState_ForeignPluginDirIsNotYesmem(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	writeOpencodeJSONC(t, home, `{"$schema": "https://opencode.ai/config.json"}`)
	dir := filepath.Join(home, ".config", "opencode", "plugins", "my-plugin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("// other"), 0644); err != nil {
		t.Fatal(err)
	}

	if state := readOpencodeConfigState(home); state.PluginConfigured {
		t.Error("foreign plugin directory reported as yesmem")
	}
}

// The embedded bundle is a glob over plugins/opencode-yesmem-v2, so a module added to the repo is picked
// up automatically. This guards the other direction: what the repo carries is what gets installed.
//
// The expected set is written out on purpose. Deriving it from isOpencodePluginRuntimeFile would make the
// test agree with the installer by construction, and an over-broad filter would then silently stop
// shipping a module with nothing turning red.
func TestInstallOpencodePlugin_V2InstallsEveryRepoModule(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}

	expected := []string{
		"auto_resolve.ts",
		"code_nav.ts",
		"context.ts",
		"failure_learn.ts",
		"idle_reminder.ts",
		"index.ts",
		"ledger_nudge.ts",
		"log.ts",
		"messages.ts",
		"rpc.ts",
		"rule_guard.ts",
		"session.ts",
		"shell.ts",
		"skill_nudge.ts",
		"skill_whitelist.ts",
		"types.ts",
		"package.json",
	}
	dstDir := v2InstallDir(home)
	for _, name := range expected {
		if _, err := os.Stat(filepath.Join(dstDir, name)); err != nil {
			t.Errorf("%s exists in the repo but was not installed: %v", name, err)
		}
	}
	// And nothing else: a module the repo carries must not be filtered out, and the plugin directory must
	// not collect files the suite needs (the tests and their shared harness).
	installed, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("read installed dir: %v", err)
	}
	if len(installed) != len(expected) {
		names := make([]string, 0, len(installed))
		for _, e := range installed {
			names = append(names, e.Name())
		}
		t.Errorf("installed %d files, expected %d: %v", len(installed), len(expected), names)
	}
}

// The v2 branch merges the settings without the v1 config entry path; the
// wizard's model choice must land there too.
func TestInstallOpencodePlugin_V2PreservesChosenModel(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")

	if _, err := installOpencodePlugin(home, "/test/yesmem", "opencode/big-pickle", "opencode/big-pickle"); err != nil {
		t.Fatalf("installOpencodePlugin: %v", err)
	}

	cfgPath := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	var cfg map[string]any
	if err := json.Unmarshal(readFileOrFail(t, cfgPath), &cfg); err != nil {
		t.Fatalf("merged config is not valid JSON: %v", err)
	}
	if cfg["model"] != "opencode/big-pickle" {
		t.Errorf("chosen model not written: %v", cfg["model"])
	}
	if cfg["mcp"] == nil {
		t.Error("mcp block missing")
	}
	// v2 drops configured file targets, so no plugin entry may be written.
	if entry, ok := cfg["plugin"]; ok {
		t.Errorf("v1 plugin entry written for opencode 2.x: %v", entry)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts")); !os.IsNotExist(err) {
		t.Errorf("v1 symlink created for opencode 2.x (stat err=%v)", err)
	}
}

// writeV1Artifacts plants what an old yesmem setup left on a v1 host: the
// source tree, the yesmem.ts symlink and the config entry. Returns the v1
// index path the entry points at.
func writeV1Artifacts(t *testing.T, home string) string {
	t.Helper()
	dir := v1InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.ts": "// v1 plugin", "RULES.md": "# old rules"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	v1Index := filepath.Join(dir, "index.ts")
	pluginsDir := filepath.Join(home, ".config", "opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v1Index, filepath.Join(pluginsDir, "yesmem.ts")); err != nil {
		t.Fatal(err)
	}
	writeOpencodeJSONC(t, home, `{
	  "$schema": "https://opencode.ai/config.json",
	  "plugin": ["`+v1Index+`", "superpowers@git+https://github.com/obra/superpowers.git"]
	}`)
	return v1Index
}

// Setup removes the v1 install on a v1 host too, not only after the upgrade to
// v2. The user's own RULES.md is generated by setup elsewhere and stays.
func TestInstallOpencodePlugin_RemovesV1ArtifactsOnV1Host(t *testing.T) {
	pinOpencodeMajor(t, 1)
	home := filepath.Join(t.TempDir(), "home")
	v1Index := writeV1Artifacts(t, home)
	userRules := filepath.Join(home, "memory", "yesmem", "RULES.md")
	if err := os.MkdirAll(filepath.Dir(userRules), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userRules, []byte("# my rules"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InstallOpencodePlugin(home); err == nil {
		t.Fatal("expected the unsupported-version message for opencode 1.x")
	}

	if _, err := os.Stat(v1InstallDir(home)); !os.IsNotExist(err) {
		t.Errorf("v1 source tree survived (stat err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts")); !os.IsNotExist(err) {
		t.Errorf("v1 symlink survived (lstat err=%v)", err)
	}
	cfg := string(readFileOrFail(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc")))
	if strings.Contains(cfg, v1Index) {
		t.Errorf("v1 plugin entry survived: %s", cfg)
	}
	if !strings.Contains(cfg, "superpowers@") {
		t.Errorf("foreign plugin entry was dropped: %s", cfg)
	}
	if data, err := os.ReadFile(userRules); err != nil || string(data) != "# my rules" {
		t.Errorf("user RULES.md changed (data=%q err=%v)", data, err)
	}
}

// The prune is scoped to the file kinds the bundle ships: a file the user put
// next to the sources is not ours to delete.
func TestInstallOpencodePlugin_KeepsForeignFilesNextToTheSources(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// index.ts reads ${pluginDirectory}/RULES.md as a catalog candidate.
	for name, body := range map[string]string{"RULES.md": "# my rules", "notes.txt": "keep me"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	for name, body := range map[string]string{"RULES.md": "# my rules", "notes.txt": "keep me"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s was deleted: %v", name, err)
			continue
		}
		if string(data) != body {
			t.Errorf("%s was rewritten: %q", name, data)
		}
	}
}

// Writing through a symlinked plugin directory would land the bundle, and the
// prune, outside the plugin directory.
func TestInstallOpencodePlugin_RefusesSymlinkedPluginDir(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(victim, []byte("important"), 0644); err != nil {
		t.Fatal(err)
	}
	pluginsDir := filepath.Join(home, ".config", "opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, v2InstallDir(home)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InstallOpencodePlugin(home); err == nil {
		t.Error("expected an error for a symlinked plugin dir, got nil")
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "important" {
		t.Errorf("prune followed the symlink and touched %s (data=%q err=%v)", victim, data, err)
	}
}

// A symlink under a bundle name must be replaced, not written through.
func TestInstallOpencodePlugin_ReplacesSymlinkedTargetFile(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "victim.ts")
	if err := os.WriteFile(outside, []byte("// outside"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "index.ts")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "// outside" {
		t.Errorf("install wrote through the symlink (data=%q err=%v)", data, err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatalf("index.ts missing: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("index.ts is still a symlink (mode=%v)", fi.Mode())
	}
}

func TestIsTrustedExecutable(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "opencode")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	worldWritable := filepath.Join(dir, "ww")
	if err := os.WriteFile(worldWritable, []byte("#!/bin/sh\n"), 0777); err != nil {
		t.Fatal(err)
	}
	// WriteFile masks the mode with the umask: on a 022 runner this lands as 0755
	// and the world-writable case would test nothing.
	if err := os.Chmod(worldWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "plain")
	if err := os.WriteFile(notExec, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Package managers link the launcher; rejecting a symlink would send the
	// common install shape down the v1 path.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	badLink := filepath.Join(dir, "bad-link")
	if err := os.Symlink(worldWritable, badLink); err != nil {
		t.Fatal(err)
	}
	brokenLink := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "nope"), brokenLink); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"regular 0755", good, true},
		{"world writable", worldWritable, false},
		{"not executable", notExec, false},
		{"symlink to good", link, true},
		{"symlink to world writable", badLink, false},
		{"broken symlink", brokenLink, false},
		{"missing", filepath.Join(dir, "nope"), false},
		{"empty", "", false},
	} {
		if got := isTrustedExecutable(tc.path); got != tc.want {
			t.Errorf("%s: isTrustedExecutable(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// status must recognise a working v2 install even when the config is JSONC with
// comments, the case YM-87 is about.
func TestReadOpencodeConfigState_V2WinsOverUnparseableConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	writeOpencodeJSONC(t, home, "{ // notes\n \"$schema\": \"https://opencode.ai/config.json\"\n}")
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("// v2"), 0644); err != nil {
		t.Fatal(err)
	}

	if state := readOpencodeConfigState(home); !state.PluginConfigured {
		t.Error("v2 install not reported for an unparseable config")
	}
}

// Only our own path is dropped from the config: an identically named path in
// another home belongs to someone else.
func TestDropYesmemPluginEntry_MatchesOwnPathOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	own := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem", "index.ts")
	foreign := "/home/other/.local/share/yesmem/plugins/opencode-yesmem/index.ts"
	// A directory with our plugin's name outside our home is someone else's
	// project, not our install (YM-371).
	foreignProject := "/srv/apps/plugins/opencode-yesmem/index.ts"
	cfg := map[string]any{"plugin": []any{own, foreign, foreignProject}}

	if !dropYesmemPluginEntry(cfg, home) {
		t.Fatal("own entry not dropped")
	}
	kept, _ := cfg["plugin"].([]any)
	if len(kept) != 2 || kept[0] != foreign || kept[1] != foreignProject {
		t.Errorf("wrong entry removed: %v", kept)
	}
}

// isV1PluginEntry decides ownership for status, uninstall and the v1 cleanup
// during install, so every shape it accepts or rejects is pinned here.
func TestIsV1PluginEntry(t *testing.T) {
	home := "/home/dev"
	tests := []struct {
		name  string
		entry string
		want  bool
	}{
		{"own persistent", filepath.Join(v1InstallDir(home), "index.ts"), true},
		// The dev shape next to a binary needs the executable anchor, so the bare
		// path is not ours (see TestLos18iDevTreeEntryIsAnchoredAtTheRunningBinary).
		{"dev fallback as written", "/home/dev/bin/../plugins/opencode-yesmem/index.ts", false},
		{"dev fallback cleaned", "/home/dev/plugins/opencode-yesmem/index.ts", false},
		{"system-wide install", "/opt/local/share/yesmem/plugins/opencode-yesmem/index.ts", true},
		{"system-wide install under usr", "/usr/local/share/yesmem/plugins/opencode-yesmem/index.ts", true},
		{"another home's install", "/home/other/.local/share/yesmem/plugins/opencode-yesmem/index.ts", false},
		// A directory that merely carries the tail is not ours. Claiming it
		// deleted another project's registration on uninstall and reported it
		// as our install in status (YM-371).
		{"another project's plugin directory", "/srv/apps/plugins/opencode-yesmem/index.ts", false},
		{"another project inside our home", "/home/dev/projects/otherapp/plugins/opencode-yesmem/index.ts", false},
		{"another home's dev fallback", "/home/other/plugins/opencode-yesmem/index.ts", false},
		{"nested beside the binaries of another project", "/home/dev2/plugins/opencode-yesmem/index.ts", false},
		// A share/yesmem tail under a foreign prefix is a copy, not a
		// system-wide install of ours (YM-371 cold review).
		{"another user's share tree", "/home/other/share/yesmem/plugins/opencode-yesmem/index.ts", false},
		{"a project's share tree", "/srv/apps/share/yesmem/plugins/opencode-yesmem/index.ts", false},
		{"a vendor tree under opt", "/opt/vendor-x/share/yesmem/plugins/opencode-yesmem/index.ts", false},
		// A path nested inside a persistent install belongs to that install, not
		// to the tail shape it happens to end with.
		{"nested under a persistent install", "/home/dev/.local/share/yesmem/plugins/opencode-yesmem/vendor/plugins/opencode-yesmem/index.ts", false},
		{"user's own plugin", "/home/other/projects/yesmem-tools/plugins/index.ts", false},
		{"relative entry", "plugins/opencode-yesmem/index.ts", false},
		{"relative with parent", "../plugins/opencode-yesmem/index.ts", false},
		// A config entry pointing at the v2 install dir is stale too: opencode
		// discovers that directory, and uninstall deletes it.
		{"v2 install dir entry", "/home/dev/.config/opencode/plugins/opencode-yesmem/index.ts", true},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := isV1PluginEntry(tc.entry, home); got != tc.want {
			t.Errorf("%s: isV1PluginEntry(%q) = %v, want %v", tc.name, tc.entry, got, tc.want)
		}
	}
}

// The dev fallback is anchored at the binary that wrote it: the v1 installer
// used <bin>/../plugins/opencode-yesmem/index.ts, so an entry next to the running
// binary is provably ours while an identically shaped path next to a different
// binary is not.
func TestLos18iDevTreeEntryIsAnchoredAtTheRunningBinary(t *testing.T) {
	const home = "/home/dev"
	const executable = "/srv/checkout/yesmem"
	entry := "/srv/plugins/opencode-yesmem/index.ts"

	if !isV1PluginEntryFrom(entry, home, executable) {
		t.Error("dev tree entry next to the running binary was not claimed")
	}
	if isV1PluginEntryFrom(entry, home, "/opt/build/yesmem") {
		t.Error("dev tree entry was claimed for a different binary")
	}
	if isV1PluginEntryFrom(entry, home, "") {
		t.Error("dev tree entry was claimed without an executable")
	}
	if isV1PluginEntryFrom("/srv/plugins/opencode-yesmem/other.ts", home, executable) {
		t.Error("a different file next to the binary was claimed")
	}
}

// The config write must not leave a temporary file behind.
func TestWriteOpencodeConfig_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	if err := writeOpencodeConfig(path, map[string]any{"model": "a/b"}); err != nil {
		t.Fatalf("writeOpencodeConfig: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "opencode.jsonc" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("unexpected files in config dir: %v", names)
	}
	var cfg map[string]any
	if err := json.Unmarshal(readFileOrFail(t, path), &cfg); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
	if cfg["model"] != "a/b" {
		t.Errorf("content lost: %v", cfg)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0644 {
		t.Errorf("config mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestWriteOpencodeConfig_KeepsARestrictedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeConfig(path, map[string]any{"model": "a/b"}); err != nil {
		t.Fatalf("writeOpencodeConfig: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("config mode widened to %v, want 0600", fi.Mode().Perm())
	}
}

// The version probe is a heuristic (a launcher the trust check rejects, a
// timeout, unparseable output). A v1 answer must not delete a v2 install that
// a previous run put there.
func TestInstallOpencodePlugin_V1HostLeavesAV2InstallAlone(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("v2 install: %v", err)
	}
	indexPath := filepath.Join(v2InstallDir(home), "index.ts")
	before := readFileOrFail(t, indexPath)

	pinOpencodeMajor(t, 1)
	if _, _, err := InstallOpencodePlugin(home); err == nil {
		t.Fatal("expected the unsupported-version message for opencode 1.x")
	}

	if after := readFileOrFail(t, indexPath); string(before) != string(after) {
		t.Errorf("v2 plugin file changed on a v1 answer")
	}
}

// A hardlink under a bundle name shares its inode with a file outside the
// plugin dir; writing through it would clobber that file.
func TestInstallOpencodePlugin_DoesNotWriteThroughAHardlink(t *testing.T) {
	pinOpencodeMajor(t, 2)
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "victim.ts")
	if err := os.WriteFile(outside, []byte("// outside"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dir, "index.ts")); err != nil {
		t.Skipf("hardlinks not supported here: %v", err)
	}

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "// outside" {
		t.Errorf("install wrote through the hardlink (data=%q err=%v)", data, err)
	}
}

// A dotfile-managed config is a symlink; replacing it with a regular file
// leaves the user's repository stale without a trace.
func TestWriteOpencodeConfig_KeepsASymlinkedConfigALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-opencode.jsonc")
	if err := os.WriteFile(target, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "opencode.jsonc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := writeOpencodeConfig(link, map[string]any{"model": "a/b"}); err != nil {
		t.Fatalf("writeOpencodeConfig: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("config symlink was replaced by a regular file")
	}
	if !strings.Contains(string(readFileOrFail(t, target)), `"model": "a/b"`) {
		t.Error("symlink target was not updated")
	}
}

// The built-in probe searches the opencode binary for the plugin's own
// literals. A marker that straddles a read boundary must still be found; a
// per-chunk search without overlap would miss exactly that case.
func TestFileContainsMarkers_AcrossChunkBoundary(t *testing.T) {
	markers := [][]byte{[]byte("opencode.yesmem"), []byte("register_pid")}
	dir := t.TempDir()

	for _, at := range []int{
		markerChunkSize - 3,
		markerChunkSize,
		2*markerChunkSize - 1,
		3 * markerChunkSize,
	} {
		path := filepath.Join(dir, fmt.Sprintf("at-%d", at))
		head := bytes.Repeat([]byte("x"), at)
		body := append(append([]byte{}, head...), markers[0]...)
		body = append(body, bytes.Repeat([]byte("z"), 128)...)
		body = append(body, markers[1]...)
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
		if !fileContainsMarkersOrFail(t, path, markers) {
			t.Errorf("markers with the first at offset %d were not found", at)
		}
	}

	inside := filepath.Join(dir, "inside")
	if err := os.WriteFile(inside, append(append([]byte("prefix-"), markers[0]...), markers[1]...), 0644); err != nil {
		t.Fatal(err)
	}
	if !fileContainsMarkersOrFail(t, inside, markers) {
		t.Error("markers inside the first chunk were not found")
	}

	absent := filepath.Join(dir, "absent")
	if err := os.WriteFile(absent, bytes.Repeat([]byte("y"), 3*markerChunkSize), 0644); err != nil {
		t.Fatal(err)
	}
	if fileContainsMarkersOrFail(t, absent, markers) {
		t.Errorf("markers reported in %s, which contains none", absent)
	}

	// One literal alone is not enough: a launcher that merely mentions the
	// plugin id must not pass for a build with the plugin compiled in.
	onlyID := filepath.Join(dir, "only-id")
	if err := os.WriteFile(onlyID, append([]byte("#!/bin/sh\n"), markers[0]...), 0644); err != nil {
		t.Fatal(err)
	}
	if fileContainsMarkersOrFail(t, onlyID, markers) {
		t.Error("a single literal was accepted as the built-in plugin")
	}
}

// An expired scan budget reports "not built in", which installs the external
// copy: setup must never hang on a pathological file, and the safe direction is
// the install.
func TestFileContainsMarkers_ExpiredBudgetIsTheSafeDirection(t *testing.T) {
	markers := opencodeBuiltinPluginMarkers
	path := filepath.Join(t.TempDir(), "late-marker")
	body := append(bytes.Repeat([]byte("y"), 2*markerChunkSize), markers[0]...)
	body = append(body, markers[1]...)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}

	prev := markerScanBudget
	markerScanBudget = 0
	t.Cleanup(func() { markerScanBudget = prev })

	if fileContainsMarkersOrFail(t, path, markers) {
		t.Error("expired budget still reported the markers")
	}

	markerScanBudget = prev
	if !fileContainsMarkersOrFail(t, path, markers) {
		t.Error("markers after the budget was restored were not found")
	}
}

func fileContainsMarkersOrFail(t *testing.T, path string, markers [][]byte) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	found, err := fileContainsMarkers(f, markers)
	if err != nil {
		t.Fatalf("fileContainsMarkers(%s): %v", path, err)
	}
	return found
}

// YM-241: an opencode build that carries the yesmem plugin drops a second copy
// with the same id and reports "Duplicate plugin ID". Where the plugin is
// built in, there is nothing to deliver, so setup writes no external copy.
// The v1 artifacts are dead on every path and still go.
func TestInstallOpencodePlugin_BuiltinPluginSkipsTheExternalCopy(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltin(t, true)
	home := filepath.Join(t.TempDir(), "home")
	v1Index := writeV1Artifacts(t, home)

	if _, detail, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	} else if !strings.Contains(detail, "built into") {
		t.Errorf("skip is not reported to the user, detail=%q", detail)
	}

	if _, err := os.Stat(v2InstallDir(home)); !os.IsNotExist(err) {
		t.Errorf("external plugin copy written although the build carries the plugin (stat err=%v)", err)
	}
	if _, err := os.Stat(v1Index); !os.IsNotExist(err) {
		t.Errorf("v1 sources survived the skip (stat err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "plugins", "yesmem.ts")); !os.IsNotExist(err) {
		t.Errorf("v1 symlink survived the skip (lstat err=%v)", err)
	}
}

// The skip removes the guard plugin when it is wrong, so the message has to
// name the binary the probe examined. One home holds one plugin directory, but
// the probe answers for a single binary: a host can start a different opencode
// build than the one found first on PATH, and only the path in the message lets
// the user see which build the decision was taken for.
func TestInstallOpencodePlugin_BuiltinPluginNamesTheProbedBinary(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltinProbe(t, "/opt/opencode/bin/opencode", true)
	home := filepath.Join(t.TempDir(), "home")

	_, detail, err := InstallOpencodePlugin(home)
	if err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if !strings.Contains(detail, "/opt/opencode/bin/opencode") {
		t.Errorf("the message does not name the probed binary, detail=%q", detail)
	}
}

// The path in that message comes from PATH or HOME, so it must not be able to
// forge a second line of setup output.
func TestInstallOpencodePlugin_ProbedBinaryCannotForgeALine(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltinProbe(t, "/opt/open\ncode/bin/opencode", true)
	home := filepath.Join(t.TempDir(), "home")

	_, detail, err := InstallOpencodePlugin(home)
	if err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if strings.ContainsAny(detail, "\n\r") {
		t.Errorf("the message carries a line break, detail=%q", detail)
	}
}

// Every class a terminal or a line-based log could read as a break, not just
// the plain newline: C0, DEL, C1 and the Unicode line separators.
func TestSetupLogText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/opt/opencode/bin/opencode", "/opt/opencode/bin/opencode"},
		{"/opt/open\ncode", "/opt/open?code"},
		{"/opt/open\r\tcode", "/opt/open??code"},
		{"/opt/open\x1bcode", "/opt/open?code"},
		{"/opt/open\x7fcode", "/opt/open?code"},
		{"/opt/open\u009bcode", "/opt/open?code"},
		{"/opt/open\u2028code", "/opt/open?code"},
		{"/opt/open\u2029code", "/opt/open?code"},
		{"/opt/open\u202ecode", "/opt/open?code"},
		{"/opt/open\u200bcode", "/opt/open?code"},
	}
	for _, tc := range tests {
		if got := setupLogText(tc.in); got != tc.want {
			t.Errorf("setupLogText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The status line reads the same path, so it carries the same rule.
func TestOpencodePluginStatusText_SanitizesTheProbedBinary(t *testing.T) {
	probe := func() (string, bool) { return "/pinned\n/opencode", true }
	if got := opencodePluginStatusText(false, probe); strings.ContainsAny(got, "\n\r") {
		t.Errorf("the status line carries a line break, got %q", got)
	}
}

// The home path reaches the same message through the redundant-copy branch.
func TestInstallOpencodePlugin_RedundantCopyCannotForgeALine(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltin(t, true)
	home := filepath.Join(t.TempDir(), "ho\nme")
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("// older copy"), 0644); err != nil {
		t.Fatal(err)
	}

	_, detail, err := InstallOpencodePlugin(home)
	if err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if strings.ContainsAny(detail, "\n\r") {
		t.Errorf("the message carries a line break, detail=%q", detail)
	}
}

// A host that ran an older yesmem has the external copy already. Setup does not
// delete it, because it may be newer than the plugin compiled into the binary,
// but it must name it: otherwise the duplicate-id entry stays unexplained.
func TestInstallOpencodePlugin_BuiltinPluginNamesTheRedundantCopy(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltin(t, true)
	home := filepath.Join(t.TempDir(), "home")
	dir := v2InstallDir(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "index.ts")
	if err := os.WriteFile(stale, []byte("// older copy"), 0644); err != nil {
		t.Fatal(err)
	}

	_, detail, err := InstallOpencodePlugin(home)
	if err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if !strings.Contains(detail, dir) {
		t.Errorf("the existing copy is not named, detail=%q", detail)
	}
	if data, err := os.ReadFile(stale); err != nil || string(data) != "// older copy" {
		t.Errorf("the existing copy was touched (data=%q err=%v)", data, err)
	}
}

// The opposite direction in one place: without the built-in the very same host
// gets the external copy, so the test above cannot pass by skipping the
// install for everyone.
func TestInstallOpencodePlugin_WithoutBuiltinStillInstalls(t *testing.T) {
	pinOpencodeMajor(t, 2)
	pinOpencodeBuiltin(t, false)
	home := filepath.Join(t.TempDir(), "home")

	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v2InstallDir(home), "index.ts")); err != nil {
		t.Errorf("external plugin copy missing on a build without the plugin: %v", err)
	}
}

// After the skip there is no installed index.ts for the status line to find, so
// the built-in has to count as the plugin being there. The probe reads the
// opencode binary, so it must not run when the config already answers.
func TestOpencodePluginStatusText(t *testing.T) {
	tests := []struct {
		configured bool
		builtin    bool
		want       string
	}{
		{false, false, "missing"},
		{true, false, "configured"},
		{false, true, "built into /pinned/opencode (probed binary, not necessarily the running build)"},
		{true, true, "configured"},
	}
	for _, tc := range tests {
		probes := 0
		probe := func() (string, bool) {
			probes++
			return "/pinned/opencode", tc.builtin
		}
		if got := opencodePluginStatusText(tc.configured, probe); got != tc.want {
			t.Errorf("opencodePluginStatusText(configured=%v, builtin=%v) = %q, want %q",
				tc.configured, tc.builtin, got, tc.want)
		}
		if want := map[bool]int{true: 0, false: 1}[tc.configured]; probes != want {
			t.Errorf("configured=%v ran the probe %d times, want %d", tc.configured, probes, want)
		}
	}
}

// End to end without the seam: the probe resolves the binary the way the
// install path does (PATH first, then ~/.opencode/bin/opencode) and the install
// decision follows it. The fake binary is a shell script carrying the plugin id
// in a comment, which is all the probe looks for.
func TestInstallOpencodePlugin_RealProbeFindsTheBuiltinAndSkips(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".opencode", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n// Plugin.define id opencode.yesmem\n// rpc.call register_pid\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	pinOpencodeMajor(t, 2)
	// The major pin also pins the built-in probe to "absent"; this test asks the
	// probe itself.
	useRealOpencodeBuiltin(t)

	_, _, err := InstallOpencodePlugin(home)
	if err != nil {
		t.Fatalf("InstallOpencodePlugin: %v", err)
	}
	if _, err := os.Stat(v2InstallDir(home)); !os.IsNotExist(err) {
		t.Errorf("external copy written although the probed binary carries the plugin (stat err=%v)", err)
	}

	// The same host without the marker in the binary gets the copy.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n// upstream build\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallOpencodePlugin(home); err != nil {
		t.Fatalf("InstallOpencodePlugin (without builtin): %v", err)
	}
	if _, err := os.Stat(filepath.Join(v2InstallDir(home), "index.ts")); err != nil {
		t.Errorf("external copy missing on a build without the plugin: %v", err)
	}
}

// A config that cannot be parsed keeps its plugin entry, so the sources it
// points at must survive: deleting both leaves a dangling entry and no
// install to re-register.
func TestRemoveOpencodePlugin_KeepsV1SourcesWhenConfigIsUnparseable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	srcDir := v1InstallDir(home)
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "index.ts"), []byte("// plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	writeOpencodeJSONC(t, home, "{ // hand-written, not JSON\n}\n")

	err := removeOpencodePlugin(home)
	if err == nil {
		t.Fatal("expected the unparseable config to be reported")
	}
	if _, statErr := os.Stat(filepath.Join(srcDir, "index.ts")); statErr != nil {
		t.Errorf("v1 plugin sources were deleted although the config still points at them: %v", statErr)
	}
}

// Following a config symlink is the documented behaviour for a dotfile-managed
// config. A link that cannot be resolved is not that case: writing through the
// path name would replace the link with a regular file and drop the user's
// dotfiles wiring without a trace.
func TestWriteOpencodeConfig_RefusesADanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "opencode.jsonc")
	if err := os.Symlink(filepath.Join(dir, "not-checked-in-yet.jsonc"), link); err != nil {
		t.Fatal(err)
	}

	if err := writeOpencodeConfig(link, map[string]any{"model": "a/b"}); err == nil {
		t.Fatal("expected an error for an unresolvable config symlink")
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("config symlink was replaced by a regular file")
	}
}
