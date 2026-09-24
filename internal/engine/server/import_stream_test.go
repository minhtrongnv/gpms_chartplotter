package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStageExchangeSetZipFiltersAndStreamsToDisk(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, dir, dir, false, "")
	defer s.Close()

	zipPath := filepath.Join(dir, "exchange.zip")
	z := makeZip(t, map[string][]byte{
		"ENC_ROOT/US5MD1MC/US5MD1MC.000": []byte("base-a"),
		"ENC_ROOT/US5MD1MC/US5MD1MC.001": []byte("update-a"),
		"ENC_ROOT/US4VA50M/US4VA50M.000": []byte("base-b"),
		"ENC_ROOT/US5MD1MC/PIC01.PNG":     []byte("picture"),
	})
	if err := os.WriteFile(zipPath, z, 0o644); err != nil {
		t.Fatal(err)
	}

	stage, err := s.stageExchangeSetZip(
		context.Background(),
		zipPath,
		"user",
		[]string{"US5MD1MC"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage.dir)

	if len(stage.stems) != 1 || stage.stems[0] != "US5MD1MC" {
		t.Fatalf("stems=%v, want [US5MD1MC]", stage.stems)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, "US5MD1MC.000")); err != nil {
		t.Fatalf("selected base missing from staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, "US5MD1MC.001")); !os.IsNotExist(err) {
		t.Fatalf("updates=false should exclude .001, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, "US4VA50M.000")); !os.IsNotExist(err) {
		t.Fatalf("name filter should exclude second cell, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stage.dir, "PIC01.PNG")); err != nil {
		t.Fatalf("aux file missing from staging: %v", err)
	}

	if err := s.commitStagedExchangeSet(
		context.Background(),
		"user",
		"d1",
		stage,
	); err != nil {
		t.Fatal(err)
	}
	dest := s.districtDir("user", "d1")
	if got, err := os.ReadFile(filepath.Join(dest, "US5MD1MC.000")); err != nil || string(got) != "base-a" {
		t.Fatalf("committed base=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "US5MD1MC.001")); !os.IsNotExist(err) {
		t.Fatalf("committed district contains excluded update: %v", err)
	}
}

func TestCommitStagedExchangeSetRemovesStaleUpdates(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, dir, dir, false, "")
	defer s.Close()

	dest := s.districtDir("user", "d2")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "US5MD1MC.001"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratch, err := s.importScratchDir("user")
	if err != nil {
		t.Fatal(err)
	}
	stageDir, err := os.MkdirTemp(scratch, "stage-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stageDir)
	if err := os.WriteFile(filepath.Join(stageDir, "US5MD1MC.000"), []byte("new-base"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.commitStagedExchangeSet(
		context.Background(),
		"user",
		"d2",
		stagedExchangeSet{dir: stageDir, stems: []string{"US5MD1MC"}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "US5MD1MC.001")); !os.IsNotExist(err) {
		t.Fatalf("stale update survived replacement: %v", err)
	}
}

func TestCloseCancelsTrackedImportJob(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, dir, dir, false, "")

	started := make(chan struct{})
	job, ok := s.startImportJob("test", func(ctx context.Context, _ string) {
		close(started)
		<-ctx.Done()
	})
	if !ok {
		t.Fatal("failed to start tracked import job")
	}
	<-started

	done := make(chan error, 1)
	go func() { done <- s.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Close did not cancel/wait for import job")
	}

	got, ok := s.imports.snapshot(job.ID)
	if !ok {
		t.Fatal("tracked import job disappeared")
	}
	if got.State != "error" || got.Err != "server shutting down" {
		t.Fatalf("job after shutdown = state %q error %q", got.State, got.Err)
	}
}
