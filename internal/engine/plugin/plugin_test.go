package plugin

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/beetlebugorg/chartplotter/internal/engine/nmea"
	"github.com/stretchr/testify/require"
)

func TestManifestValidate(t *testing.T) {
	valid := `{"manifestVersion":1,"id":"org.example.foo","name":"Foo","version":"1.2.0",` +
		`"apiVersion":1,"entry":{"wasm":"plugin.wasm"}}`
	m, err := ParseManifest([]byte(valid))
	require.NoError(t, err)
	require.Equal(t, "org.example.foo", m.ID)

	bad := []string{
		`{"manifestVersion":2,"id":"org.example.foo","version":"1.0.0","apiVersion":1,"entry":{"wasm":"p"}}`,
		`{"manifestVersion":1,"id":"BadID","version":"1.0.0","apiVersion":1,"entry":{"wasm":"p"}}`,
		`{"manifestVersion":1,"id":"org.example.foo","version":"nope","apiVersion":1,"entry":{"wasm":"p"}}`,
		`{"manifestVersion":1,"id":"org.example.foo","version":"1.0.0","apiVersion":9,"entry":{"wasm":"p"}}`,
		`{"manifestVersion":1,"id":"org.example.foo","version":"1.0.0","apiVersion":1,"entry":{}}`, // no entry point
	}
	for _, b := range bad {
		_, err := ParseManifest([]byte(b))
		require.Error(t, err, b)
	}
}

func TestInstallVerifiesHashesAndRejectsCore(t *testing.T) {
	dir := t.TempDir()
	wasm := []byte("\x00asm fake module bytes")
	sum := sha256.Sum256(wasm)
	man := map[string]any{
		"manifestVersion": 1, "id": "org.example.foo", "name": "Foo", "version": "1.0.0",
		"apiVersion": 1, "entry": map[string]any{"wasm": "plugin.wasm"},
		"files": map[string]string{"plugin.wasm": "sha256:" + hex.EncodeToString(sum[:])},
	}
	manBytes, _ := json.Marshal(man)
	good := writeZip(t, dir, "good.zip", map[string][]byte{"plugin.json": manBytes, "plugin.wasm": wasm})

	pluginsDir := filepath.Join(dir, "plugins")
	m, err := Install(good, pluginsDir, InstallOptions{})
	require.NoError(t, err)
	require.Equal(t, "org.example.foo", m.ID)
	_, err = os.Stat(filepath.Join(pluginsDir, "org.example.foo", "1.0.0", "plugin.wasm"))
	require.NoError(t, err)

	// Tamper: manifest hash no longer matches → verify fails.
	tampered := writeZip(t, dir, "bad.zip", map[string][]byte{"plugin.json": manBytes, "plugin.wasm": []byte("different")})
	_, err = Install(tampered, pluginsDir, InstallOptions{})
	require.ErrorContains(t, err, "hash mismatch")

	// core.* rejected unless AllowCore.
	coreMan := map[string]any{"manifestVersion": 1, "id": "core.foo", "name": "C", "version": "1.0.0",
		"apiVersion": 1, "entry": map[string]any{"wasm": "plugin.wasm"},
		"files": map[string]string{"plugin.wasm": "sha256:" + hex.EncodeToString(sum[:])}}
	coreBytes, _ := json.Marshal(coreMan)
	coreZip := writeZip(t, dir, "core.zip", map[string][]byte{"plugin.json": coreBytes, "plugin.wasm": wasm})
	_, err = Install(coreZip, pluginsDir, InstallOptions{})
	require.ErrorContains(t, err, "reserved")
	_, err = Install(coreZip, pluginsDir, InstallOptions{AllowCore: true})
	require.NoError(t, err)
}

func TestInstallRejectsUnhashedArchivePayload(t *testing.T) {
	dir := t.TempDir()
	wasm := []byte("\x00asm verified")
	sum := sha256.Sum256(wasm)

	man := map[string]any{
		"manifestVersion": 1,
		"id":              "org.example.unhashed",
		"name":            "Unhashed",
		"version":         "1.0.0",
		"apiVersion":      1,
		"entry":           map[string]any{"wasm": "plugin.wasm"},
		"files": map[string]string{
			"plugin.wasm": "sha256:" + hex.EncodeToString(sum[:]),
		},
	}
	manBytes, _ := json.Marshal(man)

	archive := writeZip(
		t,
		dir,
		"unhashed.zip",
		map[string][]byte{
			"plugin.json": manBytes,
			"plugin.wasm": wasm,
			"bin/extra":   []byte("unverified executable payload"),
		},
	)

	_, err := Install(
		archive,
		filepath.Join(dir, "plugins"),
		InstallOptions{},
	)
	require.ErrorContains(t, err, "not listed in manifest.files")
}

