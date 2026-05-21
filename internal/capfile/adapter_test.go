package capfile

import (
	"strings"
	"testing"
)

func TestDefaultAdapters_DirectMappings(t *testing.T) {
	cfg := DefaultAdapters()

	if cfg.Direct["store"] != "mcp__yesmem__cap_store" {
		t.Errorf("Direct[store] = %q, want mcp__yesmem__cap_store", cfg.Direct["store"])
	}
	if _, ok := cfg.Direct["web"]; ok {
		t.Error("web should not be in Direct — it is a dispatcher")
	}
	if _, ok := cfg.Direct["file"]; ok {
		t.Error("file should not be in Direct — it is a dispatcher")
	}
	if _, ok := cfg.Direct["cap_store"]; ok {
		t.Error("old name cap_store should not exist")
	}
	if _, ok := cfg.Direct["blob_put"]; ok {
		t.Error("old name blob_put should not exist")
	}
}

func TestDefaultAdapters_WebDispatcher(t *testing.T) {
	cfg := DefaultAdapters()

	web, ok := cfg.Dispatchers["web"]
	if !ok {
		t.Fatal("missing web dispatcher")
	}
	if _, ok := web["fetch"]; !ok {
		t.Error("web dispatcher missing fetch action")
	}
	if _, ok := web["search"]; !ok {
		t.Error("web dispatcher missing search action")
	}
}

func TestDefaultAdapters_FileDispatcher(t *testing.T) {
	cfg := DefaultAdapters()

	file, ok := cfg.Dispatchers["file"]
	if !ok {
		t.Fatal("missing file dispatcher")
	}
	for _, action := range []string{"read", "write", "glob"} {
		if _, ok := file[action]; !ok {
			t.Errorf("file dispatcher missing %s action", action)
		}
	}
}

