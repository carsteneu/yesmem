package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/capfile"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/proxy"
	"github.com/carsteneu/yesmem/internal/repo"
	"github.com/carsteneu/yesmem/internal/storage"
)

func (h *Handler) handleExecuteCap(params map[string]any) (resp Response) {
	name := stringOr(params, "name", "")
	fn := stringOr(params, "fn", "")
	argsJSON := stringOr(params, "args", "{}")
	sessionID := stringOr(params, "_session_id", "")

	start := time.Now()
	defer func() {
		h.logCapUsage(name, fn, sessionID, start, resp, argsJSON)
	}()

	if name == "" {
		return errorResponse("name required")
	}

	meta, project, err := h.loadCapMeta(name, h.capCallerProject(params))
	if err != nil {
		return errorResponse(fmt.Sprintf("cap %q: %v", name, err))
	}

	return h.fireCapHandler(meta, project, fn, argsJSON, sessionID)
}

func (h *Handler) logCapUsage(name, fn, sessionID string, start time.Time, resp Response, argsJSON string) {
	defer func() { _ = recover() }()
	if h.store == nil {
		return
	}
	sum := sha256.Sum256([]byte(argsJSON))
	_ = h.store.LogCapUsage(storage.CapUsageEntry{
		CapName:   name,
		Fn:        fn,
		SessionID: sessionID,
		StartedAt: start,
		Duration:  time.Since(start),
		OK:        resp.Error == "",
		Error:     resp.Error,
		ArgsHash:  hex.EncodeToString(sum[:]),
		ArgsLen:   len(argsJSON),
	})
}

// capCallerProject answers which project a cap call stands in. An explicit
// project wins, then the caller's session row, and the injected cwd last. A call
// that names no project reaches user-scoped caps only (YM-359).
func (h *Handler) capCallerProject(params map[string]any) string {
	if project := stringOr(params, "project", ""); project != "" {
		return project
	}
	if sessionID := stringOr(params, "_session_id", ""); sessionID != "" {
		if session, err := h.store.GetSession(sessionID); err == nil && session != nil && session.Project != "" {
			return session.Project
		}
	}
	return stringOr(params, "_cwd", "")
}

// capProjectMatches answers whether a row stored for rowProject serves a caller
// in callerProject. The canonical basename must agree, and so must the repository
// root when both sides name one, because two repositories can share a basename and
// a worktree path has to match its main checkout.
//
// The guarantee is exact for absolute paths on both sides, which is what save_cap
// and the caller sources produce. A relative value names no repository, so the
// root comparison cannot run and the basename alone would lease the cap to every
// checkout of that name: such a row serves no absolute caller (YM-404). A caller
// that is itself relative keeps the old pairing — neither side names a repository,
// so nothing is leased that the caller could not already claim.
func capProjectMatches(rowProject, callerProject string) bool {
	if rowProject == "" || callerProject == "" {
		return false
	}
	if canonicalProjectFor(rowProject) != canonicalProjectFor(callerProject) {
		return false
	}
	if !filepath.IsAbs(callerProject) {
		return !filepath.IsAbs(rowProject)
	}
	if !filepath.IsAbs(rowProject) {
		return false
	}
	rowRoot, callerRoot := models.RepositoryRoot(rowProject), models.RepositoryRoot(callerProject)
	if rowRoot != "" && callerRoot != "" && rowRoot != callerRoot {
		return false
	}
	return true
}

// loadCapMeta returns the cap named name that project may run, and reports the
// project the supplying row is bound to. A row bound to another project is
// skipped: a cap is code the caller then runs, so one project's cap must not
// become reachable from another (YM-359). Within reach the caller's own row wins
// over a user-scoped one, so a project can shadow a global cap of the same name.
//
// The binding travels with the returned project because the sandbox lock on the
// execution path keys on it (YM-334 Befund 6, YM-353).
func (h *Handler) loadCapMeta(name, project string) (CapMeta, string, error) {
	row, meta, err := h.capRowFor(name, project)
	if err != nil {
		return CapMeta{}, "", err
	}
	return meta, row.Project, nil
}

// capRowFor is the resolution itself: the row, not only its metadata. The
// execution paths that run a cap (the scheduler's bash run, YM-417) or rewrite
// one (the bash error handler) need the row they are about, and a second lookup
// would re-ask the question and could answer it differently. A caller that only
// needs the metadata (execute_cap) goes through loadCapMeta above.
func (h *Handler) capRowFor(name, project string) (models.Learning, CapMeta, error) {
	// The catalog-shaped readers fold a caller that names a subdirectory of a
	// checkout to the checkout root (capRowsForProject). Reachability has to answer
	// with the same folding, or the catalog offers a cap that execute_cap refuses.
	project = repo.RootOrSelf(project)
	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		return models.Learning{}, CapMeta{}, fmt.Errorf("lookup cap %q: %w", name, err)
	}
	var ownRow, globalRow *models.Learning
	var ownMeta, globalMeta *CapMeta
	for i := range caps {
		l := caps[i]
		meta, err := ParseCapMeta(l.Context)
		if err != nil || meta.Name != name {
			continue
		}
		// A retired cap is not runnable, even while its row is still active: a
		// fresh binary must not keep executing a capability yesmem dropped
		// (YM-181, YM-197).
		if capfile.IsRetiredCap(meta.Name) {
			continue
		}
		switch {
		case l.Project == "":
			if globalMeta == nil {
				globalMeta = &meta
				globalRow = &caps[i]
			}
		case capProjectMatches(l.Project, project):
			if ownMeta == nil {
				ownMeta = &meta
				ownRow = &caps[i]
			}
		}
	}
	if ownRow != nil {
		return *ownRow, *ownMeta, nil
	}
	if globalRow != nil {
		return *globalRow, *globalMeta, nil
	}
	return models.Learning{}, CapMeta{}, fmt.Errorf("not found")
}

