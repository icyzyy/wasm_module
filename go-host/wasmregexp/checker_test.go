// Package wasmregexp hosts the Rust wasm_module crate via wazero and exposes
// an idiomatic Go API for regexp matching against it.
package wasmregexp

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// newChecker builds a Checker against the crate's release .wasm artifact,
// / using the same discovery helper the demo (Task 6) will rely on.
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

	v, err := c.Version()
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

	// Clobber IN_BUF with a match request first; the shared buffer must not
	// corrupt the version string written afterwards.
	if _, err := c.CheckRegexpPattern("^ab.*", "abcdef"); err != nil {
		t.Fatalf("CheckRegexpPattern: %v", err)
	}

	v, err := c.Version()
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	if !semverRe.MatchString(v) {
		t.Errorf("Version() after buffer reuse = %q, want ^\\d+.\\d+.\\d+$ semver", v)
	}
}

func TestCheckRegexpPattern_Table(t *testing.T) {
	c := newChecker(t)

	cases := []struct {
		name    string
		pattern string
		text    string
		want    bool
		wantErr string // substring of the error message; "" = expect no error
	}{
		{"anchored_prefix_match", "^ab.*", "abcdef", true, ""},
		{"anchored_prefix_no_match", "^ab.*", "xyz", false, ""},
		{"metachars_are_regex_not_literal", "a.c", "abc", true, ""},
		{"empty_pattern_matches", "", "anything", true, ""},
		{"case_sensitive", "ABC", "abc", false, ""},
		{"invalid_pattern", "[[", "x", false, "invalid pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.CheckRegexpPattern(tc.pattern, tc.text)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckRegexpPattern(%q, %q): %v", tc.pattern, tc.text, err)
				}
				if got != tc.want {
					t.Errorf("CheckRegexpPattern(%q, %q) = %v, want %v",
						tc.pattern, tc.text, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckRegexpPattern(%q, %q) = %v, want error containing %q",
					tc.pattern, tc.text, got, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("CheckRegexpPattern(%q, %q) error = %q, want substring %q",
					tc.pattern, tc.text, err, tc.wantErr)
			}
		})
	}
}

func TestCheckRegexpPattern_InputTooLarge(t *testing.T) {
	c := newChecker(t)

	text := strings.Repeat("a", 70000)
	got, err := c.CheckRegexpPattern("a", text)
	if err == nil {
		t.Errorf("CheckRegexpPattern with %d-byte text = %v, want error", len(text), got)
	}
	if got {
		t.Error("CheckRegexpPattern reported a match for an oversized input")
	}
}

func TestCheckRegexpPattern_InvalidUTF8(t *testing.T) {
	c := newChecker(t)

	text := string([]byte{0xff}) // not valid UTF-8
	got, err := c.CheckRegexpPattern("a", text)
	if err == nil {
		t.Errorf("CheckRegexpPattern with invalid UTF-8 text = %v, want error", got)
	}
	if got {
		t.Error("CheckRegexpPattern reported a match for invalid UTF-8 text")
	}
}

func TestCheckRegexpPattern_Concurrent(t *testing.T) {
	c := newChecker(t)

	cases := []struct {
		pattern string
		text    string
		want    bool
		wantErr string
	}{
		{"^ab.*", "abcdef", true, ""},
		{"^ab.*", "xyz", false, ""},
		{"a.c", "abc", true, ""},
		{"^go.*", "gopher", true, ""},
		{"ABC", "abcd", false, ""},
		{"[[", "x", false, "invalid pattern"},
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
				got, err := c.CheckRegexpPattern(tc.pattern, tc.text)
				if tc.wantErr == "" {
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
				if err == nil {
					errCh <- fmt.Errorf("goroutine %d: CheckRegexpPattern(%q, %q) = %v, want error",
						i, tc.pattern, tc.text, got)
					return
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					errCh <- fmt.Errorf("goroutine %d: CheckRegexpPattern(%q, %q) error = %q, want substring %q",
						i, tc.pattern, tc.text, err, tc.wantErr)
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
