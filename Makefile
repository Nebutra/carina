.PHONY: all go rust zig sdk-ts clean

all: go rust zig

go:
	go build -o bin/pi ./apps/pi-cli
	go build -o bin/pi-daemon ./apps/pi-daemon
	go build -o bin/pi-tui ./apps/pi-tui
	go vet ./...

rust:
	cargo check --workspace

zig:
	cd zig && zig build

sdk-ts:
	cd sdk/typescript && npm install && npm run build

clean:
	rm -rf bin target zig/zig-out zig/.zig-cache
