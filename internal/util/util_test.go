package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAuthDirDefaultsEmptyPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("user home unavailable: %v", err)
	}

	got, err := ResolveAuthDir("")
	if err != nil {
		t.Fatalf("ResolveAuthDir(\"\") error = %v", err)
	}

	want := filepath.Clean(filepath.Join(home, ".cli-proxy-api"))
	if got != want {
		t.Fatalf("ResolveAuthDir(\"\") = %q, want %q", got, want)
	}
}
