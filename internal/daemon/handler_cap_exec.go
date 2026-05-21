package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func (h *Handler) handleExecuteCap(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("name required")
	}
	fn := stringOr(params, "fn", "")
	argsJSON := stringOr(params, "args", "{}")
	sessionID := stringOr(params, "_session_id", "")

	meta, err := h.loadCapMeta(name)
	if err != nil {
		return errorResponse(fmt.Sprintf("cap %q: %v", name, err))
	}

	return h.fireCapHandler(meta, fn, argsJSON, sessionID)
}

func (h *Handler) loadCapMeta(name string) (CapMeta, error) {
	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		return CapMeta{}, fmt.Errorf("lookup cap %q: %w", name, err)
	}
	for _, l := range caps {
		meta, err := ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if meta.Name == name {
			return meta, nil
		}
	}
	return CapMeta{}, fmt.Errorf("not found")
}

func (h *Handler) fireCapHandler(meta CapMeta, fn, argsJSON, sessionID string) Response {
	sc, found := h.findCapScript(meta, fn)
	if !found {
		return errorResponse(fmt.Sprintf("function %q not found in cap %q", fn, meta.Name))
	}
	handlerCode := sc.Body
	runtime := sc.Runtime

	var bunPath string
	if runtime == "repl" || runtime == "" {
		var err error
		bunPath, err = findBun()
		if err != nil {
			return errorResponse("bun not found in PATH — cap execution requires bun")
		}
	}

	sandbox := NewSandbox(SandboxConfig{Enabled: true, FallbackUnsandboxed: false})
	profile, _ := ParseSandboxProfile(sc.Sandbox)

	var cmd *exec.Cmd
	switch {
	case runtime == "bash":
		sockPath := SocketPath(h.dataDir)
		// Sandboxed bash: inject polyfills + args as shell variables before handler code.
		var sb strings.Builder
		sb.WriteString("set -e\n")
		// Polyfill: store() — routes cap_store calls via yesmem CLI to daemon socket
		sb.WriteString(fmt.Sprintf("YESMEM_SOCK=%s\nexport YESMEM_SOCK\n", sockPath))
		sb.WriteString(`store() { yesmem store "$1"; }
llm() {
  _body=$(yesmem json -n --arg model "${1:-}" --arg system "${2:-}" --arg prompt "${3:-}" --arg session "${4:-}" '{"model":$model,"system":$system,"prompt":$prompt,"session":$session}')
  yesmem llm-complete "$_body"
  return $?
}
`)
		var argsMap map[string]any
		json.Unmarshal([]byte(argsJSON), &argsMap)
		for k, v := range argsMap {
			val, _ := json.Marshal(v)
			fmt.Fprintf(&sb, "%s=%s\n", "ARGS_"+k, string(val))
			// Also export as shell-safe names (some caps use uppercase vars)
			fmt.Fprintf(&sb, "%s=%s\n", k, string(val))
			if upper := strings.ToUpper(k); upper != k {
				fmt.Fprintf(&sb, "%s=%s\n", upper, string(val))
			}
		}
		sb.WriteString(handlerCode)
		binary, args := sandbox.WrapExecArgs("bash", []string{"-c", sb.String()}, profile)
		if profile != ProfileNone {
			// Mount yesmem binary (for store/llm CLI calls) and daemon socket
			if yesmemBin, err := exec.LookPath("yesmem"); err == nil {
				args = append([]string{"--rw-map", yesmemBin}, args...)
			}
			if curlBin, err := exec.LookPath("curl"); err == nil {
				args = append([]string{"--rw-map", curlBin}, args...)
			}
			if bashBin, err := exec.LookPath("bash"); err == nil {
				args = append([]string{"--rw-map", bashBin}, args...)
			}
			args = append([]string{"--rw-map", sockPath}, args...)
		}
		cmd = exec.Command(binary, args...)
		cmd.Env = os.Environ()
		if sessionID != "" {
			cmd.Env = append(cmd.Env, "CAP_SESSION_ID="+sessionID)
		}

	default: // repl or empty
		sockPath := SocketPath(h.dataDir)
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
globalThis.shQuote = function(s) { return "'" + String(s).replace(/'/g, "'\\\\''") + "'"; };
globalThis.registerTool = function(name, desc, schema, handler) { _toolHandler = handler; };
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
		binary, args := sandbox.WrapExecArgs(bunPath, []string{"-e", wrapper}, profile)
		// Mount daemon socket and yesmem binary into sandbox namespace for MCP tools
		if profile != ProfileNone {
			// yesmem binary: used by sh() polyfill for cap-blob-put CLI
			if yesmemBin, err := exec.LookPath("yesmem"); err == nil {
				args = append([]string{"--rw-map", yesmemBin}, args...)
			}
			// Daemon socket: rw needed for Unix socket connections
			args = append([]string{"--rw-map", sockPath}, args...)
			// bun binary: needed for the REPL runtime itself
			if bunBin, err := exec.LookPath("bun"); err == nil {
				args = append([]string{"--rw-map", bunBin}, args...)
			}
			// curl: needed for fetching Reddit pages
			if curlBin, err := exec.LookPath("curl"); err == nil {
				args = append([]string{"--rw-map", curlBin}, args...)
			}
		}
		cmd = exec.Command(binary, args...)
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "CAP_ARGS="+argsJSON)
		if sessionID != "" {
			cmd.Env = append(cmd.Env, "CAP_SESSION_ID="+sessionID)
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		errDetail := string(output)
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

	h.auditCapExecution(meta.Name, fn, argsJSON, out, err)

	return jsonResponse(map[string]any{
		"output":   out,
		"cap_name": meta.Name,
		"fn":       fn,
		"runtime":  runtime,
	})
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

func (h *Handler) auditCapExecution(capName, fn, args, output string, execErr error) {
	shortArgs := args
	if len(shortArgs) > 200 {
		shortArgs = shortArgs[:200] + "..."
	}
	h.store.InsertLearning(&models.Learning{
		Category:  "cap_execution",
		Content:   fmt.Sprintf("execute_cap(%q, %q, args=%s) → output_len=%d err=%v", capName, fn, shortArgs, len(output), execErr),
		CreatedAt: time.Now(),
	})
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
