// Command demo exercises the wasmregexp package end to end: it loads the
// compiled wasm_module crate, prints its version, and reports whether the
// given text matches the given pattern.
//
// Usage: go run ./cmd/demo [flags] <pattern> <text>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"wasmhost/wasmregexp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// defaultReleaseDir is where cargo places the release build. The Makefile
// runs this command with cwd set to go-host, so the path is relative to it.
const defaultReleaseDir = "../target/wasm32-unknown-unknown/release"

func run() error {
	wasmFlag := flag.String("wasm", "", "path to the compiled wasm module (default: auto-discovered under ../target)")
	flag.Parse()

	args := flag.Args()
	if len(args) != 2 {
		return fmt.Errorf("usage: %s [flags] <pattern> <text>", os.Args[0])
	}
	pattern, text := args[0], args[1]

	wasmPath := *wasmFlag
	if wasmPath == "" {
		var err error
		wasmPath, err = wasmregexp.FindWASM(defaultReleaseDir)
		if err != nil {
			return err
		}
	}

	ctx := context.Background()
	checker, err := wasmregexp.New(ctx, wasmPath)
	if err != nil {
		return err
	}
	defer checker.Close(ctx)

	version, err := checker.Version()
	if err != nil {
		return err
	}

	match, err := checker.CheckRegexpPattern(pattern, text)
	if err != nil {
		return err
	}

	fmt.Printf("module version: %s\n", version)
	fmt.Printf("CheckRegexpPattern(%q, %q) = %v\n", pattern, text, match)
	return nil
}
