.PHONY: all install uninstall go rust zig sdk-ts test rust-test go-test brand-check zh-hant-check docs-build quality-check swarm-integration-test bench-gate-test audit-bench release-check release-preflight release-ready release-preflight-test release-package integration-package homebrew-formula-test homebrew-install-test platform-smoke vscode-test clean

PREFIX ?= $(HOME)/.local
BINDIR = $(PREFIX)/bin
ZIG_TOOLS = carina-scan carina-grep carina-diff carina-run carina-pty carina-patch-native

all: go rust zig

# Mirrors the release-package bin/ layout (minus the pinned Headroom bundle):
# the daemon discovers the kernel service and native tools next to its own
# binary, so everything installs flat into one directory.
install: all
	cargo build --release -p carina-kernel --bin carina-kernel-service
	install -d $(BINDIR)
	install -m 755 bin/carina bin/carina-daemon bin/carina-worker $(BINDIR)
	install -m 755 target/release/carina-kernel-service $(BINDIR)
	for name in $(ZIG_TOOLS); do install -m 755 zig/zig-out/bin/$$name $(BINDIR) || exit 1; done
	# Retired binary — interactive shell is bare `carina` only.
	rm -f $(BINDIR)/carina-tui
	@echo "Installed to $(BINDIR). Ensure it is on PATH."

uninstall:
	rm -f $(addprefix $(BINDIR)/,carina carina-daemon carina-worker carina-tui carina-kernel-service $(ZIG_TOOLS))

go:
	rm -f bin/carina-tui
	go build -o bin/carina ./apps/carina-cli
	go build -o bin/carina-daemon ./apps/carina-daemon
	go build -o bin/carina-worker ./apps/carina-worker
	go vet ./...

test: rust-test go-test

brand-check:
	python3 scripts/brand_assets.py

# Fail if Simplified Chinese catalogs changed without regenerating Traditional.
# Requires: pip install zhconv
zh-hant-check:
	python3 scripts/gen_zh_hant.py --check

# Docs site production build smoke (Astro + Starlight).
docs-build:
	cd apps/docs && pnpm install --frozen-lockfile && pnpm run build

# Lightweight quality gates for i18n derivation + docs site (also run in CI).
quality-check: brand-check zh-hant-check docs-build

bench-gate-test:
	bash scripts/test-bench-gate.sh

audit-bench:
	bash scripts/bench-audit.sh

rust-test:
	cargo test --workspace

go-test: rust
	cp target/*/carina-kernel-service bin/ 2>/dev/null || cargo build --release -p carina-kernel --bin carina-kernel-service
	CARINA_KERNEL_BIN=$(PWD)/target/release/carina-kernel-service go test ./...

# P4 acceptance test (Agent Swarm design): real carina-daemon and
# carina-worker BINARIES as separate OS processes over a real TCP socket,
# not in-process RPC-handler calls — see
# apps/carina-worker/integration_test.go. Opt-in (not part of `test`/`go-test`):
# builds and spawns real subprocesses, slower and heavier than a unit test.
swarm-integration-test: go
	cargo build --release -p carina-kernel --bin carina-kernel-service
	CARINA_KERNEL_BIN=$(PWD)/target/release/carina-kernel-service \
	CARINA_DAEMON_BIN=$(PWD)/bin/carina-daemon \
	CARINA_WORKER_BIN=$(PWD)/bin/carina-worker \
	go test -tags integration ./apps/carina-worker/... -run TestSwarmRemoteDispatchAcrossRealProcesses -v

rust:
	cargo check --workspace

zig:
	./scripts/build-zig-tools.sh

sdk-ts:
	cd sdk/typescript && npm install && npm run build

release-check:
	./scripts/release-check.sh

release-preflight:
	./scripts/release-preflight.sh

release-ready:
	./scripts/release-preflight.sh --strict --online

release-preflight-test:
	./scripts/test-release-preflight.sh

release-package:
	./scripts/package-release.sh

integration-package:
	./scripts/package-integrations.sh

homebrew-formula-test:
	./scripts/test-homebrew-formula.sh

homebrew-install-test:
	VERSION=$${VERSION:?VERSION is required} ./scripts/test-homebrew-install.sh

platform-smoke:
	./scripts/test-platform-packaging.sh

vscode-test:
	cd integrations/vscode && npm install && npm test

clean:
	rm -rf bin target zig/zig-out zig/.zig-cache