func (h *Handler) fireCapHandler(meta CapMeta, project, fn, argsJSON, sessionID string) Response {
	sc, found := h.findCapScript(meta, fn)
	if !found {
		return errorResponse(fmt.Sprintf("function %q not found in cap %q", fn, meta.Name))
	}
	handlerCode := sc.Body
	runtime := sc.Runtime

	// Timeout: explicit script timeout wins; else longer for scripts that call
	// llm()/haiku() (network-bound), else the 30s default. A bare string check for
	// "llm(" is fragile — e.g. a cap that hits an LLM via raw curl against
	// llm.ccm19.app contains "llm." but not "llm(", so it would wrongly get 30s.
	//
	// The context exists before the command is built: attaching the deadline by
	// rebuilding the command through exec.CommandContext carried only argv and the
	// environment, so every field set on the original — the working directory
	// above all — was silently dropped (YM-267).
	capTimeout := 30 * time.Second
	if sc.Timeout > 0 {
		capTimeout = time.Duration(sc.Timeout) * time.Second
	} else if strings.Contains(handlerCode, "llm(") || strings.Contains(handlerCode, "haiku(") {
		capTimeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), capTimeout)
	defer cancel()

	var bunPath string
	if runtime == "repl" || runtime == "" {
		var err error
		bunPath, err = findBun()
		if err != nil {
			return errorResponse("bun not found in PATH — cap execution requires bun")
		}
	}

	// The profile and the lock come first: a refused run must not leave the marks a
	// sandbox build leaves behind (a run dir, materialised mask placeholders), which
	// is the order the scheduler already takes (YM-353).
	profile, err := h.resolveScriptSandbox(sc.Sandbox)
	if err != nil {
		return errorResponse(fmt.Sprintf("cap %q: %v", meta.Name, err))
	}
	// The same lock the save and the job paths ask. Without it a project-bound cap
	// whose script names sandbox=none ran unsandboxed through execute_cap, while
	// save_cap refused to store it and the scheduler refused to run it (YM-353).
	if err := h.los35cRefuseUnsandboxedCapRun(meta.Name, profile, capIsProjectBound(project, meta)); err != nil {
		return errorResponse(err.Error())
	}

	// The sandbox sees the restricted job socket, never the daemon's own (YM-267):
	// a job that could reach the full RPC surface would ask for what the sandbox
	// forbids (spawn_agent, execute_cap on an unsandboxed cap, schedule).
	jobSock := JobSocketPath(h.dataDir)
	sandbox := h.newSandbox(SandboxConfig{
		Enabled:             true,
		FallbackUnsandboxed: false,
		AllowedHosts:        sc.AllowedHosts,
		// One directory per cap: a sandboxed run's cwd is mounted read-write, so a
		// shared cwd would let one cap plant a config (a .claude directory, a hook)
		// that the next run in that directory picks up (YM-286).
		WorkDir: h.capExecWorkDir(meta.Name),
		// ai-jail 2.x does not inherit the environment, so every variable the run
		// needs has to be spelled out as a forward; 0.9.x inherits it from cmd.Env.
		Env: capExecSandboxEnv(jobSock, argsJSON, sessionID),
		// On 2.x the sandbox has its own loopback: a cap that reaches the yesmem
		// proxy (a bash cap running `claude -p`) needs the same relay the bash-job
		// and headless paths already declare.
		ForwardPorts: h.agentForwardPorts(),
	})
	// A cap that runs without a sandbox leaves no trace of that fact otherwise
	// (YM-346): the bundled caps whose profile is none are the ones that touch the
	// journal, the yesmem databases and the host toolchain, so the run is worth a
	// log line.
	if profile == ProfileNone {
		// The same lock the job path carries (YM-346): a row that predates the
		// save-side lock (YM-230) can be project-bound with an inherited profile
		// none, and this path resolved the profile on its own without a save
		// (Befund 6, YM-334). A user-scoped cap may run unsandboxed.
		if err := h.los35cRefuseUnsandboxedCapRun(meta.Name, profile, capIsProjectBound(project, meta)); err != nil {
			return errorResponse(err.Error())
		}
		log.Printf("[cap-exec] cap=%s script=%s runs unsandboxed (profile none)", meta.Name, sc.Name)
	}
	// Resolve the sandbox binary before the deadline starts. A first run may
	// download ai-jail, and the cap's own budget must not pay for that (YM-267
	// review): the clock starts when the command starts.
	if profile != ProfileNone {
		sandbox.Available()
	}

	var cmd *exec.Cmd
	switch {
	case runtime == "bash":
		sockPath := JobSocketPath(h.dataDir)
		// Sandboxed bash: inject polyfills + args as shell variables before handler code.
		var sb strings.Builder
		sb.WriteString("set -e\n")
		// Polyfill: store() — routes cap_store calls via yesmem CLI to daemon socket
		sb.WriteString(fmt.Sprintf("YESMEM_SOCK=%s\nexport YESMEM_SOCK\n", sockPath))
		sb.WriteString(`store() { yesmem store "$1"; }
llm() {
  _body=$(yesmem json -n --arg model "${1:-}" --arg system "${2:-}" --arg prompt "${3:-}" --arg session "${4:-}" --arg tools "${5:-}" 'if $tools == "tools" then {model:$model,system:$system,prompt:$prompt,session:$session,tools:true} else {model:$model,system:$system,prompt:$prompt,session:$session} end')
  yesmem llm-complete "$_body"
  return $?
}
`)
		vars, err := renderCapBashVars(argsJSON)
		if err != nil {
			return errorResponse(err.Error())
		}
		sb.WriteString(vars)
		sb.WriteString(handlerCode)
		// The socket and the CLI binaries the polyfills call, registered on the
		// sandbox so the mask filter applies to them like to every other mount.
		// A dropped yesmem CLI or socket mount would break store()/llm() later
		// with a symptom that names nothing, so the run is refused here.
		yesmemCLI := lookPathOrEmpty("yesmem")
		dropped := sandbox.AddReadOnlyMounts(capExecMountPaths(sockPath, yesmemCLI, lookPathOrEmpty("curl"), lookPathOrEmpty("bash"))...)
		if err := requiredMountErr(dropped, []string{yesmemCLI, sockPath}, profile); err != nil {
			log.Printf("[cap-exec] cap=%s script=%s: %v", meta.Name, sc.Name, err)
			return errorResponse(maskRefusalForCaller(err))
		}
		binary, args, wrapErr := sandbox.WrapExecArgs("bash", []string{"-c", sb.String()}, profile)
		if wrapErr != nil {
			return errorResponse(maskRefusalForCaller(wrapErr))
		}
		cmd = exec.CommandContext(ctx, binary, args...)
		// 2.x: a cwd equal to the home would replace ai-jail's private tmpfs
		// home with a writable bind of the real one, and ai-jail reads a .ai-jail
		// from there. runWorkDir is empty on 0.9.x, which keeps that path as it was.
		cmd.Dir = sandbox.runWorkDir()
		// mergeEnv drops the inherited entry the override replaces: on Linux the
		// first match wins, so appending alone would keep a stale value.
		// The sandbox's own env carries YESMEM_SOCK, the proxy URL, the pinned XDG
		// dirs and the model list. It has to sit on the command: 2.x only forwards a
		// variable's NAME and reads the value from the process environment, and
		// 0.9.x inherits it from here.
		cmd.Env = mergeEnv(sandbox.Env())
		killProcessGroupOnCancel(cmd)

	default: // repl or empty
		sockPath := JobSocketPath(h.dataDir)
		wrapper := fmt.Sprintf(`try {
// --- Polyfills for Claude Code REPL VM ---
globalThis.sh = async function(cmd, timeoutMs) {
  var p = Bun.spawn(['sh', '-c', cmd], { stdout: 'pipe', stderr: 'pipe' });
  var out = await new Response(p.stdout).text();
  await p.exited;
  if (p.exitCode !== 0) {
    var errOut = await new Response(p.stderr).text();
    throw new Error(errOut || ('exit code ' + p.exitCode));
  }
  return out;
};
globalThis.put = async function(path, content) {
  await Bun.write(path, typeof content === 'string' ? content : JSON.stringify(content));
};
globalThis.shQuote = function(s) { return "'" + String(s).replace(/'/g, "'\"'\"'") + "'"; };
globalThis.registerTool = function(name, desc, schema, handler) { _toolHandler = handler; };
// --- LLM pass-through (routes through daemon) ---
globalThis.llm = async function(prompt, schema) {
  var body = {prompt: String(prompt || '')};
  if (schema) body.schema = schema;
  var result = await sh('yesmem llm-complete ' + shQuote(JSON.stringify(body)));
  try {
    var parsed = JSON.parse(result.trim());
    return parsed.result || '';
  } catch(e) { return result; }
};
globalThis.haiku = globalThis.llm;
// --- MCP tool polyfills (routes through daemon Unix socket) ---
var _mcpSockPath = %q;
var _mcpCall = async function(tool, body) {
  var net = await import("node:net");
  return new Promise(function(resolve, reject) {
    var sock = net.createConnection(_mcpSockPath);
    var buf = "";
    sock.on("data", function(chunk) {
      buf += chunk.toString();
      try {
        var r = JSON.parse(buf);
        sock.destroy();
        // Unwrap RPC envelope — Claude Code MCP returns result directly
        if (r.error) reject(new Error("MCP " + tool + ": " + r.error));
        else resolve(r.result !== undefined ? r.result : r);
      } catch(e) {}
    });
    sock.on("error", function(e) { reject(e); });
    if (typeof process !== 'undefined' && process.env && process.env.CAP_SESSION_ID) {
      body = Object.assign({}, body, {sender: process.env.CAP_SESSION_ID, _session_id: process.env.CAP_SESSION_ID});
    }
    sock.write(JSON.stringify({method: tool, params: body}) + "\n");
  });
};
globalThis.mcp__yesmem__cap_store = function(args) { return _mcpCall("cap_store", args); };
globalThis.mcp__yesmem__execute_cap = function(args) { return _mcpCall("execute_cap", args); };
globalThis.mcp__yesmem__broadcast = function(args) { return _mcpCall("broadcast", args); };
globalThis.mcp__yesmem__cap_blob_put = function(args) { return _mcpCall("cap_store", { action: "upsert", capability: args.capability, table: "blobs", data: { key: args.key, chunk_idx: 0, data: args.data } }); };
globalThis.mcp__yesmem__cap_blob_get = function(args) { return _mcpCall("cap_store", { action: "query", capability: args.capability, table: "blobs", where: "key=?", args: JSON.stringify([args.key]), limit: args.limit || 1000 }); };
// --- end polyfills ---

var _toolHandler = null;
var _handlerBody = %s;
if (!_toolHandler && typeof _handlerBody === 'function') { _toolHandler = _handlerBody; }
if (!_toolHandler) throw new Error("CAP body must be a function or call registerTool() to register a handler");
var _args = JSON.parse(process.env.CAP_ARGS);
var _result = _toolHandler(_args);
if (_result && typeof _result.then === 'function') {
  var _v = await _result;
  if (_v !== undefined) console.log(JSON.stringify(_v));
} else if (_result !== undefined) {
  console.log(JSON.stringify(_result));
}
} catch(e) { console.error(e.message || String(e)); process.exit(1); }
`, sockPath, handlerCode)
		// Mount daemon socket, yesmem CLI and bun into the sandbox namespace for
		// the sh()/llm()/MCP polyfills the REPL runtime needs.
		yesmemCLI := lookPathOrEmpty("yesmem")
		dropped := sandbox.AddReadOnlyMounts(capExecMountPaths(sockPath, yesmemCLI, lookPathOrEmpty("bun"), lookPathOrEmpty("curl"))...)
		if err := requiredMountErr(dropped, []string{yesmemCLI, sockPath}, profile); err != nil {
			log.Printf("[cap-exec] cap=%s script=%s: %v", meta.Name, sc.Name, err)
			return errorResponse(maskRefusalForCaller(err))
		}
		binary, args, wrapErr := sandbox.WrapExecArgs(bunPath, []string{"-e", wrapper}, profile)
		if wrapErr != nil {
			return errorResponse(maskRefusalForCaller(wrapErr))
		}
		cmd = exec.CommandContext(ctx, binary, args...)
		// 2.x: a cwd equal to the home would replace ai-jail's private tmpfs
		// home with a writable bind of the real one, and ai-jail reads a .ai-jail
		// from there. runWorkDir is empty on 0.9.x, which keeps that path as it was.
		cmd.Dir = sandbox.runWorkDir()
		// The sandbox's own env carries YESMEM_SOCK, the proxy URL, the pinned XDG
		// dirs, the model list and the cap wrapper's inputs. It has to sit on the
		// command: 2.x only forwards a variable's NAME and reads the value from the
		// process environment, and 0.9.x inherits it from here.
		cmd.Env = mergeEnv(sandbox.Env())
		killProcessGroupOnCancel(cmd)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		errDetail := string(output)
		// Check context deadline FIRST: an expired timeout surfaces as an
		// *exec.ExitError (process killed via SIGKILL), which otherwise masks
		// the real cause as "signal: killed" instead of a clear timeout message.
		if ctx.Err() != nil {
			return errorResponse(fmt.Sprintf("cap execution timed out after %v (ctx: %v)\n%s", capTimeout, ctx.Err(), errDetail))
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			if len(exitErr.Stderr) > 0 {
				errDetail = string(exitErr.Stderr)
			}
			return errorResponse(fmt.Sprintf("cap execution failed: %v\n%s", err, errDetail))
		}
		return errorResponse(fmt.Sprintf("cap execution failed: %v\n%s", err, errDetail))
	}

	out := strings.TrimSpace(string(output))
	if len(out) > 65536 {
		out = out[:65536] + fmt.Sprintf("\n... [truncated %d bytes]", len(out)-65536)
	}

	return jsonResponse(map[string]any{
		"output":   out,
		"cap_name": meta.Name,
		"fn":       fn,
		"runtime":  runtime,
	})
}

