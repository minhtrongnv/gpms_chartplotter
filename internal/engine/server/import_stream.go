package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tile57 "github.com/beetlebugorg/tile57/bindings/go"
)

// stagedExchangeSet is a disk-backed import staging area. Large ZIP payloads and
// ENC/aux entries stay on disk; only the small catalogue metadata and the list of
// cell stems are retained in memory.
type stagedExchangeSet struct {
	dir     string
	stems   []string
	catalog []tile57.CatalogEntry
}

const importScratchMarker = ".chartplotter-owned"

// cleanupImportScratch removes only transient directories carrying our ownership
// marker. This avoids treating an unrelated plugin/application ".imports" folder
// under the shared data root as disposable.
func cleanupImportScratch(dataDir string) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		scratch := filepath.Join(dataDir, entry.Name(), ".imports")
		if _, err := os.Stat(filepath.Join(scratch, importScratchMarker)); err == nil {
			_ = os.RemoveAll(scratch)
		}
	}
}

func (s *Server) importScratchDir(provider string) (string, error) {
	root := filepath.Join(s.providerDataDir(provider), ".imports")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(
		filepath.Join(root, importScratchMarker),
		[]byte("chartplotter transient import scratch\n"),
		0o600,
	); err != nil {
		return "", err
	}
	return root, nil
}

// spoolImportRequest copies a raw or multipart upload to a bounded temporary file.
// Multipart parsing may keep a small prefix in memory, but large files are spilled
// by net/http and are never materialised as one []byte by the chartplotter.
func (s *Server) spoolImportRequest(
	ctx context.Context,
	r *http.Request,
	provider string,
) (string, int64, error) {
	var (
		src io.Reader = r.Body
		cleanup func()
	)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxImportMultipartMemory); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return "", 0, fmt.Errorf(
					"%w: import request exceeds %d bytes",
					errRequestBodyTooLarge,
					maxImportRequestBytes,
				)
			}
			return "", 0, fmt.Errorf("multipart: %w", err)
		}
		if r.MultipartForm != nil {
			cleanup = func() { _ = r.MultipartForm.RemoveAll() }
		}

		f, _, err := r.FormFile("file")
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return "", 0, fmt.Errorf("multipart: %w", err)
		}
		defer f.Close()
		src = f
	}
	if cleanup != nil {
		defer cleanup()
	}

	scratch, err := s.importScratchDir(provider)
	if err != nil {
		return "", 0, err
	}
	return spoolReaderToTemp(ctx, src, scratch, "upload-*.zip", maxImportBytes)
}

func spoolReaderToTemp(
	ctx context.Context,
	src io.Reader,
	dir, pattern string,
	maxBytes int64,
) (path string, written int64, err error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", 0, err
	}
	path = tmp.Name()
	ok := false
	closed := false
	defer func() {
		if !closed {
			if closeErr := tmp.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if !ok {
			_ = os.Remove(path)
		}
	}()

	written, err = copyLimitedContext(ctx, tmp, src, maxBytes)
	if err != nil {
		return "", written, err
	}
	if err := tmp.Close(); err != nil {
		return "", written, err
	}
	closed = true
	ok = true
	return path, written, nil
}