func TestInstallRejectsDuplicateArchivePath(t *testing.T) {
	dir := t.TempDir()
	good := []byte("\x00asm verified")
	bad := []byte("\x00asm replacement")
	sum := sha256.Sum256(good)

	man := map[string]any{
		"manifestVersion": 1,
		"id":              "org.example.duplicate",
		"name":            "Duplicate",
		"version":         "1.0.0",
		"apiVersion":      1,
		"entry":           map[string]any{"wasm": "plugin.wasm"},
		"files": map[string]string{
			"plugin.wasm": "sha256:" + hex.EncodeToString(sum[:]),
		},
	}
	manBytes, _ := json.Marshal(man)

	archive := writeZipEntries(
		t,
		dir,
		"duplicate.zip",
		[]zipEntry{
			{name: "plugin.json", body: manBytes},
			{name: "plugin.wasm", body: good},
			{name: "plugin.wasm", body: bad},
		},
	)

	_, err := Install(
		archive,
		filepath.Join(dir, "plugins"),
		InstallOptions{},
	)
	require.ErrorContains(t, err, "duplicate archive path")
}

func TestInstallRejectsUnsafeEntryPath(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("x")
	sum := sha256.Sum256(payload)

	man := map[string]any{
		"manifestVersion": 1,
		"id":              "org.example.escape",
		"name":            "Escape",
		"version":         "1.0.0",
		"apiVersion":      1,
		"entry": map[string]any{
			"native": map[string]string{
				platformKey(): "../outside",
			},
		},
		"files": map[string]string{
			"payload.bin": "sha256:" + hex.EncodeToString(sum[:]),
		},
	}
	manBytes, _ := json.Marshal(man)

	archive := writeZip(
		t,
		dir,
		"escape.zip",
		map[string][]byte{
			"plugin.json": manBytes,
			"payload.bin": payload,
		},
	)

	_, err := Install(
		archive,
		filepath.Join(dir, "plugins"),
		InstallOptions{},
	)
	require.ErrorContains(t, err, "unsafe path")
}

func TestInstallAllowsReservedSignatureMetadata(t *testing.T) {
	dir := t.TempDir()
	wasm := []byte("\x00asm verified")
	sum := sha256.Sum256(wasm)

	man := map[string]any{
		"manifestVersion": 1,
		"id":              "org.example.signed",
		"name":            "Signed",
		"version":         "1.0.0",
		"apiVersion":      1,
		"entry":           map[string]any{"wasm": "plugin.wasm"},
		"files": map[string]string{
			"plugin.wasm": "sha256:" + hex.EncodeToString(sum[:]),
		},
	}
	manBytes, _ := json.Marshal(man)

	archive := writeZip(
		t,
		dir,
		"signed.zip",
		map[string][]byte{
			"plugin.json": manBytes,
			"plugin.wasm": wasm,
			"plugin.sig":  []byte("future-signature-metadata"),
		},
	)

	_, err := Install(
		archive,
		filepath.Join(dir, "plugins"),
		InstallOptions{},
	)
	require.NoError(t, err)
}

func TestPluginHTTPClientRejectsRedirectOutsideAllowlist(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		http.Redirect(
			w,
			r,
			target.URL+"/secret",
			http.StatusFound,
		)
	}))
	defer source.Close()

	sourceURL, err := url.Parse(source.URL)
	require.NoError(t, err)

	client := newPluginHTTPClient(Capability{
		Hosts: []string{sourceURL.Host},
	})

	req, err := http.NewRequest(
		http.MethodGet,
		source.URL,
		nil,
	)
	require.NoError(t, err)

	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	require.ErrorContains(
		t,
		err,
		"redirect host",
	)
}

func TestMatchHostAllow(t *testing.T) {
	cases := []struct {
		patterns  []string
		host      string
		port      int
		wantAllow bool
	}{
		{[]string{"sk.local:3000"}, "sk.local", 3000, true},
		{[]string{"sk.local:3000"}, "sk.local", 3001, false},
		{[]string{"sk.local"}, "sk.local", 9999, true},
		{[]string{"*.tile.example.com"}, "a.tile.example.com", 443, true},
		{[]string{"*.tile.example.com"}, "tile.example.com", 443, true},
		{[]string{"*.tile.example.com"}, "evil.com", 443, false},
		{[]string{"192.168.1.10:2000"}, "192.168.1.10", 2000, true},
		{nil, "anything", 80, false},
	}
	for _, c := range cases {
		require.Equal(t, c.wantAllow, matchHostAllow(c.patterns, c.host, c.port), c)
	}
}

