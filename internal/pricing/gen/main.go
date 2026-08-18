// Command gen refreshes the embedded price catalog from models.dev.
//
// Run it with `make catalog` and commit the result. It is a build-time tool,
// never linked into the gateway: the binary reads the snapshot it writes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/qunqin24/polyglot/internal/pricing"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
}

func run() error {
	snap, err := pricing.Fetch(context.Background())
	if err != nil {
		return err
	}

	// Written next to the package that embeds it, found relative to this
	// source file so the command works from any working directory.
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("cannot locate the pricing package")
	}
	out := filepath.Join(filepath.Dir(filepath.Dir(self)), "snapshot.json")

	// Indented and newline-terminated: this file is reviewed in diffs, so a
	// price change should show as one line rather than one enormous one.
	body, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s — %d models, version %s\n", out, len(snap.Entries), snap.Version)
	return nil
}