// renderCapBashVars renders the cap's JSON args as shell variable assignments
// (ARGS_<key>, <key> and <KEY>) to prepend to a bash handler.
//
// The value is the JSON-marshalled argument written into an unquoted assignment.
// The shell strips the outer quote pair, so a scalar arrives as its bare text
// ("world") — modulo the JSON escapes Go emits for control characters and for
// HTML-significant runes (`\n`, `\t`, `<`, `>`, `&`) — and objects or arrays
// additionally lose their inner quotes. That mangling predates this function. What this function adds is safety: dollar
// signs and backticks are escaped, so a value like "$(cmd)" reaches the handler
// as text instead of running cmd, and argument names that are not shell
// identifiers are rejected outright.
func renderCapBashVars(argsJSON string) (string, error) {
	var argsMap map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &argsMap); err != nil {
		return "", fmt.Errorf("invalid cap args: %w", err)
	}
	var sb strings.Builder
	for k, v := range argsMap {
		if !capArgKeyRe.MatchString(k) {
			return "", fmt.Errorf("invalid cap arg name %q: must match %s", k, capArgKeyRe)
		}
		val, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("encode cap arg %q: %w", k, err)
		}
		encoded := escapeShellExpansion(string(val))
		fmt.Fprintf(&sb, "%s=%s\n", "ARGS_"+k, encoded)
		// Also export as shell-safe names (some caps use uppercase vars)
		fmt.Fprintf(&sb, "%s=%s\n", k, encoded)
		if upper := strings.ToUpper(k); upper != k {
			fmt.Fprintf(&sb, "%s=%s\n", upper, encoded)
		}
	}
	return sb.String(), nil
}

var capArgKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// escapeShellExpansion neutralizes parameter expansion and command substitution
// in a value that is assigned without quoting.
func escapeShellExpansion(s string) string {
	return strings.NewReplacer("$", `\$`, "`", "\\`").Replace(s)
}

// capExecMountPaths returns the host paths a sandboxed cap needs read-only: the
// CLI binaries its polyfills call and the restricted job socket. Read-only on
// purpose: a cap may execute them, but must not be able to rewrite the binary or
// the socket, and a read-only mounted Unix socket still serves full
// bidirectional traffic.
//
// The paths are registered on the Sandbox (AddReadOnlyMounts) rather than rendered
// into the argv here, so the filter that drops a mount below a masked path applies
// to them like to every other mount (YM-314).
func capExecMountPaths(sockPath string, binaries ...string) []string {
	paths := make([]string, 0, len(binaries)+1)
	for _, b := range binaries {
		if b == "" {
			continue
		}
		paths = append(paths, b)
	}
	return append(paths, sockPath)
}

func lookPathOrEmpty(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// capExecWorkDir is the working directory one cap execution runs in. Keyed on the
// cap, so caps never share a writable directory (YM-286).
func (h *Handler) capExecWorkDir(capName string) string {
	dir, err := EnsureRunWorkDir(h.dataDir, "cap:"+capName)
	if err != nil {
		log.Printf("[cap-exec] no work dir for cap %q: %v", capName, err)
		return ""
	}
	return dir
}

// newSandbox builds the sandbox used for cap and job execution. It fills in the
// host paths a sandboxed bash cap needs (the yesmem CLI and the daemon socket,
// read-only) and the secret paths that must stay unreadable — the yesmem install
// state holds an API key.
func (h *Handler) newSandbox(cfg SandboxConfig) *Sandbox {
	if h.dataDir == "" {
		log.Printf("[sandbox] WARNING: no data dir configured, the data dir mask is skipped")
	}
	if cfg.ReadOnlyMounts == nil {
		// The restricted socket, not the daemon's own (YM-267): see handleJobSocket.
		cfg.ReadOnlyMounts = []string{lookPathOrEmpty("yesmem"), JobSocketPath(h.dataDir)}
	}
	// A caller that brings its own masks must not lose the defaults behind them.
	// Its own paths are materialised first: ai-jail ignores a missing --mask path,
	// so an entry that does not exist yet would protect nothing until the path
	// appeared — and a run could create it.
	callerPaths, maskErr := materialiseMaskPaths(callerMaskEntries(cfg.MaskedPaths))
	defaults, defaultsErr := maskDefaultsChecked()
	if maskErr == nil {
		maskErr = defaultsErr
	}
	cfg.MaskedPaths = appendMissingPaths(appendMissingPaths(nil, callerPaths...), defaults...)
	// Without a home the list comes back empty and no secret is masked; the run
	// itself does not fail, so this is the only trace.
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		log.Printf("[sandbox] WARNING: no home directory (%v), the sandbox can read provider keys and Claude state", homeErr)
	} else {
		// ~/.claude.json is not masked here: whether a mask or a read-only map can
		// hold depends on the run's cwd, which the Sandbox decides when it renders
		// the flags (see claudeConfigFlags). A missing file is materialised so a job
		// cannot create it either; "{}" is a valid empty config.
		globalConfig := claudeGlobalConfig(home)
		if _, err := os.Lstat(globalConfig); err != nil {
			if err := createMaskPlaceholder(maskEntry{path: globalConfig}); err != nil && maskErr == nil {
				maskErr = fmt.Errorf("mask placeholder %s: %w", globalConfig, err)
			}
		}
		if _, err := os.Stat(globalConfig); err == nil {
			cfg.ClaudeConfig = globalConfig
		}
	}
	// The CAP.md re-import channel is the directory the daemon watches, which the
	// handler carries (caps_dir, defaulting to <data dir>/../caps). Masking only
	// the home's caps dir would leave a configured directory exposed.
	capsDir := config.CapsDirPath(h.capsDir, h.dataDir)
	if capsDir != "" {
		// Same treatment as the default list: a caps dir that does not exist yet is
		// materialised, because a missing --mask path is a silent no-op and a job
		// could create the directory and plant a CAP.md in it (YM-291).
		capsPaths, capsErr := materialiseMaskPaths([]maskEntry{{path: capsDir, dir: true}})
		if capsErr != nil && maskErr == nil {
			maskErr = capsErr
		}
		cfg.MaskedPaths = appendMissingPaths(cfg.MaskedPaths, capsPaths...)
	}
	// paths.opencode_db can move the session database out of the roots the default
	// list derives (daemon.go:295). The mask follows the path the handler actually
	// reads, so a configured database is not the one channel left open while the
	// derived path is protected.
	if dbEntries := opencodeSessionDBMaskEntriesFor(h.ocDBPath); len(dbEntries) > 0 {
		dbPaths, dbErr := materialiseMaskPaths(dbEntries)
		cfg.MaskedPaths = appendMissingPaths(cfg.MaskedPaths, dbPaths...)
		if dbErr != nil && maskErr == nil {
			maskErr = dbErr
		}
	}
	// A sandboxed run that starts an agent (a bash job may run `claude -p`) loses
	// ~/.claude/settings.json to the mask above, and that file is where the proxy
	// URL normally comes from. Forward it from the daemon's environment when that
	// carries one, and derive it from the configured proxy port otherwise: a
	// daemon started without the variable left such a run without a route. The
	// loopback port itself is forwarded by the scheduler's job configs
	// (agentForwardPorts), not here; ai-jail 2.x inherits no environment and needs
	// the name spelled out, which withEnvForwards does.
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" && h.proxyForwardPort > 0 {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", h.proxyForwardPort)
	}
	if baseURL != "" {
		cfg.Env = append(cfg.Env, "ANTHROPIC_BASE_URL="+baseURL)
	}
	// XDG_CONFIG_HOME and XDG_DATA_HOME move every agent tool's config directory.
	// Both are pinned to the values the host already uses (xdgDirs): inside the
	// sandbox that is the location the masks above cover, and an inherited redirect
	// cannot point the run at a directory none of them reaches. For a run without a
	// sandbox the pin restates what the tool would compute itself.
	if configRoot, dataRoot := xdgDirs(); configRoot != "" && dataRoot != "" {
		cfg.Env = append(cfg.Env,
			"XDG_CONFIG_HOME="+configRoot,
			"XDG_DATA_HOME="+dataRoot)
	}
	// The provider/model list the bundled caps show for their /models command. They
	// used to read it out of the opencode config, which is masked now (its plugin
	// paths and mcpServers are a write channel into the next opencode start). The
	// daemon reads the file unsandboxed and hands the list to the run — from the
	// root the masks above cover, so a custom XDG_CONFIG_HOME does not hand the run
	// an empty list.
	if configRoot, _ := xdgDirs(); configRoot != "" {
		if models := proxy.OpencodeModelEntries(filepath.Join(configRoot, "opencode")); len(models) > 0 {
			cfg.Env = append(cfg.Env, "YESMEM_OPENCODE_MODELS="+strings.Join(models, "\n"))
		}
	}
	// The data dir as a whole, not single files inside it (YM-276). ai-jail 0.9.x
	// leaves the standard profile without a filesystem policy, so a job could reach
	// agent-*.sock.inject (the inject socket feeds a PTY master verbatim — key
	// injection into an unsandboxed agent), yesmem.db and config.yaml. The agent
	// sockets only come into existence while the daemon runs, so no fixed file list
	// can cover them; masking the directory replaces all of it with an empty,
	// writable-but-ephemeral dir. Everything a run needs from there (the restricted
	// socket, the job log dirs) lives in RuntimeDir beside it. In-sandbox hooks lose
	// their DB and guard-config access with it and fail open (hooks/check.go:165,
	// hooks/guard.go:170), which is how they already behave under ai-jail 2.x, whose
	// private tmpfs home hides the data dir anyway.
	cfg.MaskedPaths = appendMissingPaths(cfg.MaskedPaths, h.dataDir)
	// No default for AllowedHosts: an unset list means the caller declared no
	// egress and the standard profile runs fully offline under ai-jail 2.x.
	// WorkDir does get a default: a run must not start in the daemon's cwd (the
	// user's home) because ai-jail 2.x would then write-bind that home over its
	// own private tmpfs, and would read a .ai-jail from there.
	if cfg.WorkDir == "" {
		cfg.WorkDir = h.jobWorkDir(nil)
	}
	// A run with no usable work dir would start in the daemon's cwd, and that is the
	// user's home for a daemon started from a login shell: ai-jail 2.x write-binds it
	// over its own private home, so every map and mask the run was prepared with is
	// void (YM-437). Refused while the run is prepared, not at the start; only none is
	// exempt, because it starts the command directly and binds nothing.
	// The check therefore runs exactly once, before the start: a work dir that
	// disappears between this point and the exec is not caught again, and the run
	// falls back to the daemon's cwd (runProcess, sandbox.go). Closing that window
	// from Run() would change behaviour the real-ai-jail tests pin today (OPS-109).
	if !workDirUsable(cfg.WorkDir) {
		// Joined rather than dropped behind an older cause: a mask failure alone does
		// not refuse a strict run (profileRendersMasks), so a work-dir cause lost
		// behind it would let exactly that run start in the daemon's cwd.
		maskErr = errors.Join(maskErr, noWorkDirErr(cfg.WorkDir))
	}
	// A work dir that is the home or an ancestor of it is refused: ai-jail binds it
	// read-write after the mounts registered below, so the maps of the yesmem CLI
	// and the job socket would be void and the run could rewrite the CLI that
	// executes outside the sandbox (YM-428). Recorded as PrepareErr rather than
	// returned: none starts the command directly and binds nothing, and a work dir
	// at the home is a legitimate none run.
	if workDirCoversHome(cfg.WorkDir) {
		if maskErr == nil {
			maskErr = workDirCoversHomeErr(cfg.WorkDir)
		} else {
			// PrepareErr carries one error and the mask cause is already on record.
			// The run is refused either way; this keeps the second cause in the daemon
			// log instead of losing it behind the first.
			log.Printf("[sandbox] %v", workDirCoversHomeErr(cfg.WorkDir))
		}
	}
	// Claude Code reads .mcp.json and .claude/settings*.json from its working
	// directory upward, and ai-jail binds that directory read-write — a home mask
	// does not reach them. Only paths that exist are masked here: a placeholder
	// would plant a file in the user's repository.
	for _, path := range projectLocalClaudeConfigs(cfg.WorkDir) {
		cfg.MaskedPaths = appendMissingPaths(cfg.MaskedPaths, path)
	}
	// A mount below a mask would put that part of the path back into the sandbox,
	// so the invariant is enforced here and not only in the comment above. The data
	// dir was the only case until YM-303; with the agent-state paths masked the same
	// holds for every one of them. The filter runs after the project-local masks
	// are on the list: a caller mount below a project config is the same gap.
	requiredMounts := []string{lookPathOrEmpty("yesmem"), JobSocketPath(h.dataDir)}
	beforeMounts := cfg.ReadOnlyMounts
	cfg.ReadOnlyMounts = dropMountsInside(cfg.ReadOnlyMounts, cfg.MaskedPaths)
	cfg.RWMapPaths = dropMountsInside(cfg.RWMapPaths, cfg.MaskedPaths)
	// Befund 4 reaches this path too: a scheduled job's CLI and socket mounts are
	// seeded above, so a mask over one of them used to leave only a log line behind
	// and the job failed later inside store()/llm(). PrepareErr is the refusal a
	// mask-rendering profile acts on, and that is exactly the case a lost mount
	// breaks (strict drops every map, none renders none), so the standard gate is
	// the right one at this point.
	if lostErr := requiredMountErr(missingPaths(beforeMounts, cfg.ReadOnlyMounts), requiredMounts, ProfileStandard); lostErr != nil {
		if maskErr == nil {
			maskErr = lostErr
		} else {
			// PrepareErr carries one error and the mask cause is already on record, so
			// this one stays in the daemon log instead of being lost behind the first
			// (YM-441). Whether the refusal still holds is maskFailure's decision, not
			// this branch's: a mask cause alone does not refuse a strict run.
			// lostErr's own text is not reused: it is built around errMaskPrepare and
			// would claim a mask that could not be applied, while here a mask applied
			// fine and took a required mount with it. That reading is what an operator
			// greps for, so the line names the actual cause instead.
			log.Printf("[sandbox] a mask took a mount the run needs (the yesmem CLI or the job socket), store() and llm() would fail for this run")
		}
	}
	// Every sandboxed run sees the restricted socket at its own path, so the CLI
	// and MCP server it starts have to be pointed there (YM-267). Two halves: the
	// name is forwarded into the sandbox (ai-jail 2.x inherits no environment),
	// and the value travels in the run's own environment — so the caller sets it
	// on the command as well, which Run/RunWithProfile do with cfg.Env.
	cfg.Env = dedupeEnv(append(cfg.Env, "YESMEM_SOCK="+JobSocketPath(h.dataDir)))
	// A mask ai-jail cannot apply is a silent no-op, so the run is refused rather
	// than started with a channel the list meant to close (SandboxConfig.PrepareErr).
	// Quoted, not printed verbatim: the text carries a mask path from the caller's
	// list, and one with a line break in it would write a journal line of its own
	// (YM-448).
	if maskErr != nil {
		log.Printf("[sandbox] refusing sandboxed runs: %q", maskErr)
	}
	cfg.PrepareErr = maskErr
	return NewSandbox(cfg)
}

