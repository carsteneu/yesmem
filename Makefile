BINARY    := yesmem
INSTALL   := $(HOME)/.local/bin/$(BINARY)
INSTALL_NEW := $(INSTALL)-new
# Same lock the updater takes (lockUpdate in internal/update/download.go): it
# sits next to the installed binary, so a deploy and a running update exclude
# each other instead of swapping the same inode.
UPDATE_LOCK := $(dir $(INSTALL)).yesmem-update.lock

# Version: use git tag if available, else commit count + short hash
VERSION   := $(shell git describe --tags --always 2>/dev/null || echo "dev")

# Go build settings
export GOROOT  := $(HOME)/memory/go-sdk/go
export PATH    := $(GOROOT)/bin:$(PATH)
export GOPATH  := $(HOME)/.cache/yesmem/gopath
export GOCACHE := $(HOME)/.cache/yesmem/gocache
export CGO_ENABLED := 0

LDFLAGS := -X main.version=$(VERSION)

# bun is not on PATH everywhere (on the dev host it lives in ~/.bun/bin and
# agent shells do not see it), and the plugin suite is part of the test surface,
# so a bare "bun" in the recipe would make `make test` fail instead of testing.
BUN ?= $(shell command -v bun 2>/dev/null || echo $(HOME)/.bun/bin/bun)
PLUGIN_DIR := plugins/opencode-yesmem-v2

# Race gate selection (YM-438): -race runs in no other target. Whole-package
# -race runs are not affordable (measured on a warm cache: internal/hooks 653s,
# internal/storage 875s, internal/daemon exceeds the 600s default and panics),
# so the gate names the packages that carried the race classes and, inside them,
# the tests that exercise the concurrent wiring. RACE_RUN is used by the recipe
# below and repeated verbatim in ci.yml; the guard test in
# ci_build_surface_los60e_test.go fails when the two selections differ, when a
# package of RACE_PKGS is missing from either side, or when a name in RACE_RUN
# no longer exists as a test function.
RACE_PKGS := ./internal/proxy ./internal/storage ./internal/daemon ./internal/hooks ./internal/guard
RACE_RUN := TestLLMWiringIsRaceFreeAndVisible|TestEmbedWiringIsRaceFreeAndVisible|TestVectorSearchWithoutWiring|TestStartEmbedWorkerPublishesQueue|TestConsolidationRunnerPersistsBaselineAndCoalescesConcurrentRuns|TestResolveProjectParam_GenerationRaceGuard|TestResolveProjectParam_GenerationConcurrentAccess|TestAttemptRestart_RaceGuard|TestLos58e_TwoConcurrentSpawnsKeepTheirOwnSession|TestAgentNextIDConcurrent_Unique|TestCapStoreConcurrent_NoDeadlock|TestCapsUpsert_CrossGoroutineReadAfterWrite|TestCapsClaimAndRead_ClaimsAtomic|TestDeleteDocSource_IsAtomic|TestEnsureUniqueMessageSequenceUnderConcurrentCallers|TestEnsureUniqueMessageSequenceWaitsForAConcurrentRepair|TestReplaceMessagesBySessionIsAtomic|TestGetPendingRecoveryIsAtomicUnderConcurrency|TestIncrementTurnCount_Concurrent|TestCallModelCachesTheAnswer|TestDaemonCall_Success|TestDaemonCall_Unreachable|TestRunSessionEnd_CallsDaemon|TestRunSessionEnd_DaemonUnreachable

.PHONY: build install deploy deploy-safe restart-services test test-go test-plugin test-race benchmark clean update vet

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

benchmark: build
	@echo "Run: ./$(BINARY) locomo-bench --help"

