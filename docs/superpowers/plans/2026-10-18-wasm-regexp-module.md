# Rust Wasm Regexp Module (Go/wazero host) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Rust `cdylib` Wasm module with clean-architecture layers exposing exactly two functions — `check_regexp_pattern` (the operation) and `version` (module identity) — plus a Go/wazero host that calls them as `CheckRegexpPattern(pattern, text string) (bool, error)` and `Version() (string, error)`.

**Architecture:** Rust crate split into `domain` (compiled-regex entity), `application` (the check use case), and `adapters/wasm_ffi` (static 64 KiB buffer, the single `extern "C"` export, i32 code mapping). Go side keeps all wazero plumbing in one `wasmregexp` package; callers see only strings, bools, and errors. Dependency rule: `adapters → application → domain`, never backward.

**Tech Stack:** Rust stable (cargo 1.98, edition 2024), target `wasm32-unknown-unknown`, `regex` crate; Go 1.26 with `github.com/tetratelabs/wazero`; Makefile.

**Spec:** `docs/superpowers/specs/2026-10-18-wasm-regexp-design.md`

## Global Constraints

- Exactly two exported Wasm **functions**: `check_regexp_pattern(i32, i32) -> i32` and `version() -> i32` (snake_case: Wasm export names are the Rust names — no `export_name` indirection; each layer's naming follows its own ecosystem, Go's public methods are `CheckRegexpPattern` and `Version`). Exports `memory` and global `IN_BUF` are permitted plumbing; no other function exports, no WASI imports, no `wasm-bindgen`.
- `version()` contract: writes the UTF-8 bytes of `env!("CARGO_PKG_VERSION")` (source of truth: `Cargo.toml`, default `0.1.0`) into `IN_BUF` at offset 0 and returns the byte length (always `0 <= n <= 65536`). It shares `IN_BUF` with `check_regexp_pattern` — calls serialize on the Go side's mutex.
- Sole dependencies: Rust `regex`, Go `github.com/tetratelabs/wazero`. No TinyGo — standard Go toolchain.
- Input buffer: 65536 bytes, layout `[0, pattern_len)` pattern, `[pattern_len, pattern_len+text_len)` text.
- Return codes: `1` match, `0` no match, `-1` invalid pattern, `-2` invalid input (negative/oversized length, invalid UTF-8).
- Match semantics: `is_match` (search anywhere), case-sensitive, empty pattern matches everything.
- Go API name: `CheckRegexpPattern(pattern, text string) (bool, error)` (exported).
- Clean-architecture rule: nothing in `domain` or `application` may reference Wasm, buffers, pointers, or i32 codes.
- Rust-skills compliance: edition-2024 unsafe attribute forms (`#[unsafe(no_mangle)]`); `// SAFETY:` comment above every `unsafe` block; snake_case fn names all the way through, including the Wasm export; `///` doc comments on all public items; lowercase `Display` messages (e.g. `invalid pattern`). Deliberate deviation: no `thiserror` — spec pins `regex` as the sole Rust dependency.

- Commit format (commit-code skill): every step's `git commit -m "..."` in this plan is only the **subject**; the actual command appends the required footer, resolved from git config at commit time:
  ```bash
  git commit -m "$(printf '%s\n\nOn-behalf-of: %s <%s>' "<subject from step>" "$(git config user.name)" "$(git config user.email)")"
  ```
  Run `git diff` and read it before committing; pick the type from the diff. Git config on this machine: `Pantakan Chusri-eam <pantakan.c@ku.th>` (verified 2026-10-18).

## Review Focus

