package plugin

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// Plugin uploads are already capped at the HTTP layer. These limits bound the
	// *expanded* archive too, so a small compressed zip cannot explode into
	// unbounded CPU, memory, or disk work during verification/install.
	maxPluginArchiveFiles  = 4096
	maxPluginFileBytes     = 64 << 20  // 64 MiB per expanded file
	maxPluginExpandedBytes = 256 << 20 // 256 MiB total expanded payload
)

// install.go verifies and unpacks a plugin archive. A plugin is a zip whose
// plugin.json carries a `files` map of path→sha256 covering every other payload
// file. Signing + TOFU key pinning are Phase 3; v1 does content-hash verification.

// InstallOptions tunes installer policy.
type InstallOptions struct {
	// AllowCore permits ids under the reserved core.* prefix. False for third-party
	// archives (the CLI/UI install path); true only for in-tree tooling.
	AllowCore bool
}

// VerifyArchive opens the zip at path, parses+validates plugin.json, and verifies
// archive structure + hashes. Every payload file must be listed in manifest.files;
// plugin.json is the hash root and plugin.sig is reserved signature metadata.
func VerifyArchive(archivePath string) (*Manifest, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	return verify(&zr.Reader)
}

func verify(zr *zip.Reader) (*Manifest, error) {
	files, err := indexArchiveFiles(zr)
	if err != nil {
		return nil, err
	}

	mf := files["plugin.json"]
	if mf == nil {
		return nil, fmt.Errorf("archive has no plugin.json")
	}

	mb, err := readZipFile(mf, maxPluginFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read plugin.json: %w", err)
	}

	m, err := ParseManifest(mb)
	if err != nil {
		return nil, err
	}

	if err := validateManifestEntryPaths(m); err != nil {
		return nil, err
	}

	// Every manifest hash must name one canonical archive file and match it.
	for rawName, want := range m.Files {
		name, ok := canonicalArchivePath(rawName)
		if !ok || name != rawName || name == "plugin.json" || name == "plugin.sig" {
			return nil, fmt.Errorf("manifest has invalid file path %q", rawName)
		}

		f := files[name]
		if f == nil {
			return nil, fmt.Errorf(
				"manifest lists %q but it is missing from the archive",
				name,
			)
		}

		sum, err := hashZipFile(f, maxPluginFileBytes)
		if err != nil {
			return nil, fmt.Errorf("hash %q: %w", name, err)
		}
		if !hashEqual(sum, want) {
			return nil, fmt.Errorf(
				"hash mismatch for %q: archive has %s, manifest wants %s",
				name,
				sum,
				want,
			)
		}
	}

	// Do not unpack anything that was not authenticated by the manifest. Without
	// this check, an archive could carry an unhashed native/WASM/UI payload even
	// though the installer reported successful verification.
	for name := range files {
		if isPluginMetadata(name) {
			continue
		}
		if _, ok := m.Files[name]; !ok {
			return nil, fmt.Errorf(
				"archive file %q is not listed in manifest.files",
				name,
			)
		}
	}

	return m, nil
}

func validateManifestEntryPaths(m *Manifest) error {
	check := func(label, raw string) error {
		if raw == "" {
			return nil
		}

		name, ok := canonicalArchivePath(raw)
		if !ok || name != raw {
			return fmt.Errorf("%s has unsafe path %q", label, raw)
		}

		if _, ok := m.Files[name]; !ok {
			return fmt.Errorf(
				"%s %q must be listed in manifest.files",
				label,
				name,
			)
		}

		return nil
	}

	if err := check("entry.wasm", m.Entry.WASM); err != nil {
		return err
	}

	for platform, rel := range m.Entry.Native {
		if err := check("entry.native["+platform+"]", rel); err != nil {
			return err
		}
	}

	if m.UI != nil {
		if err := check("ui.entry", m.UI.Entry); err != nil {
			return err
		}
	}

	return nil
}

// Install verifies the archive and unpacks it to <pluginsDir>/<id>/<version>/,
// returning the manifest. It refuses to overwrite an already-unpacked version.
//
// Verification and extraction use the same opened archive to avoid a path-level
// TOCTOU window where the zip could be replaced between those two phases.
func Install(
	archivePath,
	pluginsDir string,
	opts InstallOptions,
) (*Manifest, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	m, err := verify(&zr.Reader)
	if err != nil {
		return nil, err
	}

	if !opts.AllowCore && strings.HasPrefix(m.ID, CorePrefix) {
		return nil, fmt.Errorf(
			"id %q uses the reserved %q prefix",
			m.ID,
			CorePrefix,
		)
	}

	dest := filepath.Join(pluginsDir, m.ID, m.Version)
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf(
			"%s@%s is already installed",
			m.ID,
			m.Version,
		)
	}

	// Unpack into a temp dir then rename, so a failed unpack leaves nothing partial.
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)

	if err := unpack(&zr.Reader, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}

	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}

	return m, nil
}