// callerMaskEntries turns a caller's mask paths into entries. A trailing separator
// marks a directory — materialising a directory path as a file would make the
// daemon's own MkdirAll fail later. An entry without one is treated as a file,
// which is the harmless case: ai-jail masks a file and a directory alike. A
// relative path is dropped: materialising it would write below the daemon's own
// cwd, and ai-jail resolves --mask against its own cwd, never the run's.
func callerMaskEntries(paths []string) []maskEntry {
	entries := make([]maskEntry, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			log.Printf("[sandbox] dropping mask %q: a mask needs an absolute path", path)
			continue
		}
		entries = append(entries, maskEntry{
			path: filepath.Clean(path),
			dir:  strings.HasSuffix(path, string(filepath.Separator)),
		})
	}
	return entries
}

// projectLocalClaudeConfigs are the agent configs a run can find from its work
// dir upward. Claude Code reads .mcp.json and .claude/settings*.json from its cwd
// upward, and ai-jail binds that dir read-write: the work dir's own config is
// reachable and steers the run, while directories above it are bound only where
// they belong to a tree ai-jail mounts anyway (a run that inherits the daemon's cwd,
// or a work dir inside ~/.claude). Masking the whole chain is cheap insurance for
// those cases; it is not a claim that every parent is reachable (measured on 0.9.2:
// with a plain work dir the parents are empty mount points). Existing paths only: a
// placeholder would write a file into the project the run is working in.
func projectLocalClaudeConfigs(workDir string) []string {
	if workDir == "" {
		return nil
	}
	// The run's cwd is the resolved directory, so a symlinked work dir would leave
	// the chain Claude Code really walks uncovered.
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		workDir = resolved
	}
	var found []string
	for dir := filepath.Clean(workDir); ; {
		found = append(found, existingPaths([]string{
			filepath.Join(dir, ".mcp.json"),
			filepath.Join(dir, ".claude", "settings.json"),
			filepath.Join(dir, ".claude", "settings.local.json"),
		})...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return appendMissingPaths(nil, found...)
}

// dropMountsInside removes mounts that point at or below one of dirs. A mount at
// a masked path re-exposes exactly what the mask hides, and so does one below it
// (YM-276, YM-303). Equality is part of the rule because sitsUnderMask applies it
// the same way: a mount whose path IS the mask is the re-exposure Befund 3 names,
// one level lower.
func dropMountsInside(mounts []string, dirs []string) []string {
	if len(dirs) == 0 || len(mounts) == 0 {
		return mounts
	}
	kept := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		dropped := false
		for _, dir := range dirs {
			if dir == "" {
				continue
			}
			if !samePath(mount, dir) && !pathInside(dir, mount) {
				continue
			}
			log.Printf("[sandbox] dropping mount %q: it lies inside the masked path %q", mount, dir)
			dropped = true
			break
		}
		if !dropped {
			kept = append(kept, mount)
		}
	}
	return kept
}