1. **Oversized / negative lengths** — must return `-2`, never panic or read out of bounds. → Rust protocol-matrix test (Task 3, cases `-2`), Go too-large test (Task 5, `TestCheckRegexpPattern_InputTooLarge`).
2. **Invalid UTF-8 bytes** (Go strings are byte slices) — must return a Go error, never panic. → Go `TestCheckRegexpPattern_InvalidUTF8` (Task 5).
3. **`IN_BUF` may not surface as an exported global** (linker-dependent) — must fail loudly, not silently. → `New()` errors if function/memory/global missing; Task 5 `TestNew_ResolvesRequiredExports` asserts `New` succeeds against the real `.wasm` built in Task 4.
4. **Concurrent calls share one static buffer** — results must stay correct under goroutines. → `sync.Mutex` in `Checker`, `TestCheckRegexpPattern_Concurrent` (Task 5).
5. **Semantics pinning** — empty pattern → `true`, case-sensitive matching. → domain test (Task 1), application test (Task 2), Go table cases (Task 5).
6. **`version()` reads back a buffer other calls clobber** — `Version()` must return the correct version string even immediately after `CheckRegexpPattern` calls, and `version()` must never return a length the Go side would read out of bounds. → Rust protocol-matrix version case (Task 3), Go `TestVersion_AfterBufferReuse` + `TestVersion_ReturnsCrateSemver` (Task 5).

---

### Task 1: Scaffold + domain layer

**Files:**
- Create: `Cargo.toml`, `src/lib.rs`, `src/domain/mod.rs`, `src/domain/pattern.rs`
- Test: `src/domain/pattern.rs` (`#[cfg(test)] mod tests`)

**Interfaces:**
- Produces: `domain::Pattern::new(&str) -> Result<Pattern, InvalidPattern>`, `Pattern::matches(&self, &str) -> bool`, `domain::InvalidPattern` (implements `std::fmt::Display`). `src/lib.rs` declares `pub mod domain;`.

- [ ] **Step 1: Init repo and crate**

```bash
git init
cargo init --lib --name wasm_module
```

- [ ] **Step 2: Pin crate config in `Cargo.toml`**

Set `edition = "2024"` and keep `version = "0.1.0"` (single source of truth for the `version()` export, read via `env!("CARGO_PKG_VERSION")`), add:

```toml
[lib]
crate-type = ["cdylib", "rlib"]

[dependencies]
regex = "1"

[profile.release]
opt-level = 3
lto = "fat"
codegen-units = 1
panic = "abort"
strip = true
```

Add `.gitignore` containing `/target/`.

- [ ] **Step 3: Write the failing domain test** in `src/domain/pattern.rs`

```rust
#[test]
fn matches_simple_prefix() {
    let p = Pattern::new("^ab.*").unwrap();
    assert!(p.matches("abcdef"));
    assert!(!p.matches("xyz"));
}
#[test]
fn invalid_pattern_is_rejected() {
    assert!(Pattern::new("[[").is_err());
}
#[test]
fn empty_pattern_matches_everything() {
    assert!(Pattern::new("").unwrap().matches("anything"));
}
#[test]
fn matching_is_case_sensitive() {
    let p = Pattern::new("ABC").unwrap();
    assert!(!p.matches("abc"));
}
```

(`src/domain/mod.rs` and the `pub mod domain;` line in `lib.rs` come with this step; `Pattern`/`InvalidPattern` do not exist yet.)

- [ ] **Step 4: Run test, verify compile failure**

Run: `cargo test`
Expected: FAIL — cannot find `Pattern` in scope / unresolved.

- [ ] **Step 5: Implement `Pattern` in `src/domain/pattern.rs`**

Struct wrapping `regex::Regex`; `new` maps `regex::Error` to unit struct `InvalidPattern` with `#[derive(Debug, PartialEq, Eq, Clone, Copy)]` and a hand-rolled `Display` whose message is lowercase (`"invalid pattern"` — no `thiserror`, sole-dep constraint); `matches` delegates to `re.is_match(text)`. `///` doc comments on `Pattern`, `new`, `matches`, `InvalidPattern`.

- [ ] **Step 6: Run test, verify PASS**

Run: `cargo test`
Expected: 4 domain tests pass.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "feat: domain layer with Pattern entity"
```

---

### Task 2: Application layer

**Files:**
- Create: `src/application/mod.rs`, `src/application/check_pattern.rs`
- Modify: `src/lib.rs` (add `pub mod application;`)
- Test: `src/application/check_pattern.rs` (`#[cfg(test)] mod tests`)

**Interfaces:**
- Consumes: `domain::{Pattern, InvalidPattern}` (Task 1).
- Produces: `application::check_pattern(pattern: &str, text: &str) -> Result<bool, InvalidPattern>`.

- [ ] **Step 1: Write the failing test** in `src/application/check_pattern.rs`

