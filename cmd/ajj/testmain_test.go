package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// Jujutsu reports physical workspace paths. Keep fixture expectations in
	// that form too on macOS, where /tmp and /var are symlinks. Tests of aliased
	// workspace paths create explicit symlinks independently of TMPDIR.
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err == nil {
		err = os.Setenv("TMPDIR", tmp)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve test temporary directory:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
