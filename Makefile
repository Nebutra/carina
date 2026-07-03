.PHONY: all go rust zig sdk-ts test rust-test go-test clean

all: go rust zig

go:
	go build -o bin/pi ./apps/pi-cli
	go build -o bin/pi-daemon ./apps/pi-daemon
	go build -o bin/pi-worker ./apps/pi-worker
	go build -o bin/pi-tui ./apps/pi-tui
	go vet ./...

test: rust-test go-test

rust-test:
	cargo test --workspace

go-test: rust
	cp target/*/pi-kernel-service bin/ 2>/dev/null || cargo build --release -p pi-kernel --bin pi-kernel-service
	PI_KERNEL_BIN=$(PWD)/target/release/pi-kernel-service go test ./...

rust:
	cargo check --workspace

zig:
	cd zig && zig build

sdk-ts:
	cd sdk/typescript && npm install && npm run build

clean:
	rm -rf bin target zig/zig-out zig/.zig-cache
