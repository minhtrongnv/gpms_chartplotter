package server

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveBakeWorkersCapsOverride(t *testing.T) {
	t.Setenv("CHARTPLOTTER_BAKE_WORKERS", "9999")
	if got := liveBakeWorkers(); got != maxLiveBakeWorkers {
		t.Fatalf("liveBakeWorkers()=%d, want cap %d", got, maxLiveBakeWorkers)
	}
}

func TestSHA256File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.pmtiles")
	body := []byte("stream this archive instead of reading it all into RAM")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(body)
	want := hex.EncodeToString(wantSum[:])
	got, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("sha256File()=%q, want %q", got, want)
	}
}