// indexArchiveFiles validates the zip namespace before any payload is trusted.
// Duplicate canonical/case-folded paths are rejected because "verify first,
// overwrite later" is a classic zip signature/hash bypass, especially across
// Windows/Linux path semantics.
func indexArchiveFiles(zr *zip.Reader) (map[string]*zip.File, error) {
	if len(zr.File) > maxPluginArchiveFiles {
		return nil, fmt.Errorf(
			"archive has too many entries (%d > %d)",
			len(zr.File),
			maxPluginArchiveFiles,
		)
	}

	files := make(map[string]*zip.File, len(zr.File))
	seenFolded := make(map[string]string, len(zr.File))
	var total uint64

	for _, f := range zr.File {
		name, ok := canonicalArchivePath(f.Name)
		if !ok {
			return nil, fmt.Errorf("unsafe archive path %q", f.Name)
		}

		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive symlink is not allowed: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			continue
		}

		folded := strings.ToLower(name)
		if prev, exists := seenFolded[folded]; exists {
			return nil, fmt.Errorf(
				"duplicate archive path %q conflicts with %q",
				name,
				prev,
			)
		}
		seenFolded[folded] = name

		if f.UncompressedSize64 > maxPluginFileBytes {
			return nil, fmt.Errorf(
				"archive file %q exceeds expanded limit (%d > %d bytes)",
				name,
				f.UncompressedSize64,
				maxPluginFileBytes,
			)
		}

		if total > maxPluginExpandedBytes-f.UncompressedSize64 {
			return nil, fmt.Errorf(
				"archive expanded size exceeds %d bytes",
				maxPluginExpandedBytes,
			)
		}
		total += f.UncompressedSize64

		files[name] = f
	}

	return files, nil
}

// unpack writes every verified archive file under dest, rejecting traversal and
// re-checking actual decompressed byte counts rather than trusting zip metadata.
func unpack(zr *zip.Reader, dest string) error {
	var total uint64

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		rel, ok := canonicalArchivePath(f.Name)
		if !ok {
			return fmt.Errorf("unsafe archive path %q", f.Name)
		}

		clean, ok := safeJoin(dest, rel)
		if !ok {
			return fmt.Errorf("unsafe archive path %q", f.Name)
		}

		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return err
		}

		n, err := extractOne(f, clean)
		if err != nil {
			return err
		}

		if total > maxPluginExpandedBytes-n {
			return fmt.Errorf(
				"archive expanded size exceeds %d bytes",
				maxPluginExpandedBytes,
			)
		}
		total += n
	}

	return nil
}

func extractOne(f *zip.File, dest string) (uint64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// Native entry points need the executable bit; everything else is 0644.
	mode := os.FileMode(0o644)
	rel, _ := canonicalArchivePath(f.Name)
	if strings.HasPrefix(rel, "bin/") {
		mode = 0o755
	}

	out, err := os.OpenFile(
		dest,
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY,
		mode,
	)
	if err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(
		out,
		io.LimitReader(rc, int64(maxPluginFileBytes)+1),
	)
	closeErr := out.Close()

	if copyErr != nil {
		return uint64(n), copyErr
	}
	if closeErr != nil {
		return uint64(n), closeErr
	}
	if n > int64(maxPluginFileBytes) {
		return uint64(n), fmt.Errorf(
			"archive file %q exceeds expanded limit of %d bytes",
			f.Name,
			maxPluginFileBytes,
		)
	}

	return uint64(n), nil
}

// safeJoin joins base and a possibly-hostile relative path, returning ok=false if
// the result escapes base.
func safeJoin(base, name string) (string, bool) {
	rel, ok := canonicalArchivePath(name)
	if !ok {
		return "", false
	}

	clean := filepath.Join(
		base,
		filepath.FromSlash(rel),
	)

	relToBase, err := filepath.Rel(base, clean)
	if err != nil ||
		relToBase == ".." ||
		strings.HasPrefix(relToBase, ".."+string(os.PathSeparator)) {

		return "", false
	}

	return clean, true
}

func canonicalArchivePath(name string) (string, bool) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", false
	}

	// ZIP paths are slash-separated, but reject/normalise backslashes too so a
	// package has identical security semantics on Linux and Windows.
	normalised := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(normalised, "/") {
		return "", false
	}

	clean := path.Clean(normalised)
	if clean == "." ||
		clean == ".." ||
		strings.HasPrefix(clean, "../") {

		return "", false
	}

	return clean, true
}

func isPluginMetadata(name string) bool {
	return name == "plugin.json" || name == "plugin.sig"
}

func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		canonical, ok := canonicalArchivePath(f.Name)
		if ok && canonical == name && !f.FileInfo().IsDir() {
			return f
		}
	}
	return nil
}

func readZipFile(f *zip.File, maxBytes uint64) ([]byte, error) {
	if f.UncompressedSize64 > maxBytes {
		return nil, fmt.Errorf(
			"%q exceeds expanded limit of %d bytes",
			f.Name,
			maxBytes,
		)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	b, err := io.ReadAll(
		io.LimitReader(rc, int64(maxBytes)+1),
	)
	if err != nil {
		return nil, err
	}
	if uint64(len(b)) > maxBytes {
		return nil, fmt.Errorf(
			"%q exceeds expanded limit of %d bytes",
			f.Name,
			maxBytes,
		)
	}

	return b, nil
}

func hashZipFile(f *zip.File, maxBytes uint64) (string, error) {
	if f.UncompressedSize64 > maxBytes {
		return "", fmt.Errorf(
			"%q exceeds expanded limit of %d bytes",
			f.Name,
			maxBytes,
		)
	}

	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	h := sha256.New()
	n, err := io.Copy(
		h,
		io.LimitReader(rc, int64(maxBytes)+1),
	)
	if err != nil {
		return "", err
	}
	if n > int64(maxBytes) {
		return "", fmt.Errorf(
			"%q exceeds expanded limit of %d bytes",
			f.Name,
			maxBytes,
		)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashEqual compares a computed hex digest against a manifest value, tolerating an
// optional "sha256:" prefix and case differences.
func hashEqual(got, want string) bool {
	want = strings.TrimPrefix(want, "sha256:")
	return strings.EqualFold(got, want)
}
