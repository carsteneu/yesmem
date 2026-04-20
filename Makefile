BINARY    := yesmem
INSTALL   := $(HOME)/.local/bin/$(BINARY)
INSTALL_NEW := $(INSTALL)-new

# Version: use git tag if available, else commit count + short hash
VERSION   := $(shell git describe --tags --always 2>/dev/null || echo "dev")

# Go build settings
export GOROOT  := $(HOME)/memory/go-sdk/go
export PATH    := $(GOROOT)/bin:$(PATH)
export GOPATH  := $(HOME)/.cache/yesmem/gopath
export GOCACHE := $(HOME)/.cache/yesmem/gocache
export CGO_ENABLED := 0

LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install deploy restart-services test benchmark clean update

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

benchmark: build
	@echo "Run: ./$(BINARY) locomo-bench --help"

install: build
	cp $(BINARY) $(INSTALL)

deploy: build
	cp $(BINARY) $(INSTALL_NEW)
	mv -f $(INSTALL_NEW) $(INSTALL)
	cp scripts/injector.js $(HOME)/.claude/yesmem/injector.js
	$(MAKE) restart-services
	@echo "deployed $(VERSION) → $(INSTALL)"

restart-services:
	@if systemctl --user list-unit-files yesmem.service >/dev/null 2>&1; then \
		systemctl --user restart yesmem; \
	elif pgrep -af "$(INSTALL) daemon|$(BINARY) daemon" >/dev/null 2>&1; then \
		nohup $(INSTALL) daemon --replace >/tmp/yesmem-daemon.out 2>/tmp/yesmem-daemon.err < /dev/null & \
		sleep 2; \
	fi
	@if systemctl --user list-unit-files yesmem-proxy.service >/dev/null 2>&1; then \
		systemctl --user restart yesmem-proxy; \
	elif pgrep -af "$(INSTALL) proxy|$(BINARY) proxy" >/dev/null 2>&1; then \
		pkill -f "$(INSTALL) proxy|$(BINARY) proxy" || true; \
		sleep 1; \
		nohup $(INSTALL) proxy >/tmp/yesmem-proxy.out 2>/tmp/yesmem-proxy.err < /dev/null & \
	fi

test:
	go test ./internal/... ./skills/... -count=1
	go test -count=1 .

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