install: build
	@if [ -L "$(UPDATE_LOCK)" ]; then echo "refusing: $(UPDATE_LOCK) is a symlink, not the lock file"; exit 1; fi
	@( umask 077; : >> $(UPDATE_LOCK); chmod go-rw $(UPDATE_LOCK) 2>/dev/null || true )
	@if command -v flock >/dev/null 2>&1; then \
		flock -w 700 "$(UPDATE_LOCK)" sh -c 'tmp=$$(mktemp "$(INSTALL_NEW).XXXXXX") && cp "$(BINARY)" "$$tmp" && chmod 0755 "$$tmp" && mv -f "$$tmp" "$(INSTALL)"'; \
	else \
		echo "warning: flock not found, installing without the update lock"; \
		tmp=$$(mktemp "$(INSTALL_NEW).XXXXXX") && cp "$(BINARY)" "$$tmp" && chmod 0755 "$$tmp" && mv -f "$$tmp" "$(INSTALL)"; \
	fi
	@echo "installed $(VERSION) → $(INSTALL)"

# For deployment from an agent, use deploy-safe. Its worker survives restarting
# both yesmem and the agent's server, and restores services on failure.
deploy-safe: build
	bash scripts/deploy-safe.sh

deploy: build
	@if [ -L "$(UPDATE_LOCK)" ]; then echo "refusing: $(UPDATE_LOCK) is a symlink, not the lock file"; exit 1; fi
	@( umask 077; : >> $(UPDATE_LOCK); chmod go-rw $(UPDATE_LOCK) 2>/dev/null || true )
	@if command -v flock >/dev/null 2>&1; then \
		flock -w 700 "$(UPDATE_LOCK)" sh -c 'tmp=$$(mktemp "$(INSTALL_NEW).XXXXXX") && cp "$(BINARY)" "$$tmp" && chmod 0755 "$$tmp" && mv -f "$$tmp" "$(INSTALL)"'; \
	else \
		echo "warning: flock not found, installing without the update lock"; \
		tmp=$$(mktemp "$(INSTALL_NEW).XXXXXX") && cp "$(BINARY)" "$$tmp" && chmod 0755 "$$tmp" && mv -f "$$tmp" "$(INSTALL)"; \
	fi
	cp scripts/injector.js $(HOME)/.claude/yesmem/injector.js
	@if [ ! -f $(HOME)/.claude/yesmem/SYSTEM.md ]; then \
		mkdir -p $(HOME)/.claude/yesmem; \
		cp configs/SYSTEM.md $(HOME)/.claude/yesmem/SYSTEM.md; \
	fi
	mkdir -p $(HOME)/.config/opencode/plugins/yesmem-window
	cp plugins/yesmem-window/tui.tsx $(HOME)/.config/opencode/plugins/yesmem-window/
	# The plan block is its own plugin, the way the todo plugin is its own entry.
	# opencode-sidebar (cache status) is deactivated for now: keep the source, do not
	# install it. Re-enable by adding it back to this list once it is verified.
	@for plugin in opencode-plan; do \
		dst=$(HOME)/.config/opencode/plugins/$$plugin; \
		mkdir -p $$dst; \
		cp plugins/$$plugin/tui.tsx plugins/$$plugin/package.json $$dst/; \
		ls plugins/$$plugin/*.ts >/dev/null 2>&1 && cp plugins/$$plugin/*.ts $$dst/ || true; \
		if command -v bun >/dev/null 2>&1; then \
			(cd $$dst && bun install --silent) || \
			echo "warning: bun install failed for $$plugin; that plugin stays unloaded"; \
		else \
			echo "warning: bun not found, skipping $$plugin dependencies"; \
		fi; \
	done
	mkdir -p $(HOME)/.config/opencode/themes
	@for f in plugins/themes/*.json; do \
		dst=$(HOME)/.config/opencode/themes/$$(basename $$f); \
		install -m 644 $$f $$dst.tmp && mv -f $$dst.tmp $$dst || exit 1; \
	done
	$(MAKE) reap-stale-mcp
	$(MAKE) restart-services
	@echo "deployed $(VERSION) → $(INSTALL)"

# Kill langlaufende yesmem-mcp-Subprozesse, die noch das alte Binary
# gemappt haben (/proc/PID/exe → ...yesmem (deleted)). Der mv-Schritt in
# deploy ersetzt die Inode; langlaufende MCP-Children der opencode-Clients
# behalten den alten Inode und servieren ein veraltetes Tool-Schema, bis
# sie sterben und opencode sie neu forkt. Siehe Learning #81467.
# Die Auswahl trifft das Binary, nicht die Shell: dieselbe argv-geankerte
# Erkennung wie in `yesmem status` (YM-312).
reap-stale-mcp:
	@"$(INSTALL)" reap-stale-mcp

restart-services:
	@if systemctl --user list-unit-files yesmem.service >/dev/null 2>&1; then \
		systemctl --user restart yesmem; \
	else \
		"$(INSTALL)" stop daemon || true; \
		nohup "$(INSTALL)" daemon --replace >/tmp/yesmem-daemon.out 2>/tmp/yesmem-daemon.err < /dev/null & \
		sleep 2; \
	fi
	@if systemctl --user list-unit-files yesmem-proxy.service >/dev/null 2>&1; then \
		systemctl --user restart yesmem-proxy; \
	else \
		"$(INSTALL)" stop proxy || true; \
		sleep 1; \
		nohup "$(INSTALL)" proxy >/tmp/yesmem-proxy.out 2>/tmp/yesmem-proxy.err < /dev/null & \
	fi

# Same package list as test: ./... would break on the ignored go-path/ dir.
vet:
	go vet ./internal/... ./skills/... .

# The plugin suite is part of the test surface: without the install step it
# aborts on the missing devDependency and looks like a code failure (YM-434).
test: test-go test-plugin

test-go:
	go test ./internal/... ./skills/... -count=1
	go test -count=1 .

test-plugin:
	@command -v "$(BUN)" >/dev/null 2>&1 || { echo "bun not found: install bun (https://bun.sh) or set BUN=/path/to/bun"; exit 1; }
	cd $(PLUGIN_DIR) && "$(BUN)" install --frozen-lockfile || { echo "bun install --frozen-lockfile failed in $(PLUGIN_DIR): bun.lock is out of date with package.json, run '$(BUN) install' there and commit bun.lock"; exit 1; }
	cd $(PLUGIN_DIR) && "$(BUN)" test

# See RACE_PKGS/RACE_RUN above for what this covers and why it filters.
test-race:
	CGO_ENABLED=1 go test -race -count=1 -timeout 900s ./internal/proxy
	CGO_ENABLED=1 go test -race -count=1 -timeout 900s -run '$(RACE_RUN)' ./internal/storage ./internal/daemon ./internal/hooks ./internal/guard

clean:
	rm -f $(BINARY)

update:
	@echo "=== Checking for updates ==="
	@go list -m -u -json all 2>/dev/null | python3 -c "\
	import sys, json; \
	raw = sys.stdin.read().strip(); \
	[raw := '{' + p + '}' if not p.startswith('{') else p for _ in [0] for p in [raw]][0]; \
	parts = raw.replace('}\n{', '}|||{').split('|||'); \
	mods = []; \
	[mods.append((o['Path'], o.get('Version','?'), o['Update']['Version'])) for p in parts if (o := json.loads(p)) and o.get('Update') and not o.get('Indirect') and not o.get('Main')]; \
	[print(f'  {p:<50s} {c:<15s} → {u}') for p, c, u in sorted(mods)] if mods else print('  All direct dependencies up to date.'); \
	" || echo "  (python3 not available, run manually: go list -m -u all)"
	@echo ""
	@echo "=== Updating direct dependencies ==="
	go get -u -d ./internal/... ./skills/... .
	go mod tidy -e
	@echo ""
	@echo "=== Build ==="
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo ""
	@echo "=== Test ==="
	$(MAKE) test
	@echo ""
	@echo "=== Done — review changes with: git diff go.mod go.sum ==="