func TestProviderToGeneric_Store(t *testing.T) {
	cfg := DefaultAdapters()

	got := ProviderToGeneric("await mcp__yesmem__cap_store({action: 'query'})", cfg)
	want := "await store({action: 'query'})"
	if got != want {
		t.Errorf("ProviderToGeneric:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestGenericToProvider_Store(t *testing.T) {
	cfg := DefaultAdapters()

	got := GenericToProvider("await store({action: 'query'})", cfg)
	want := "await mcp__yesmem__cap_store({action: 'query'})"
	if got != want {
		t.Errorf("GenericToProvider:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestNameMappingRoundTrip(t *testing.T) {
	cfg := DefaultAdapters()
	original := "const r = await store({action:'upsert', table:'t', data:{id:1}})"

	provider := GenericToProvider(original, cfg)
	if !strings.Contains(provider, "mcp__yesmem__cap_store(") {
		t.Error("GenericToProvider should replace store with provider name")
	}

	back := ProviderToGeneric(provider, cfg)
	if back != original {
		t.Errorf("roundtrip failed:\noriginal: %s\nback:     %s", original, back)
	}
}

func TestProviderToGeneric_NoMatch(t *testing.T) {
	cfg := DefaultAdapters()
	input := "const x = doSomething()"
	got := ProviderToGeneric(input, cfg)
	if got != input {
		t.Errorf("should not modify unrelated code:\ngot:  %s\nwant: %s", got, input)
	}
}

func TestGenericToProvider_MultipleOccurrences(t *testing.T) {
	cfg := DefaultAdapters()
	input := "store({action:'a'}); store({action:'b'})"
	got := GenericToProvider(input, cfg)
	count := strings.Count(got, "mcp__yesmem__cap_store(")
	if count != 2 {
		t.Errorf("should replace all occurrences: found %d, want 2", count)
	}
}

func TestGenerateAdapterJS_DirectShim(t *testing.T) {
	cfg := DefaultAdapters()
	js := GenerateAdapterJS(cfg, false)

	if !strings.Contains(js, "globalThis.store=") {
		t.Error("should generate store shim")
	}
	if !strings.Contains(js, "mcp__yesmem__cap_store") {
		t.Error("store shim should reference provider tool")
	}
}

func TestGenerateAdapterJS_SkipStore(t *testing.T) {
	cfg := DefaultAdapters()
	js := GenerateAdapterJS(cfg, true)

	if strings.Contains(js, "globalThis.store=") {
		t.Error("skipStore=true should NOT generate store shim, got: ", js)
	}
	if !strings.Contains(js, "globalThis.web=") {
		t.Error("skipStore=true should still generate web dispatcher")
	}
}

func TestGenerateAdapterJS_WebDispatcher(t *testing.T) {
	cfg := DefaultAdapters()
	js := GenerateAdapterJS(cfg, false)

	if !strings.Contains(js, "globalThis.web=") {
		t.Error("should generate web dispatcher")
	}
	if !strings.Contains(js, "fetch") || !strings.Contains(js, "search") {
		t.Error("web dispatcher should include fetch and search actions")
	}
}

func TestGenerateAdapterJS_FileDispatcher(t *testing.T) {
	cfg := DefaultAdapters()
	js := GenerateAdapterJS(cfg, false)

	if !strings.Contains(js, "globalThis.file=") {
		t.Error("should generate file dispatcher")
	}
	for _, action := range []string{"read", "write", "glob"} {
		if !strings.Contains(js, action) {
			t.Errorf("file dispatcher should include %s action", action)
		}
	}
}

func TestUsesStoreAdapter(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   bool
	}{
		{"generic store", "async () => store({action:'query'})", true},
		{"provider store", "async () => mcp__yesmem__cap_store({action:'query'})", true},
		{"web only", "async () => web({action:'fetch'})", false},
		{"file only", "async () => file({action:'read'})", false},
		{"no adapter", "async () => sh('echo hello')", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UsesStoreAdapter(tt.script)
			if got != tt.want {
				t.Errorf("UsesStoreAdapter(%q) = %v, want %v", tt.script, got, tt.want)
			}
		})
	}
}

func TestGenerateAdapterBash_StoreFunction(t *testing.T) {
	bash := GenerateAdapterBash()

	if !strings.Contains(bash, "store()") {
		t.Error("should define store() shell function")
	}
}

func TestGenerateAdapterBash_StartsCoprocWorker(t *testing.T) {
	bash := GenerateAdapterBash()

	if !strings.Contains(bash, "coproc YESMEM_WORKER") {
		t.Error("preamble should start a coproc named YESMEM_WORKER")
	}
	if !strings.Contains(bash, "yesmem worker") {
		t.Error("coproc body should invoke `yesmem worker`")
	}
}

func TestGenerateAdapterBash_StoreUsesCoproc(t *testing.T) {
	bash := GenerateAdapterBash()

	if !strings.Contains(bash, "YESMEM_WORKER_IN") {
		t.Error("store() should reference YESMEM_WORKER_IN file descriptor")
	}
	if !strings.Contains(bash, "YESMEM_WORKER_OUT") {
		t.Error("store() should reference YESMEM_WORKER_OUT file descriptor")
	}
	if !strings.Contains(bash, `"op":"store"`) {
		t.Error("store() should send op=store payload to worker")
	}
}

func TestGenerateAdapterBash_LlmFunction(t *testing.T) {
	bash := GenerateAdapterBash()

	if !strings.Contains(bash, "llm()") {
		t.Error("should define llm() shell function")
	}
	if !strings.Contains(bash, "yesmem llm-complete") {
		t.Error("llm() should call yesmem llm-complete")
	}
}

func TestGenerateAdapterBash_YesmemFunction(t *testing.T) {
	bash := GenerateAdapterBash()

	if !strings.Contains(bash, "yesmem()") {
		t.Error("should define yesmem() shell function to intercept json subcommand")
	}
	if !strings.Contains(bash, `"op":"json_cli"`) {
		t.Error("yesmem() should route json subcommand through worker via op=json_cli")
	}
	if !strings.Contains(bash, "command yesmem") {
		t.Error("yesmem() should fall through to real binary via `command yesmem` for non-json subcommands")
	}
	if !strings.Contains(bash, "stdin_len") {
		t.Error("yesmem() should send stdin_len for length-framed stdin payload")
	}
}

func TestGenerateAdapterBash_EndsWithNewline(t *testing.T) {
	bash := GenerateAdapterBash()
	if !strings.HasSuffix(bash, "\n") {
		t.Error("preamble should end with newline for clean concatenation")
	}
}

func TestGenerateAdapterBash_ValidShell(t *testing.T) {
	bash := GenerateAdapterBash()
	if strings.Contains(bash, "globalThis") {
		t.Error("bash preamble must not contain JS constructs")
	}
}

func TestUsesGenericAdapters(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   bool
	}{
		{"store call", "async ({q}) => store({action:'query', q})", true},
		{"web call", "async () => web({action:'fetch', url:'http://x'})", true},
		{"file call", "async () => file({action:'read', path:'/tmp/x'})", true},
		{"no adapter", "async () => sh('echo hello')", false},
		{"bash handler", "echo hello", false},
		{"provider name", "mcp__yesmem__cap_store({action:'query'})", false},
		{"empty", "", false},
		{"store in string", `async () => log("store()")`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UsesGenericAdapters(tt.script)
			if got != tt.want {
				t.Errorf("UsesGenericAdapters(%q) = %v, want %v", tt.script, got, tt.want)
			}
		})
	}
}