func copyLimitedContext(
	ctx context.Context,
	dst io.Writer,
	src io.Reader,
	maxBytes int64,
) (int64, error) {
	if maxBytes < 0 {
		return 0, fmt.Errorf("invalid byte limit")
	}
	lr := &io.LimitedReader{R: src, N: maxBytes + 1}
	buf := make([]byte, 256<<10)
	var written int64

	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, rerr := lr.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > maxBytes {
				return written, fmt.Errorf(
					"%w: payload exceeds %d bytes",
					errRequestBodyTooLarge,
					maxBytes,
				)
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return written, err
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

func fileIsZip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	n, _ := io.ReadFull(f, magic[:])
	return n == len(magic) && isZip(magic[:])
}

// downloadURLToTemp streams a provider ZIP directly to disk. It preserves the
// existing no-progress watchdog while also inheriting the server import context,
// so shutdown cancels an in-flight download immediately.
func (s *Server) downloadURLToTemp(
	ctx context.Context,
	raw, provider string,
	onProgress func(done, total int),
) (string, error) {
	scratch, err := s.importScratchDir(provider)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := chartHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.ContentLength > maxImportBytes {
		return "", fmt.Errorf("download exceeds %d bytes", maxImportBytes)
	}

	tmp, err := os.CreateTemp(scratch, "download-*.zip")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()

	total := 0
	if resp.ContentLength > 0 && resp.ContentLength <= int64(^uint(0)>>1) {
		total = int(resp.ContentLength)
	}
	var done int64
	buf := make([]byte, 256<<10)

	stallTimer := time.AfterFunc(chartDownloadNoProgressTimeout, func() {
		cancel(fmt.Errorf(
			"%w: no bytes received for %s",
			errChartDownloadStalled,
			chartDownloadNoProgressTimeout,
		))
	})
	defer stallTimer.Stop()

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stallTimer.Reset(chartDownloadNoProgressTimeout)
			done += int64(n)
			if done > maxImportBytes {
				return "", fmt.Errorf("download exceeds %d bytes", maxImportBytes)
			}
			if _, err := tmp.Write(buf[:n]); err != nil {
				return "", err
			}
			if onProgress != nil {
				progressDone := int(done)
				if int64(progressDone) != done {
					progressDone = int(^uint(0) >> 1)
				}
				onProgress(progressDone, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if cause := context.Cause(ctx); cause != nil {
				return "", cause
			}
			return "", rerr
		}
	}

	if err := tmp.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

// stageExchangeSetZip extracts only relevant exchange-set content into scratch
// storage. Each entry is streamed independently and the aggregate expanded byte
// budget is enforced using actual bytes read, so a ZIP bomb cannot inflate RAM.
func (s *Server) stageExchangeSetZip(
	ctx context.Context,
	zipPath, provider string,
	names []string,
	applyUpdates bool,
) (stagedExchangeSet, error) {
	scratch, err := s.importScratchDir(provider)
	if err != nil {
		return stagedExchangeSet{}, err
	}
	stageDir, err := os.MkdirTemp(scratch, "stage-*")
	if err != nil {
		return stagedExchangeSet{}, err
	}
	stage := stagedExchangeSet{dir: stageDir}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stageDir)
		}
	}()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return stagedExchangeSet{}, fmt.Errorf("not a valid zip: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxImportZipEntries {
		return stagedExchangeSet{}, fmt.Errorf(
			"zip has too many entries (%d > %d)",
			len(zr.File),
			maxImportZipEntries,
		)
	}

	want := make(map[string]bool, len(names))
	for _, name := range names {
		stem := strings.TrimSuffix(filepath.Base(name), ".000")
		if isCellName(stem) {
			want[stem] = true
		}
	}
	baseSeen := map[string]bool{}
	auxSeen := map[string]bool{}
	updateFiles := map[string][]string{}
	var expanded int64
	var catalogDone bool

	for _, entry := range zr.File {
		if err := ctx.Err(); err != nil {
			return stagedExchangeSet{}, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}

		isCat := isCatalogFile(entry.Name)
		ext := ""
		if !isCat {
			ext = encExtServer(entry.Name)
		}
		isAux := ext == "" && isAuxContentServer(entry.Name)
		if !isCat && ext == "" && !isAux {
			continue
		}
		if entry.UncompressedSize64 > uint64(maxImportZipEntryBytes) {
			return stagedExchangeSet{}, fmt.Errorf(
				"zip entry %q exceeds expanded limit of %d bytes",
				entry.Name,
				maxImportZipEntryBytes,
			)
		}

		if ext != "" {
			base := filepath.Base(entry.Name)
			stem := strings.TrimSuffix(base, filepath.Ext(base))
			if !isCellName(stem) {
				continue
			}
			if len(want) > 0 && !want[stem] {
				continue
			}
			if !applyUpdates && ext != ".000" {
				continue
			}

			if ext == ".000" && baseSeen[stem] {
				continue // current in-memory importer is first-base-wins
			}
			if maxImportExpandedBytes-expanded <= 0 {
				return stagedExchangeSet{}, fmt.Errorf(
					"zip expanded payload exceeds %d bytes",
					maxImportExpandedBytes,
				)
			}
			destName := base
			if ext == ".000" {
				destName = stem + ".000"
			}
			n, err := copyZipEntryToFile(
				ctx,
				entry,
				filepath.Join(stageDir, destName),
				min64(maxImportZipEntryBytes, maxImportExpandedBytes-expanded),
			)
			if err != nil {
				return stagedExchangeSet{}, err
			}
			expanded += n
			if ext == ".000" {
				baseSeen[stem] = true
			} else {
				updateFiles[stem] = append(updateFiles[stem], destName)
			}
			continue
		}

		if isCat {
			if catalogDone {
				continue
			}
			b, n, err := readZipEntryContext(
				ctx,
				entry,
				min64(maxImportZipEntryBytes, maxImportExpandedBytes-expanded),
			)
			if err != nil {
				return stagedExchangeSet{}, err
			}
			expanded += n
			catalogDone = true
			if entries, err := tile57.CatalogEntries(b); err == nil {
				stage.catalog = entries
				if encoded, err := json.Marshal(entries); err == nil {
					if err := os.WriteFile(
						filepath.Join(stageDir, districtCatFile),
						encoded,
						0o644,
					); err != nil {
						return stagedExchangeSet{}, err
					}
				}
			} else {
				// Match the old importer: a malformed optional catalogue does not
				// invalidate otherwise-valid ENC cells.
				log.Printf("import: CATALOG.031 parse failed (ignored): %v", err)
			}
			continue
		}

		auxName := strings.ToUpper(filepath.Base(entry.Name))
		if auxSeen[auxName] {
			continue
		}
		n, err := copyZipEntryToFile(
			ctx,
			entry,
			filepath.Join(stageDir, auxName),
			min64(maxImportZipEntryBytes, maxImportExpandedBytes-expanded),
		)
		if err != nil {
			return stagedExchangeSet{}, err
		}
		expanded += n
		auxSeen[auxName] = true
	}

	// Updates without a matching base cell are not part of a usable exchange set.
	for stem, files := range updateFiles {
		if baseSeen[stem] {
			continue
		}
		for _, name := range files {
			_ = os.Remove(filepath.Join(stageDir, name))
		}
	}

	stage.stems = make([]string, 0, len(baseSeen))
	for stem := range baseSeen {
		stage.stems = append(stage.stems, stem)
	}
	sort.Strings(stage.stems)
	ok = true
	return stage, nil
}

