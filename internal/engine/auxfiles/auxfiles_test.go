package auxfiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDirFromPathsStreamsFiles(t *testing.T) {
	srcDir := t.TempDir()
	outDir := t.TempDir()

	txt := filepath.Join(srcDir, "DESC.TXT")
	png := filepath.Join(srcDir, "PIC.PNG")
	if err := os.WriteFile(txt, []byte("description"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(png, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := WriteDirFromPaths(outDir, map[string]string{
		"DESC.TXT": txt,
		"PIC.PNG":  png,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("written=%d, want 2", n)
	}
	if got, err := os.ReadFile(filepath.Join(outDir, "DESC.TXT")); err != nil || string(got) != "description" {
		t.Fatalf("DESC.TXT=%q err=%v", got, err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, IndexName))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if man.Files["DESC.TXT"].Stored != "DESC.TXT" {
		t.Fatalf("manifest DESC.TXT=%+v", man.Files["DESC.TXT"])
	}
	if man.Files["PIC.PNG"].Type != "image/png" {
		t.Fatalf("manifest PIC.PNG=%+v", man.Files["PIC.PNG"])
	}
}
