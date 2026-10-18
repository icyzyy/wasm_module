// Package wasmregexp hosts the Rust wasm_module crate in-process via wazero
// and exposes an idiomatic Go API: regexp matching and crate version, backed
// by the module's shared 65536-byte input buffer.
package wasmregexp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// bufSize is the size in bytes of the shared input buffer exported by the
// wasm module as the global IN_BUF. Must stay in sync with the crate's
// wasm_ffi::IN_BUF_SIZE (65536).
const bufSize = 65536

// The four exports the wasm module must provide; absence of any is a
// construction error reported by New.
const (
	exportCheck = "check_regexp_pattern"
	exportVer   = "version"
	exportMem   = "memory"
	exportBuf   = "IN_BUF"
)

// Checker drives the wasm_module crate via wazero. It is safe for concurrent
// use: both exports share one input buffer, so every call is serialized on
// Checker.mu. Call CheckRegexpPattern or Version, and Close exactly once when
// done. Construct with New.
type Checker struct {
	runtime   wazero.Runtime
	module    api.Module
	checkFn   api.Function
	versionFn api.Function
	mem       api.Memory
	bufPtr    uint64
	mu        sync.Mutex
}

// FindWASM locates the crate's compiled module inside releaseDir: the single
// file matching `*.wasm`. It reports an error when nothing matches exactly
// one file.
func FindWASM(releaseDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(releaseDir, "*.wasm"))
	if err != nil {
		return "", fmt.Errorf("glob %s: %w", filepath.Join(releaseDir, "*.wasm"), err)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no *.wasm files found in %s", releaseDir)
	default:
		return "", fmt.Errorf("expected exactly one *.wasm file in %s, found %d: %v",
			releaseDir, len(matches), matches)
	}
}

// New loads the wasm module at wasmPath into a fresh wazero runtime and
// resolves its required exports (two functions, memory, IN_BUF).
//
// # Errors
//
// Reports an error if the module file cannot be read or compiled, if any
// required export is missing, or if the buffer implied by IN_BUF does not fit
// the exported memory. The runtime is fully released on error.
func New(ctx context.Context, wasmPath string) (*Checker, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read wasm module: %w", err)
	}
	runtime := wazero.NewRuntime(ctx)
	module, err := runtime.InstantiateWithConfig(ctx, wasmBytes, wazero.NewModuleConfig())
	if err != nil {
		runtime.Close(ctx) // no leaked runtime on compile/instantiate failure
		return nil, fmt.Errorf("instantiate %s: %w", wasmPath, err)
	}
	c := &Checker{runtime: runtime, module: module}
	if err := c.resolveExports(); err != nil {
		_ = c.closeModules(ctx)
		return nil, err
	}
	return c, nil
}

// resolveExports populates the Checker's export fields, failing with a
// descriptive error if any of the four required exports is missing or the
// buffer does not fit the exported memory.
func (c *Checker) resolveExports() error {
	c.checkFn = c.module.ExportedFunction(exportCheck)
	if c.checkFn == nil {
		return missingExport(exportCheck)
	}
	c.versionFn = c.module.ExportedFunction(exportVer)
	if c.versionFn == nil {
		return missingExport(exportVer)
	}
	c.mem = c.module.Memory()
	if c.mem == nil {
		return missingExport(exportMem)
	}
	global := c.module.ExportedGlobal(exportBuf)
	if global == nil {
		return missingExport(exportBuf)
	}
	c.bufPtr = global.Get()
	if uint64(c.mem.Size()) < c.bufPtr+bufSize {
		return fmt.Errorf("%s memory is %d bytes, too small for the %d-byte buffer at %s address %d",
			exportMem, c.mem.Size(), bufSize, exportBuf, c.bufPtr)
	}
	return nil
}

// missingExport describes which required wasm export could not be resolved.
func missingExport(name string) error {
	return fmt.Errorf("wasm module missing required export %q", name)
}

// CheckRegexpPattern reports whether text matches the regular expression
// pattern, as compiled by the wasm module's regex engine.
//
// # Errors
//
// Reports "invalid pattern" if pattern is not a syntactically valid regular
// expression, "invalid input: too large or not valid UTF-8" if either input
// is malformed or does not fit the module's 65536-byte input buffer, and an
// error for any unexpected wasm result.
func (c *Checker) CheckRegexpPattern(pattern, text string) (bool, error) {
	ctx := context.Background()
	c.mu.Lock()
	defer c.mu.Unlock()

	// The wasm module rejects the same condition (a negative length cannot
	// occur here); failing fast keeps the buffer writes in bounds.
	if len(pattern)+len(text) > bufSize {
		return false, errors.New("invalid input: too large or not valid UTF-8")
	}

	// Buffer layout per the wasm ABI: pattern bytes, then the text bytes.
	if ok := c.mem.Write(uint32(c.bufPtr), []byte(pattern)); !ok {
		return false, fmt.Errorf("write %d pattern bytes at offset %d into wasm memory failed",
			len(pattern), c.bufPtr)
	}
	if ok := c.mem.Write(uint32(c.bufPtr)+uint32(len(pattern)), []byte(text)); !ok {
		return false, fmt.Errorf("write %d text bytes at offset %d into wasm memory failed",
			len(text), c.bufPtr+uint64(len(pattern)))
	}

	res, err := c.checkFn.Call(ctx, uint64(len(pattern)), uint64(len(text)))
	if err != nil {
		return false, fmt.Errorf("call check_regexp_pattern: %w", err)
	}
	return matchResult(res)
}

// matchResult maps the wasm call's result codes to the Go API.
func matchResult(res []uint64) (bool, error) {
	if len(res) == 0 {
		return false, errors.New("check_regexp_pattern returned no result")
	}
	switch code := int32(res[0]); code {
	case 1:
		return true, nil
	case 0:
		return false, nil
	case -1:
		return false, errors.New("invalid pattern")
	case -2:
		return false, errors.New("invalid input: too large or not valid UTF-8")
	default:
		return false, fmt.Errorf("check_regexp_pattern returned unexpected result %d", code)
	}
}

// Version returns the wasm module crate's version string, e.g. "0.1.0".
//
// # Errors
//
// Reports an error if the wasm call fails or the returned length is outside
// the input buffer's bounds.
func (c *Checker) Version() (string, error) {
	ctx := context.Background()
	c.mu.Lock()
	defer c.mu.Unlock()

	res, err := c.versionFn.Call(ctx)
	if err != nil {
		return "", fmt.Errorf("call version: %w", err)
	}
	if len(res) == 0 {
		return "", errors.New("version returned no result")
	}
	n := int64(int32(res[0]))
	if n < 0 || n > bufSize {
		return "", fmt.Errorf("version returned invalid length %d", n)
	}
	bytes, ok := c.mem.Read(uint32(c.bufPtr), uint32(n))
	if !ok {
		return "", fmt.Errorf("read %d bytes at offset %d from wasm memory failed", n, c.bufPtr)
	}
	return string(bytes), nil
}

// Close releases the wasm module and its runtime. It is safe to call on a
// Checker at any point after New, and flushing no calls — Close simply stops
// using the module afterwards.
//
// # Errors
//
// Reports an error if closing the module or runtime fails.
func (c *Checker) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeModules(ctx)
}

// closeModules closes the module then the runtime, joining any errors.
func (c *Checker) closeModules(ctx context.Context) error {
	moduleErr := c.module.Close(ctx)
	runtimeErr := c.runtime.Close(ctx)
	return errors.Join(moduleErr, runtimeErr)
}
