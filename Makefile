# DEPRECATED: Make remains as a compatibility wrapper while Taskfile.yml is
# introduced. Prefer `task --list-all` for the task-native command surface.
$(warning Makefile is deprecated; prefer Taskfile.yml via `task --list-all`)

.PHONY: build build-p2p build-all install upgrade install-cli run dev start stop setup test test-integration test-integration-up test-integration-down test-integration-overnight lint clean uninstall web-build go-build go-build-p2p secret go-build-platforms rules-sync check-deadcode test-upgrade-smoke preflight

GIT_VERSION ?= $(shell git describe --tags --dirty --always --abbrev=12 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
GO_LDFLAGS ?= -X main.buildVersion=$(GIT_VERSION) -X main.buildCommit=$(GIT_COMMIT)

# Default build: slim binary without libp2p (~30 MB).
build: web-build go-build

# Variant build: includes libp2p (~54 MB, +23 MB). Output: bin/mcplexer-p2p.
build-p2p: web-build go-build-p2p

# Both variants in one shot. Useful for releases / CI matrix.
build-all: web-build go-build go-build-p2p

# preflight — check Go 1.25+ and Node 20+ before building. Exits with a
# clear error and download link when a tool is missing or too old.
preflight:
	@# Go version check
	@command -v go >/dev/null 2>&1 || { echo "ERROR: Go is not installed." && echo "" && echo "Install Go 1.25+ from: https://go.dev/dl/" && exit 1; }
	@GO_VER=$$(go version | awk '{print $$3}' | sed 's/go//') && \
	GO_MAJOR=$$(echo "$$GO_VER" | cut -d. -f1) && \
	GO_MINOR=$$(echo "$$GO_VER" | cut -d. -f2) && \
	( [ "$$GO_MAJOR" -gt 1 ] || { [ "$$GO_MAJOR" -eq 1 ] && [ "$$GO_MINOR" -ge 25 ]; } ) || \
	{ echo "ERROR: Go $$GO_VER found, but Go 1.25+ is required." && echo "" && echo "Upgrade from: https://go.dev/dl/" && exit 1; }
	@# Node version check
	@command -v node >/dev/null 2>&1 || { echo "ERROR: Node.js is not installed." && echo "" && echo "Install Node 20+ from: https://nodejs.org/" && exit 1; }
	@NODE_VER=$$(node --version | sed 's/v//') && \
	NODE_MAJOR=$$(echo "$$NODE_VER" | cut -d. -f1) && \
	[ "$$NODE_MAJOR" -ge 20 ] || \
	{ echo "ERROR: Node $$NODE_VER found, but Node 20+ is required." && echo "" && echo "Upgrade from: https://nodejs.org/ (LTS recommended)" && exit 1; }
	@echo "preflight: Go $$(go version | awk '{print $$3}' | sed 's/go//'), Node $$(node --version | sed 's/v//') — OK"

# install — primary install path. Builds the daemon binary, copies it into
# the stable location at ~/.mcplexer/bin/mcplexer, then runs the interactive
# setup (configures detected MCP clients, installs the launchd plist, opens
# the dashboard). The web UI is bundled into the Go binary via go:embed and
# served at http://localhost:3333. Install it as a Chrome PWA from there.
install: preflight build-p2p
	@echo "==> Syncing daemon binary to ~/.mcplexer/bin (atomic)..."
	@mkdir -p $$HOME/.mcplexer/bin
	@cp bin/mcplexer-p2p $$HOME/.mcplexer/bin/.mcplexer.new
	@chmod +x $$HOME/.mcplexer/bin/.mcplexer.new
	@mv $$HOME/.mcplexer/bin/.mcplexer.new $$HOME/.mcplexer/bin/mcplexer
	@echo "==> Running setup (launchd + MCP client config)..."
	$$HOME/.mcplexer/bin/mcplexer setup
	@echo "==> Hardening data dir file modes..."
	@bash scripts/harden-data-dir.sh
	@echo "==> Syncing agent rules into ~/.claude/CLAUDE.md..."
	@$$HOME/.mcplexer/bin/mcplexer rules sync || echo "  (skipped; agent rules will sync on next setup)"
	@echo ""
	@echo "✓ MCPlexer is running at http://localhost:3333"
	@echo "  In Chrome / Edge / Arc, click the install icon in the address bar"
	@echo "  to install MCPlexer as a desktop app (PWA)."

# upgrade — hardened in-place daemon swap. Builds first, then delegates to
# scripts/upgrade.sh which waits for drain, swaps with rollback, verifies
# readiness, and rolls back on failure. Pass UPGRADE_FLAGS=--dry-run to
# preview without making changes.
upgrade: build-p2p
	@bash scripts/upgrade.sh ${UPGRADE_FLAGS:-}

test-upgrade-smoke:
	@bash scripts/upgrade.sh --dry-run --binary bin/mcplexer-p2p 2>/dev/null || echo "(smoke: dry-run with no installed daemon — expected)"

install-cli: preflight build
	./bin/mcplexer setup

# rules-sync — manually re-run the marker-bounded mcplexer block install
# into ~/.claude/CLAUDE.md. install + upgrade call this automatically;
# use it standalone when you've bumped the block content during dev or
# want to refresh after editing ~/.claude/CLAUDE.md by hand.
rules-sync: build
	./bin/mcplexer rules sync

web-build:
	cd web && npm ci && npm run build

go-build:
	go build -ldflags "$(GO_LDFLAGS)" -o bin/mcplexer ./cmd/mcplexer
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign --force -s - bin/mcplexer; fi

go-build-p2p:
	go build -tags p2p -ldflags "$(GO_LDFLAGS)" -o bin/mcplexer-p2p ./cmd/mcplexer
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign --force -s - bin/mcplexer-p2p; fi

run: build
	-./bin/mcplexer daemon stop 2>/dev/null
	./bin/mcplexer daemon start

dev:
	go run ./cmd/mcplexer serve --mode=http --addr=:3333

start:
	./bin/mcplexer daemon start

stop:
	./bin/mcplexer daemon stop

setup:
	./bin/mcplexer setup

status:
	./bin/mcplexer status

test:
	go test ./...
	go test -tags p2p ./internal/p2p/...

# test-integration — multi-node docker harness exercising pairing, mesh
# send, skill registry, worker run, audit. Always cleans up on success
# OR failure (trap inside the wrapper). Use TEST_KEEP=1 to skip teardown
# for debugging.
test-integration:
	@bash test/integration/run.sh

test-integration-up:
	@docker compose -f test/integration/docker-compose.yml up --build -d

test-integration-down:
	@docker compose -f test/integration/docker-compose.yml down -v --remove-orphans

# test-integration-overnight — repeat the bulletproof e2e suite N times
# (default 10, override via RUNS_N=N) and aggregate flake / stable-fail /
# stable-skip results. Drives `make test-integration` with BULLETPROOF=1
# under the hood; per-run logs land in test/integration/_logs/run-N/.
test-integration-overnight:
	@bash test/integration/run_overnight.sh $${RUNS_N:-10}

lint:
	golangci-lint run --build-tags p2p

secret:
	@if [ -z "$(SCOPE)" ] || [ -z "$(KEY)" ] || [ -z "$(VALUE)" ]; then \
		echo "Usage: make secret SCOPE=<scope-id> KEY=<key> VALUE=<value>"; \
		exit 1; \
	fi
	./bin/mcplexer secret put $(SCOPE) $(KEY) $(VALUE)

clean:
	rm -rf bin/ web/dist/

uninstall:
	-./bin/mcplexer daemon stop 2>/dev/null
	-./bin/mcplexer daemon uninstall 2>/dev/null
	rm -f /usr/local/bin/mcplexer
	rm -rf ~/.mcplexer/bin/
	@echo "MCPlexer daemon uninstalled."
	@echo "If you also installed the PWA in Chrome: chrome://apps → right-click MCPlexer → Remove."

# Sweep orphan agent worktrees. Dry-run by default; `make worktrees-gc-yes`
# actually removes. Branches are preserved — only the worktree dir + the
# lock file go. Recover any with `git worktree add <new-path> <branch>`.
worktrees-gc:
	@bash scripts/worktrees-gc.sh

worktrees-gc-yes:
	@bash scripts/worktrees-gc.sh --yes

# Cross-compile Go for all platforms
go-build-platforms:
	GOOS=darwin GOARCH=arm64 go build -tags p2p -ldflags "$(GO_LDFLAGS)" -o bin/darwin/arm64/mcplexer ./cmd/mcplexer
	GOOS=darwin GOARCH=amd64 go build -tags p2p -ldflags "$(GO_LDFLAGS)" -o bin/darwin/amd64/mcplexer ./cmd/mcplexer
	GOOS=linux GOARCH=amd64 go build -tags p2p -ldflags "$(GO_LDFLAGS)" -o bin/linux/amd64/mcplexer ./cmd/mcplexer
	GOOS=linux GOARCH=arm64 go build -tags p2p -ldflags "$(GO_LDFLAGS)" -o bin/linux/arm64/mcplexer ./cmd/mcplexer

check-deadcode:
	@sh scripts/check-deadcode.sh
