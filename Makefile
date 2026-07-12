.PHONY: all go rust zig sdk-ts test rust-test go-test swarm-integration-test bench-gate-test audit-bench release-check release-preflight release-ready release-preflight-test release-package integration-package homebrew-formula-test homebrew-install-test platform-smoke vscode-test clean

all: go rust zig

go:
	go build -o bin/carina ./apps/carina-cli
	go build -o bin/carina-daemon ./apps/carina-daemon
	go build -o bin/carina-worker ./apps/carina-worker
	go build -o bin/carina-tui ./apps/carina-tui
	go vet ./...

test: rust-test go-test

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
