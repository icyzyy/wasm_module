# Rust Wasm Regexp Module (Go/wazero host) — Design

Date: 2026-10-18
Status: approved in chat; user opted to skip the builder-review round.

## Intent

An example project showing **clean architecture inside a Rust WebAssembly
module** consumed by a **Go program via wazero** (standard Go toolchain, not
TinyGo, no JavaScript glue). The primary operation is regexp matching:
`CheckRegexpPattern(pattern, text string) (bool, error)`, with a `version`
export so the host can verify which module it loaded.

## Goals

- Two exported Wasm functions: `check_regexp_pattern` (the operation) and
  `version` (module identity; writes `CARGO_PKG_VERSION` bytes into `IN_BUF`,
  returns the length). Both snake_case — export names are the Rust names; the
  Go-facing methods stay `CheckRegexpPattern` and `Version`.
  (`memory` and one global
  are unavoidable plumbing — Wasm has no string parameters.)
- Rust layers with the dependency rule pointing inward only:
  `adapters → application → domain`.
- Domain + application are pure Rust, unit-tested natively (no Wasm needed).
- Only the adapter layer knows it is inside Wasm.
- Go side exposes idiomatic methods (`CheckRegexpPattern`, `Version`);
  wazero plumbing stays in one file.
- Integration tests on the Go side drive the real built `.wasm` through wazero.

## Non-goals / anti-goals

- No `wasm-bindgen`, no JS host, no WASI imports, no TinyGo.
- No pattern caching or compiled-pattern reuse (possible extension, YAGNI now).
- Failure = layering is cosmetic (everything reachable from the FFI function),
  or tests only exist on one side.

## Constraints

- Rust: stable cargo 1.98+, edition 2024, target `wasm32-unknown-unknown`,
  crate-type `["cdylib", "rlib"]`, sole dependency: `regex`.
  rust-skills style applies: `#[unsafe(no_mangle)]` forms, `// SAFETY:`
  comments on unsafe blocks, snake_case fn names (export name equals the
  Rust name), `///` docs on public items,
  release profile `lto="fat"`, `codegen-units=1`, `panic="abort"`, `strip=true`.
  Deliberate deviation: no `thiserror` (sole-dependency constraint).
- Go: 1.26, sole dependency: `github.com/tetratelabs/wazero`.
- Single static buffer `IN_BUF`: 65536 bytes — input for
  `check_regexp_pattern`, output channel for `version`.

## FFI protocol (the contract)

- Exported functions: `check_regexp_pattern(pattern_len: i32, text_len: i32) -> i32`
  and `version() -> i32`
- Exported memory: `memory`; exported global `IN_BUF` (address of the buffer).
- Buffer layout written by Go: `[0, pattern_len)` = pattern UTF-8 bytes,
  `[pattern_len, pattern_len + text_len)` = text UTF-8 bytes.
- Return codes (`check_regexp_pattern`): `1` match, `0` no match,
  `-1` invalid pattern, `-2` invalid input (negative or oversized length,
  invalid UTF-8).
- `version()` protocol: writes UTF-8 bytes of `env!("CARGO_PKG_VERSION")` at
  `IN_BUF[0..]`, returns byte length `n` (`0 <= n <= 65536`); Go reads
  `IN_BUF[0..n]` back. Shares the buffer with check calls — serialized by the
  Go-side mutex.
- Semantics: `regex::Regex::is_match` — search anywhere, case-sensitive,
  standard regex syntax, empty pattern matches everything.

## Signatures

```rust
// domain
pub struct Pattern { /* compiled regex */ }
impl Pattern {
    pub fn new(source: &str) -> Result<Pattern, InvalidPattern>;
    pub fn matches(&self, text: &str) -> bool;
}
pub struct InvalidPattern;

// application
pub fn check_pattern(pattern: &str, text: &str) -> Result<bool, InvalidPattern>;

// adapters/wasm_ffi
#[unsafe(no_mangle)] pub static mut IN_BUF: [u8; 65536];
#[unsafe(no_mangle)]
pub extern "C" fn check_regexp_pattern(pattern_len: i32, text_len: i32) -> i32;
#[unsafe(no_mangle)]
pub extern "C" fn version() -> i32; // length of version bytes written into IN_BUF
```

```go
// Go package wasmregexp
func New(ctx context.Context, wasmPath string) (*Checker, error) // errors if any required export is missing
func (c *Checker) CheckRegexpPattern(pattern, text string) (bool, error)
func (c *Checker) Version() (string, error)
func (c *Checker) Close(ctx context.Context) error
```

## What's left to the builder

- Confirm the exact `.wasm` filename cargo emits (Go side discovers it by
  globbing `target/wasm32-unknown-unknown/release/*.wasm`, requiring exactly 1).
- Confirm `IN_BUF` surfaces as an exported global; if the linker does not
  export it, document the deviation and adapt the Go lookup.
- Makefile targets, README copy, Go module path (`wasmhost`).
