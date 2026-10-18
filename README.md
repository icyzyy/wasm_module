# wasm_module

This repository is a demonstration of clean-architecture Rust compiled to
WebAssembly, consumed in-process by a Go host. The Rust crate implements a
regexp matcher across layered modules (domain / application / adapter), and
the Go host drives it through a raw FFI protocol — no CLI subprocess, no
JS glue — using [wazero](https://github.com/tetratelabs/wazero), a
zero-dependency WebAssembly runtime.

## Directory layout

```
wasm_module/
├── src/            # Rust crate: layers from inner to outer
│   └── adapters/   # Adapter layer: wasm_ffi.rs — wasm exports + shared input buffer
├── go-host/        # Go host: everything on the Go side of the boundary
│   ├── wasmregexp/ # Host package wrapping the wasm module behind a Go API
│   └── cmd/demo/   # Runnable demo CLI (this is the "see it work" entry point)
├── target/         # Cargo output; the .wasm artifact lands under
│                   # target/wasm32-unknown-unknown/release/
├── docs/           # Project notes
└── Makefile        # Build, test, and demo entry points
```

Layer responsibilities:

- **Domain** (`src/domain`): pure regexp logic, no wasm awareness.
- **Application** (`src/application`): the `check_pattern` use case
  orchestrating domain calls and returning `Result<bool, InvalidPattern>`.
- **Adapter / FFI** (`src/adapters/wasm_ffi.rs`): the only code that knows
  about wasm — it exposes the exports described below, produces the FFI
  result codes (1/0/-1/-2), and shares a 65536-byte input buffer with the
  host.
- **Go host** (`go-host/wasmregexp`): loads the module with wazero, writes
  inputs into the buffer, calls the exports, and translates result codes into
  an idiomatic Go API (`New`, `CheckRegexpPattern`, `Version`, `Close`,
  `FindWASM`).
- **Demo CLI** (`go-host/cmd/demo`): the user-facing command tying it
  together.

## Prerequisites

- Rust with the wasm target:
  ```
  rustup target add wasm32-unknown-unknown
  ```
- Go 1.26+

## Build

```
make build
```

Compiles the crate in release mode for `wasm32-unknown-unknown`, producing
`target/wasm32-unknown-unknown/release/wasm_module.wasm`.

## Test

```
make test
```

Runs the Rust tests (`make test-rust` → `cargo test`) and the Go tests
(`make test-go` → builds the wasm artifact first, then
`cd go-host && go test ./...`).

## Demo

```
make demo PATTERN='^ab.*' TEXT='abcdef'
# module version: 0.1.0
# CheckRegexpPattern("^ab.*", "abcdef") = true

make demo PATTERN='^ab.*' TEXT='xyz'
# module version: 0.1.0
# CheckRegexpPattern("^ab.*", "xyz") = false
```

The Makefile runs the demo from `go-host/`, which auto-discovers the wasm
artifact under `../target/wasm32-unknown-unknown/release`. To point the
command at a specific module yourself:

```
cd go-host && go run ./cmd/demo -wasm /path/to/wasm_module.wasm '^ab.*' 'abcdef'
```

A non-match is a result, not an error: it prints `= false` and exits 0.
Errors (bad pattern, missing artifact, oversized inputs) print to stderr and
exit 1.

## FFI protocol

Both sides speak a shared 65536-byte input buffer, exported by the module as
the global `IN_BUF` (alongside its linear `memory`). The buffer holds
`pattern` bytes starting at offset 0, followed by `text` bytes starting at
offset `pattern_len`.

| Export | Signature | Behavior |
| --- | --- | --- |
| `check_regexp_pattern` | `check_regexp_pattern(pattern_len, text_len) -> i32` | Reads pattern at `[0, pattern_len)` and text at `[pattern_len, pattern_len+text_len)` from `IN_BUF`. Returns `1` (match), `0` (no match), `-1` (invalid pattern), or `-2` (invalid input: too large or not valid UTF-8). |
| `version` | `version() -> i32` | Writes the crate version string (e.g. `0.1.0`) into `IN_BUF` at offset 0 and returns its length in bytes; the host reads that many bytes back. |

Match semantics: matching is a **search for the pattern anywhere in the
text** (is-match, not full-string match) and is **case-sensitive**. The empty
pattern matches every text.
