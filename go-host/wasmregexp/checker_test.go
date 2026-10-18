package wasmregexp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// newChecker builds a Checker against the crate's release .wasm artifact,
// using the same discovery helper the demo relies on.
func newChecker(t *testing.T) *Checker {
	t.Helper()
	ctx := context.Background()
	releaseDir := filepath.Join("..", "..", "target", "wasm32-unknown-unknown", "release")
	path, err := FindWASM(releaseDir)
	if err != nil {
		t.Fatalf("FindWASM(%s): %v", releaseDir, err)
	}
	c, err := New(ctx, path)
	if err != nil {
		t.Fatalf("New(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	return c
}

// semverRe matches a plain MAJOR.MINOR.PATCH version string, e.g. "0.1.0".
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func TestNew_ResolvesRequiredExports(t *testing.T) {
	c := newChecker(t)

	if c.checkFn == nil {
		t.Error("check_regexp_pattern export was not resolved")
	}
	if c.versionFn == nil {
		t.Error("version export was not resolved")
	}
	if c.mem == nil {
		t.Fatal("memory export was not resolved")
	}
	if uint64(c.mem.Size()) < c.bufPtr+bufSize {
		t.Errorf("memory size %d too small for %d-byte buffer at IN_BUF address %d",
			c.mem.Size(), bufSize, c.bufPtr)
	}
}

func TestVersion_ReturnsCrateSemver(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	v, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	if v == "" {
		t.Error("Version() returned an empty string")
	}
	if !semverRe.MatchString(v) {
		t.Errorf("Version() = %q, want a non-empty ^\\d+.\\d+.\\d+$ semver", v)
	}
}

func TestVersion_AfterBufferReuse(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	// Clobber IN_BUF with a match request first; the shared buffer must not
	// corrupt the version string written afterwards.
	if _, err := c.CheckRegexpPattern(ctx, "^ab.*", "abcdef"); err != nil {
		t.Fatalf("CheckRegexpPattern: %v", err)
	}

	v, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	if !semverRe.MatchString(v) {
		t.Errorf("Version() after buffer reuse = %q, want ^\\d+.\\d+.\\d+$ semver", v)
	}
}

func TestCheckRegexpPattern_Table(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	cases := []struct {
		name    string
		pattern string
		text    string
		want    bool
		wantIs  error // sentinel the error must satisfy; nil = expect no error
	}{
		{"anchored_prefix_match", "^ab.*", "abcdef", true, nil},
		{"anchored_prefix_no_match", "^ab.*", "xyz", false, nil},
		{"metachars_are_regex_not_literal", "a.c", "abc", true, nil},
		{"empty_pattern_matches", "", "anything", true, nil},
		{"case_sensitive", "ABC", "abc", false, nil},
		{"invalid_pattern", "[[", "x", false, ErrInvalidPattern},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.CheckRegexpPattern(ctx, tc.pattern, tc.text)
			if tc.wantIs == nil {
				if err != nil {
					t.Fatalf("CheckRegexpPattern(%q, %q): %v", tc.pattern, tc.text, err)
				}
				if got != tc.want {
					t.Errorf("CheckRegexpPattern(%q, %q) = %v, want %v",
						tc.pattern, tc.text, got, tc.want)
				}
				return
			}
			if !errors.Is(err, tc.wantIs) {
				t.Errorf("CheckRegexpPattern(%q, %q) error = %v, want errors.Is(%v)",
					tc.pattern, tc.text, err, tc.wantIs)
			}
		})
	}
}

func TestCheckRegexpPattern_InputTooLarge(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	text := strings.Repeat("a", 70000)
	got, err := c.CheckRegexpPattern(ctx, "a", text)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("CheckRegexpPattern with %d-byte text error = %v, want errors.Is(ErrInvalidInput)", len(text), err)
	}
	if got {
		t.Error("CheckRegexpPattern reported a match for an oversized input")
	}
}

func TestCheckRegexpPattern_InvalidUTF8(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	text := string([]byte{0xff}) // not valid UTF-8
	got, err := c.CheckRegexpPattern(ctx, "a", text)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("CheckRegexpPattern with invalid UTF-8 text error = %v, want errors.Is(ErrInvalidInput)", err)
	}
	if got {
		t.Error("CheckRegexpPattern reported a match for invalid UTF-8 text")
	}
}

// TestCheckRegexpPattern_ExactBufferSizeAccepted pins the off-by-one
// direction of the buffer contract: exactly bufSize total bytes must be
// accepted (one byte more is rejected by TestCheckRegexpPattern_InputTooLarge).
func TestCheckRegexpPattern_ExactBufferSizeAccepted(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	pattern := "a"
	text := strings.Repeat("a", bufSize-len(pattern))
	if len(pattern)+len(text) != bufSize {
		t.Fatalf("test setup: input is %d bytes, want exactly %d", len(pattern)+len(text), bufSize)
	}
	got, err := c.CheckRegexpPattern(ctx, pattern, text)
	if err != nil {
		t.Fatalf("CheckRegexpPattern with exactly %d-byte input: %v", bufSize, err)
	}
	if !got {
		t.Error("CheckRegexpPattern with exactly bufSize input = false, want true")
	}
}

func TestCheckRegexpPattern_Concurrent(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	cases := []struct {
		pattern string
		text    string
		want    bool
		wantIs  error
	}{
		{"^ab.*", "abcdef", true, nil},
		{"^ab.*", "xyz", false, nil},
		{"a.c", "abc", true, nil},
		{"^go.*", "gopher", true, nil},
		{"ABC", "abcd", false, nil},
		{"[[", "x", false, ErrInvalidPattern},
	}
	const goroutines = 50
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range cases {
				tc := cases[(i+j)%len(cases)]
				got, err := c.CheckRegexpPattern(ctx, tc.pattern, tc.text)
				if tc.wantIs == nil {
					switch {
					case err != nil:
						errCh <- fmt.Errorf("goroutine %d: pattern %q: %w", i, tc.pattern, err)
						return
					case got != tc.want:
						errCh <- fmt.Errorf("goroutine %d: CheckRegexpPattern(%q, %q) = %v, want %v",
							i, tc.pattern, tc.text, got, tc.want)
						return
					}
					continue
				}
				if !errors.Is(err, tc.wantIs) {
					errCh <- fmt.Errorf("goroutine %d: CheckRegexpPattern(%q, %q) error = %v, want errors.Is(%v)",
						i, tc.pattern, tc.text, err, tc.wantIs)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestChecker_AfterClose_StableError verifies that calling methods on a
// closed Checker fails with the ErrClosed sentinel rather than a
// wazero-internal error.
func TestChecker_AfterClose_StableError(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := c.CheckRegexpPattern(ctx, "a", "b"); !errors.Is(err, ErrClosed) {
		t.Errorf("CheckRegexpPattern after Close error = %v, want errors.Is(ErrClosed)", err)
	}
	if _, err := c.Version(ctx); !errors.Is(err, ErrClosed) {
		t.Errorf("Version after Close error = %v, want errors.Is(ErrClosed)", err)
	}
}

// TestChecker_DoubleCloseIdempotent verifies that a second Close is a no-op
// returning nil (t.Cleanup in this file itself double-closes in the tests
// above, so this invariant also protects the suite).
func TestChecker_DoubleCloseIdempotent(t *testing.T) {
	c := newChecker(t)
	ctx := t.Context()

	if err := c.Close(ctx); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Errorf("second Close() = %v, want nil (idempotent)", err)
	}
}