func copyZipEntryToFile(
	ctx context.Context,
	entry *zip.File,
	dest string,
	maxBytes int64,
) (int64, error) {
	rc, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".entry-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	n, err := copyLimitedContext(ctx, tmp, rc, maxBytes)
	if err != nil {
		return n, fmt.Errorf("zip entry %q: %w", entry.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	if err := replaceFile(tmpPath, dest); err != nil {
		return n, err
	}
	ok = true
	return n, nil
}

func readZipEntryContext(
	ctx context.Context,
	entry *zip.File,
	maxBytes int64,
) ([]byte, int64, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()

	var buf bytes.Buffer
	n, err := copyLimitedContext(ctx, &buf, rc, maxBytes)
	if err != nil {
		return nil, n, fmt.Errorf("zip entry %q: %w", entry.Name, err)
	}
	return buf.Bytes(), n, nil
}

func replaceFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	// Windows cannot replace an existing file with Rename. The Linux/ship path
	// never takes this fallback, but keep tests/development portable.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dest)
}

// commitStagedExchangeSet merges a fully-validated staging directory into the
// district. Bakes are excluded while files move, so the native engine never walks
// a half-committed update chain.
func (s *Server) commitStagedExchangeSet(
	ctx context.Context,
	provider, district string,
	stage stagedExchangeSet,
) error {
	if len(stage.stems) == 0 {
		return fmt.Errorf("no ENC base cells (.000) in input")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.bakeMu.Lock()
	defer s.bakeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	destDir := s.districtDir(provider, district)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	// Match cacheDistrict semantics: replacing a base cell first removes its old
	// .001+ chain, so updates=0 or a shorter new chain cannot leave stale updates.
	stems := make(map[string]bool, len(stage.stems))
	for _, stem := range stage.stems {
		stems[stem] = true
	}
	if entries, err := os.ReadDir(destDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := encExtServer(entry.Name())
			stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if ext != "" && ext != ".000" && stems[stem] {
				if err := os.Remove(filepath.Join(destDir, entry.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}

	entries, err := os.ReadDir(stage.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(stage.dir, entry.Name())
		dst := filepath.Join(destDir, entry.Name())
		if err := replaceFile(src, dst); err != nil {
			return err
		}
	}

	if s.cellIdx != nil {
		s.cellIdx.forget(stage.stems)
		s.cellIdx.rebuild()
	}
	if s.auxIdx != nil {
		s.auxIdx.invalidate()
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
