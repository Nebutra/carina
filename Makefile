.PHONY: all go rust zig sdk-ts test rust-test go-test release-check release-package homebrew-formula-test homebrew-install-test clean

all: go rust zig

go:
	go build -o bin/carina ./apps/carina-cli
	go build -o bin/carina-daemon ./apps/carina-daemon
	go build -o bin/carina-worker ./apps/carina-worker
	go build -o bin/carina-tui ./apps/carina-tui
	go vet ./...

test: rust-test go-test

rust-test:
	cargo test --workspace

go-test: rust
	cp target/*/carina-kernel-service bin/ 2>/dev/null || cargo build --release -p carina-kernel --bin carina-kernel-service
	CARINA_KERNEL_BIN=$(PWD)/target/release/carina-kernel-service go test ./...

rust:
	cargo check --workspace

zig:
	cd zig && zig build

sdk-ts:
	cd sdk/typescript && npm install && npm run build

release-check:
	./scripts/release-check.sh

release-package:
	./scripts/package-release.sh

homebrew-formula-test:
	./scripts/test-homebrew-formula.sh

homebrew-install-test:
	VERSION=$${VERSION:?VERSION is required} ./scripts/test-homebrew-install.sh

clean:
	rm -rf bin target zig/zig-out zig/.zig-cache
