PATTERN ?=
TEXT ?=

TARGET := wasm32-unknown-unknown

.PHONY: build test-rust test-go test demo

build:
	cargo build --release --target $(TARGET)

test-rust:
	cargo test

test-go: build
	cd go-host && go test ./...

test: test-rust test-go

demo: build
	cd go-host && go run ./cmd/demo "$(PATTERN)" "$(TEXT)"