```rust
use super::check_pattern;
#[test]
fn delegates_to_domain_and_returns_bool() {
    assert_eq!(check_pattern("^ab.*", "abcdef"), Ok(true));
    assert_eq!(check_pattern("^ab.*", "xyz"), Ok(false));
}
#[test]
fn propagates_invalid_pattern() {
    assert!(check_pattern("[[", "x").is_err());
}
```

Also add `pub mod application;` to `src/lib.rs` and create `application/mod.rs` re-exporting `pub mod check_pattern; pub use check_pattern::check_pattern;`.

- [ ] **Step 2: Run test, verify compile failure**

Run: `cargo test`
Expected: FAIL — `check_pattern` not found.

- [ ] **Step 3: Implement `check_pattern(pattern, text) -> Result<bool, InvalidPattern>`**

Body: `Pattern::new(pattern).map(|p| p.matches(text))`. One line — the use case exists to keep the FFI layer from calling the domain directly. The test's `assert_eq!` on `Result` requires `#[derive(Debug, PartialEq, Eq)]` on `domain::InvalidPattern` — add it there if Task 1 didn't already.

- [ ] **Step 4: Run test, verify PASS**

Run: `cargo test`
Expected: all tests pass (6 total).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: application layer with check_pattern use case"
```

---

### Task 3: Wasm adapter (buffer + single export)

**Files:**
- Create: `src/adapters/mod.rs`, `src/adapters/wasm_ffi.rs`
- Modify: `src/lib.rs` (add `pub mod adapters;`)
- Test: `src/adapters/wasm_ffi.rs` (`#[cfg(test)] mod tests`)

**Interfaces:**
- Consumes: `application::check_pattern` (Task 2).
- Produces: export `IN_BUF` (`[u8; 65536]`, `#[unsafe(no_mangle)] pub static mut`) and two Wasm exports: `check_regexp_pattern(i32, i32) -> i32` and `version() -> i32` (both Rust `pub extern "C"` fns marked `#[unsafe(no_mangle)]`; export names equal the Rust names).

- [ ] **Step 1: Write the failing protocol-matrix test** in `src/adapters/wasm_ffi.rs`

Single test fn (the shared `static mut` buffer must not be touched by parallel tests):

```rust
unsafe fn set_input(pattern: &str, text: &str) -> (i32, i32) { /* copy pattern then text into IN_BUF, return (len, len) */ }

#[test]
fn protocol_matrix() {
    unsafe {
        let (p, t) = set_input("^ab.*", "abcdef");
        assert_eq!(check_regexp_pattern(p, t), 1);
        let (p, t) = set_input("^ab.*", "xyz");
        assert_eq!(check_regexp_pattern(p, t), 0);
        let (p, t) = set_input("[[", "x");
        assert_eq!(check_regexp_pattern(p, t), -1);   // invalid pattern
        assert_eq!(check_regexp_pattern(-1, 5), -2);  // negative length
        assert_eq!(check_regexp_pattern(65537, 0), -2); // oversized
        assert_eq!(check_regexp_pattern(65536, 1), -2); // sum > buffer
        let (p, t) = set_input("a", "b");
        assert_eq!(check_regexp_pattern(i32::MAX, t), -2); // no overflow panic
        // version(): writes version bytes into IN_BUF, returns length
        let n = version();
        assert!(n > 0);
        let bytes = /* SAFETY: version() wrote n bytes (n <= IN_BUF_SIZE) at IN_BUF[0..] */
            unsafe { std::slice::from_raw_parts(IN_BUF.as_ptr(), n as usize) };
        assert_eq!(bytes, env!("CARGO_PKG_VERSION").as_bytes());
    }
}
```

Each `unsafe` block (including `set_input`) carries a `// SAFETY:` comment stating why it is sound (bounds validated before copy / lengths validated before slice creation).

Add `pub mod adapters;` to `lib.rs` (`adapters/mod.rs` declares `pub mod wasm_ffi;`).

- [ ] **Step 2: Run test, verify compile failure**

Run: `cargo test`
Expected: FAIL — `check_regexp_pattern` not found.

- [ ] **Step 3: Implement `src/adapters/wasm_ffi.rs`**

