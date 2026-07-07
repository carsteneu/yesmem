# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

YesMem is a single Go binary (module `github.com/carsteneu/yesmem`, Go 1.25) that gives Claude Code and OpenCode persistent, searchable memory. It bundles four cooperating layers: a CLI, a long-running daemon, an HTTP proxy, and an event-driven hook layer. The same binary also serves the MCP tool surface and ships a TypeScript OpenCode plugin.

`AGENTS.md` holds the authoritative agent conventions; this file is the Claude Code entry point and cross-references it.

## Build, test, lint

- `make build` builds with `CGO_ENABLED=0` (pure Go, static). Do not invoke `go build` directly; the Makefile sets the right flags and ldflags.
- `make test` runs `go test ./internal/... ./skills/... -count=1 && go test -count=1 .`.
- Single test: `go test ./internal/proxy/ -run TestCollapse -count=1` (swap the package path and `-run` pattern as needed).
- CI (`/.github/workflows/ci.yml`) runs `go test ./... -count=1 -timeout 120s` with `CGO_ENABLED=0` on Ubuntu and macOS, plus a Bun setup step (for the plugin).
- There is no separate lint step; formatting is `gofmt`. Match existing style.

### GOROOT gotcha (this machine)

On the developer machine `GOROOT` may point at `~/memory/go-sdk/go`. If `make build` fails to find the toolchain, override it explicitly:

```
GOROOT=/usr/local/go make build
```

### Known pre-existing test failures

`internal/update` has two tests that fail by design in CI/local sandboxes (they hit the network and GitHub release assets): `TestFindAssetURL` and `TestFullUpdateFlow`. Do not treat these as regressions you introduced; do not "fix" them by skipping without checking with the owner first.

## OpenCode plugin (TypeScript + Bun)

`plugins/opencode-yesmem/` is a Bun/TypeScript plugin. Typecheck with `npm run typecheck` (runs `bun run --check index.ts`). Edit the `.ts` files in place; there is no build step.

## Architecture (the parts that span multiple files)

**CLI dispatch.** `main.go` is a switch over subcommands. Each top-level command lives in its own `cmd_*.go` file in the repo root (for example `cmd_serve.go`, `cmd_search.go`). Logic is pushed down into `internal/`, which is organized one-concern-per-file. When adding a command, follow this split: a thin `cmd_<name>.go` handler that delegates to an `internal/` package.

**Daemon + store.** The daemon is a background process owning the on-disk store under `~/.claude/yesmem/`: SQLite databases, a vector store, and logs. It is the single writer. Other layers talk to it rather than touching the store directly. The SQLite busy timeout is set high (30s) specifically for multi-writer contention; do not lower it without understanding the write pattern.

**HTTP proxy.** Listens on `:9099` by default and sits in the request path to perform context collapsing (folding old turns into compact stubs to keep context windows bounded). The proxy package owns token accounting and collapse logic.

**Hooks.** An event-driven layer that reacts to tool events. New behavior that must trigger on tool use goes here, not in the daemon.

**MCP server.** Built with `github.com/mark3labs/mcp-go`. The MCP tool surface is assembled in `internal/httpapi/assemble.go`; adding an MCP tool means wiring it there plus implementing the handler in the relevant `internal/` package.

**Caps and skills.** Caps are saved, executable tool definitions (persisted, testable). Skills are reusable prompt/tool bundles. Both are first-class artifacts in this repo, not scratch files.

**Extraction.** `internal/extraction/` handles LLM-driven extraction (uses an OpenAI-compatible client). Note the per-task `max_tokens` tuning (for example code-describe needs a large limit for reasoning models) lives in this package.

## Conventions specific to this repo

- **TDD for Go.** Write the test first, then implement. Never commit Go code without a covering test; never commit with failing tests (except the two documented `internal/update` failures).
- **No `panic` in library code.** Libraries return errors; only `main.go` may exit on fatal conditions.
- **Stdlib first.** Reach for third-party only when the stdlib genuinely cannot do it (current third-party deps are deliberate: `mcp-go`, `goldmark`, bloom filter, tokenizer, pty, fsnotify).
- **Comments only for non-obvious WHY.** Do not narrate what code does; the code already says that.
- **Persistence is one-writer.** If you need to read or write store data, go through the daemon/proxy API, not direct file access.

## Where to look next

- `README.md` full feature list, install, and the published MCP tool reference.
- `AGENTS.md` extended agent conventions and the per-package map.
- `docs/architecture.md` and `docs/implementation.md` per-subsystem deep dives.
- `config.example.yaml` full configuration reference.