// pathInside reports whether path sits below dir.
func pathInside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (h *Handler) findCapScript(meta CapMeta, fn string) (ScriptMeta, bool) {
	if meta.Scripts != nil {
		for _, sc := range meta.Scripts {
			if fn != "" && sc.Name == fn {
				return sc, true
			}
		}
		if fn == "" {
			for _, sc := range meta.Scripts {
				if sc.Kind == "tool" {
					return sc, true
				}
			}
		}
	}
	// Legacy single-handler caps
	if meta.HandlerBash != "" && (fn == "" || fn == "run") {
		return ScriptMeta{Name: "run", Runtime: "bash", Body: meta.HandlerBash, Kind: "tool"}, true
	}
	if meta.HandlerREPL != "" && (fn == "" || fn == "run") {
		return ScriptMeta{Name: "run", Runtime: "repl", Body: meta.HandlerREPL, Kind: "tool"}, true
	}
	return ScriptMeta{}, false
}

func findBun() (string, error) {
	for _, path := range []string{
		filepath.Join(os.Getenv("HOME"), ".bun", "bin", "bun"),
		"/usr/local/bin/bun",
		"/usr/bin/bun",
	} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return exec.LookPath("bun")
}

// capExecSandboxEnv is what a sandboxed cap run carries itself. ai-jail 2.x
// forwards a variable to the run only when the sandbox names it, so the wrapper's
// inputs have to sit in the sandbox environment and not only on cmd.Env: the tool
// wrapper reads its arguments from CAP_ARGS and the caller from CAP_SESSION_ID
// (YM-206).
func capExecSandboxEnv(jobSock, argsJSON, sessionID string) []string {
	env := []string{"YESMEM_SOCK=" + jobSock, "CAP_ARGS=" + argsJSON}
	if sessionID != "" {
		env = append(env, "CAP_SESSION_ID="+sessionID)
	}
	return env
}