- `pub const IN_BUF_SIZE: usize = 65536;` and `pub static mut IN_BUF: [u8; IN_BUF_SIZE]` with `#[unsafe(no_mangle)]`.
- `pub extern "C" fn check_regexp_pattern(pattern_len: i32, text_len: i32) -> i32` with `#[unsafe(no_mangle)]` (export name equals the Rust name — snake_case is the Wasm contract here). `///` doc comment listing all four return codes.
- `pub extern "C" fn version() -> i32` with `#[unsafe(no_mangle)]`: copies `env!("CARGO_PKG_VERSION").as_bytes()` into `IN_BUF` at offset 0 via `addr_of_mut!` and returns its length. `///` doc comment stating the protocol. (Pure adapter metadata — deliberately not in domain/application, which stay about matching.)
- Validation first (order matters): any length `< 0` → `-2`; `pattern_len > 65536 || text_len > 65536 || pattern_len as i64 + text_len as i64 > 65536` → `-2`; `std::str::from_utf8` on both slices fails → `-2`.
- Then `application::check_pattern(pattern, text)`; `Ok(true)` → `1`, `Ok(false)` → `0`, `Err(_)` → `-1`.
- Read slices via `core::ptr::addr_of!(IN_BUF)` and write via `addr_of_mut!` in the test helper (avoids edition-2024 `static_mut_refs` deny); every `unsafe` block gets a `// SAFETY:` comment naming the invariant it relies on (lengths validated before `from_raw_parts`; buffer is 65536 bytes and sum checked before copy).
- Guard: only compile the `extern "C"` machinery unconditionally (it is plain Rust, also valid natively); no `#[cfg(target_arch = "wasm32")]` gating needed.

- [ ] **Step 4: Run test, verify PASS**