func TestVesselPublishDeltasMapping(t *testing.T) {
	store := &nmea.Store{}
	n, err := store.PublishDeltas("pluginX", []nmea.Delta{
		{Path: "navigation.sog", Value: json.RawMessage(`6.2`)},
		{Path: "navigation.position", Value: json.RawMessage(`{"lat":48.1,"lon":11.5}`)},
		{Path: "not.a.real.path", Value: json.RawMessage(`1`)},
	})
	require.Equal(t, 2, n)
	require.ErrorContains(t, err, "unknown vessel path")

	snap := store.Snapshot()
	require.NotNil(t, snap.Navigation.SOG)
	require.InDelta(t, 6.2, *snap.Navigation.SOG, 1e-9)
	require.NotNil(t, snap.Navigation.Position)
	require.InDelta(t, 48.1, snap.Navigation.Position.Lat, 1e-9)
	require.Equal(t, "pluginX", store.Provenance()["navigation.sog"])
}

func TestLineLoggerBoundsUnterminatedLine(t *testing.T) {
	var messages []string

	logger := &lineLogger{
		logf: func(level, msg string) {
			messages = append(messages, msg)
		},
	}

	payload := bytes.Repeat(
		[]byte("x"),
		maxPluginLogLine+1024,
	)

	n, err := logger.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Len(t, logger.buf, 0)
	require.True(t, logger.dropping)
	require.Len(t, messages, 1)
	require.Contains(t, messages[0], "[truncated]")

	_, err = logger.Write([]byte("discard this\nok\n"))
	require.NoError(t, err)
	require.False(t, logger.dropping)
	require.Equal(t, "ok", messages[len(messages)-1])
}

func TestPluginHandleTableIsBounded(t *testing.T) {
	b := &brokerSession{
		handles: map[int]*ioHandle{},
	}

	for i := 0; i < maxPluginHandles; i++ {
		if _, ok := b.addHandle(&ioHandle{}); !ok {
			t.Fatalf("handle %d should fit", i)
		}
	}

	if _, ok := b.addHandle(&ioHandle{}); ok {
		t.Fatal("expected handle table to reject overflow")
	}
}

func TestParseBytes(t *testing.T) {
	require.Equal(t, int64(10<<20), parseBytes("10MB"))
	require.Equal(t, int64(512<<10), parseBytes("512KB"))
	require.Equal(t, int64(1024), parseBytes("1024"))
	require.Equal(t, int64(2048), parseBytes("2048B"))

	// Invalid, zero, negative, and overflowing quotas fall back to the
	// conservative default rather than accidentally becoming "unlimited".
	require.Equal(t, int64(5<<20), parseBytes("0"))
	require.Equal(t, int64(5<<20), parseBytes("-1"))
	require.Equal(t, int64(5<<20), parseBytes("9223372036854775807MB"))
}

func TestPluginStorageBudgetIncludesServedArtifacts(t *testing.T) {
	dir := t.TempDir()

	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(dir, "existing.bin"),
			[]byte("123456"),
			0o644,
		),
	)

	b := &brokerSession{
		storeDir: dir,
		quota:    10,
	}

	err := b.ensureStorageBudget(
		filepath.Join(dir, "new.bin"),
		5,
	)
	require.ErrorContains(t, err, "storage quota exceeded")

	err = b.ensureStorageBudget(
		filepath.Join(dir, "existing.bin"),
		9,
	)
	require.NoError(t, err)
}

type zipEntry struct {
	name string
	body []byte
}

func writeZipEntries(
	t *testing.T,
	dir,
	name string,
	entries []zipEntry,
) string {
	t.Helper()

	archivePath := filepath.Join(dir, name)
	f, err := os.Create(archivePath)
	require.NoError(t, err)
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		require.NoError(t, err)
		_, err = w.Write(entry.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	return archivePath
}

// writeZip builds a zip archive at dir/name from files and returns its path.
func writeZip(t *testing.T, dir, name string, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	zw := zip.NewWriter(f)
	for n, b := range files {
		w, err := zw.Create(n)
		require.NoError(t, err)
		_, err = w.Write(b)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return path
}