// GenericToProvider must be a stable round-trip: applying it twice yields the
// same result as applying it once. Before the word-boundary fix, the second
// call prepended another mcp__yesmem__cap_ layer because strings.ReplaceAll
// matched the substring "store(" inside the already-prefixed name. That bug
// produced the 6-layer corruption observed in cap.db on disk.
func TestGenericToProvider_Idempotent(t *testing.T) {
	cfg := DefaultAdapters()
	once := GenericToProvider("async ({q}) => store({action:'query', q})", cfg)
	twice := GenericToProvider(once, cfg)
	if once != twice {
		t.Errorf("GenericToProvider not idempotent\n once:  %s\n twice: %s", once, twice)
	}
	if !strings.Contains(once, "mcp__yesmem__cap_store(") {
		t.Errorf("first call did not prefix store call: %s", once)
	}
	if strings.Contains(twice, "mcp__yesmem__cap_mcp__yesmem__cap_") {
		t.Errorf("second call double-prefixed: %s", twice)
	}
}

// On an already-prefixed body GenericToProvider must be a no-op. This is the
// import-roundtrip case: the daemon's CapsDirWatcher re-renders CAP.md every
// 30s; before this fix that re-render added another mcp__yesmem__cap_ layer.
func TestGenericToProvider_AlreadyPrefixedNoOp(t *testing.T) {
	cfg := DefaultAdapters()
	input := "async ({q}) => mcp__yesmem__cap_store({action:'query', q})"
	if got := GenericToProvider(input, cfg); got != input {
		t.Errorf("GenericToProvider must not modify already-prefixed input\n got:  %s\n want: %s", got, input)
	}
}

// ProviderToGeneric on an already-generic body must be a no-op so it can be
// safely called on input of unknown provenance.
func TestProviderToGeneric_Idempotent(t *testing.T) {
	cfg := DefaultAdapters()
	input := "async ({q}) => store({action:'query', q})"
	if got := ProviderToGeneric(input, cfg); got != input {
		t.Errorf("ProviderToGeneric must not modify generic-only input\n got:  %s\n want: %s", got, input)
	}
}

// Identifier characters around the callsite must not match. "tableStore(" or
// "_store(" or "myStore(" share the suffix store( but are distinct identifiers
// and must be left alone.
func TestWrapToolWithStore_InjectsCapability(t *testing.T) {
	body := "async (args) => { await store({action:'query', table:'t'}); }"
	got := WrapToolWithStore(body, "mycap")

	if !strings.Contains(got, "capability") {
		t.Fatal("should inject capability into store wrapper, got:", got)
	}
	if !strings.Contains(got, `"mycap"`) {
		t.Errorf("should use capability name 'mycap', got: %s", got)
	}
	if !strings.Contains(got, body) {
		t.Errorf("should preserve original body, got: %s", got)
	}
}

func TestWrapToolWithStore_StringifiesArrayArgs(t *testing.T) {
	body := "async (args) => { await store({args: [1, 2]}); }"
	got := WrapToolWithStore(body, "mycap")

	if !strings.Contains(got, "Array.isArray") {
		t.Fatal("should include Array.isArray check, got:", got)
	}
	if !strings.Contains(got, "JSON.stringify") {
		t.Fatal("should include JSON.stringify, got:", got)
	}
}

func TestWrapToolWithStore_PassesThroughStringArgs(t *testing.T) {
	got := WrapToolWithStore("async () => store({args:'x'})", "mycap")

	if !strings.Contains(got, "Array.isArray") {
		t.Fatal("wrapper should handle both array and string args, got:", got)
	}
}

func TestWrapToolWithStore_AllowsExplicitCapabilityOverride(t *testing.T) {
	got := WrapToolWithStore("async () => store({capability:'bar', action:'q'})", "mycap")

	spreadIdx := strings.Index(got, "...a")
	if spreadIdx < 0 {
		t.Fatal("wrapper must use spread to allow override, got:", got)
	}
	if !strings.Contains(got, "capability") {
		t.Fatal("should include capability in wrapper, got:", got)
	}
}

func TestGenericToProvider_DoesNotTouchSimilarIdentifiers(t *testing.T) {
	cfg := DefaultAdapters()
	cases := []string{
		"const result = tableStore({key:'x'})",
		"_store(arg)",
		"myStore(arg)",
		"obj.store_(arg)",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := GenericToProvider(in, cfg); got != in {
				t.Errorf("must not touch non-callsite identifier\n got:  %s\n want: %s", got, in)
			}
		})
	}
}