Run: `cargo test`
Expected: all tests pass, no warnings about `static_mut_refs`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: wasm adapter with check_regexp_pattern and version exports"
```

---

### Task 4: Build the Wasm artifact

**Files:**
- Create: none in source; artifact `target/wasm32-unknown-unknown/release/*.wasm`

**Interfaces:**
- Produces: the `.wasm` file that Task 5's Go tests glob for.

- [ ] **Step 1: Install target and build**

```bash
rustup target add wasm32-unknown-unknown
cargo build --release --target wasm32-unknown-unknown
```

Expected: build succeeds.

- [ ] **Step 2: Verify exactly one wasm artifact**

Run: `ls target/wasm32-unknown-unknown/release/*.wasm`
Expected: exactly one file (record its name; Go finds it by glob, requiring exactly 1 match).

- [ ] **Step 3: Commit the build config**

```bash
git add -A && git commit -m "build: wasm32-unknown-unknown release artifact" || true
```

(If the artifact is gitignored, commit is a no-op — acceptable.)

---

### Task 5: Go/wazero host package + integration tests

**Files:**
- Create: `go-host/go.mod`, `go-host/wasmregexp/checker.go`, `go-host/wasmregexp/checker_test.go`

**Interfaces:**
- Consumes: exports `check_regexp_pattern`, `version`, `memory`, `IN_BUF` (Task 3/4).
- Produces: `wasmregexp.New(ctx, wasmPath) (*Checker, error)`, `(*Checker).CheckRegexpPattern(pattern, text string) (bool, error)`, `(*Checker).Version() (string, error)`, `(*Checker).Close(ctx) error`. Module path `wasmhost`, package import `wasmhost/wasmregexp`.

- [ ] **Step 1: Init Go module**

```bash
cd go-host && go mod init wasmhost && go get github.com/tetratelabs/wazero@latest
```

- [ ] **Step 2: Write the failing integration tests** in `go-host/wasmregexp/checker_test.go`

Helper `newChecker(t)` that globs `../../target/wasm32-unknown-unknown/release/*.wasm` (require exactly 1), calls `New`, registers `t.Cleanup(Close)`.

```go
func TestNew_ResolvesRequiredExports(t *testing.T)   // New succeeds → both fns, memory, IN_BUF all exist
func TestVersion_ReturnsCrateSemver(t *testing.T)    // Version() matches ^\d+\.\d+\.\d+$ and is non-empty
func TestVersion_AfterBufferReuse(t *testing.T)      // CheckRegexpPattern first (clobbers IN_BUF) → Version still correct
func TestCheckRegexpPattern_Table(t *testing.T) {
    // {"^ab.*", "abcdef", true, nil}
    // {"^ab.*", "xyz", false, nil}
    // {"a.c", "abc", false, nil}      // metachars are regex, not literal
    // {"", "anything", true, nil}     // empty pattern matches
    // {"ABC", "abc", false, nil}      // case-sensitive
    // {"[[", "x", false, error}       // invalid pattern
}
func TestCheckRegexpPattern_InputTooLarge(t *testing.T)  // strings.Repeat("a", 70000) → error, no panic
func TestCheckRegexpPattern_InvalidUTF8(t *testing.T)     // string([]byte{0xff}) as text → error, no panic
func TestCheckRegexpPattern_Concurrent(t *testing.T)      // 50 goroutines × mixed patterns → all correct
```

- [ ] **Step 3: Run tests, verify failure**

Run: `cd go-host && go test ./wasmregexp/`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Implement `go-host/wasmregexp/checker.go`**

- `Checker` struct: `wazero.Runtime`, `api.Module`, two `api.Function`s (`check_regexp_pattern`, `version`), `api.Memory`, `bufPtr uint64` (from exported global `IN_BUF`), `sync.Mutex`.
- `New`: compile + instantiate wasm (no WASI), resolve the four exports — return a descriptive error if any is missing.
- `CheckRegexpPattern`: lock mutex; `patternLen, textLen := len(...)`; write pattern bytes at `bufPtr`, text bytes at `bufPtr+patternLen` via `mem.Write`; `fn.Call(ctx, int64(patternLen), int64(textLen))`; map `1→true, 0→false, -1→error "invalid pattern", -2→error "invalid input: too large or not valid UTF-8"`; unknown code → error.
- `Version`: lock mutex; call `versionFn.Call(ctx)`; validate returned `n` is `0 <= n <= 65536` (else error), `mem.Read(bufPtr, bufPtr+n)`, return `string(bytes)`.
- `Close`: closes module then runtime.

- [ ] **Step 5: Run tests, verify PASS**

Run: `cd go-host && go test -v ./wasmregexp/`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat: Go wazero host with CheckRegexpPattern + Version and tests"
```

---

### Task 6: Demo CLI, Makefile, README

**Files:**
- Create: `go-host/cmd/demo/main.go`, `Makefile`, `README.md`

**Interfaces:**
- Consumes: `wasmregexp.New` / `CheckRegexpPattern` / `Version` (Task 5); wasm artifact glob helper logic (same as tests).

- [ ] **Step 1: Implement `go-host/cmd/demo/main.go`**

Takes two CLI args (pattern, text), finds the `.wasm` via the same glob (or `-wasm` flag override), prints `module version: %s\n` (from `Version()`) then `CheckRegexpPattern(%q, %q) = %v\n` (the Go API name), exit 1 on error. Non-match exits 0 — a result, not an error.

- [ ] **Step 2: Write `Makefile`**

Targets: `build` (cargo release wasm), `test-rust` (`cargo test`), `test-go` (depends on `build`; `cd go-host && go test ./...`), `test` (test-rust + test-go), `demo` (depends on `build`; `cd go-host && go run ./cmd/demo "$(PATTERN)" "$(TEXT)"`).

- [ ] **Step 3: Write `README.md`**

Sections: what this is (clean-architecture Rust Wasm ↔ Go/wazero), directory layout with layer responsibilities, prerequisites (`rustup target add wasm32-unknown-unknown`, Go 1.26+), build/test/demo commands, FFI protocol table (both function signatures, buffer layout, return codes, the `version()` read-back protocol, semantics).

- [ ] **Step 4: Full verification**

Run: `make test`
Expected: Rust tests pass, Go tests pass against freshly built wasm.

Run: `make demo PATTERN='^ab.*' TEXT='abcdef'`
Expected: prints
```
module version: 0.1.0
CheckRegexpPattern("^ab.*", "abcdef") = true
```

Run: `make demo PATTERN='^ab.*' TEXT='xyz'`
Expected: second line `= false`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat: demo CLI, Makefile, README"
```
