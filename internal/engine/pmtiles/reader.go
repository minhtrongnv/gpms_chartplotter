package pmtiles

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// Directory sections are normally tiny relative to tile data. Keep a generous
	// hard ceiling so a corrupt/hand-dropped archive cannot ask the server to
	// allocate gigabytes before any tile request is served.
	maxReaderDirectoryBytes uint64 = 256 << 20
	maxReaderMetadataBytes  uint64 = 1 << 20
	maxReaderTileBytes      uint64 = 64 << 20
)

// Reader provides random tile access to a PMTiles v3 archive written by Builder.
// It is the mirror of WriteArchive/serializeDir: parse the 127-byte header, then
// walk the root directory (descending into a leaf page when the archive is large)
// to find a tile's (offset,length) in the data section. Directories are read once
// and cached; tile bodies are read per request via the ReaderAt.
//
// Only the subset Builder emits is supported: uncompressed directories, MVT tiles
// (optionally gzipped), single root until >leafSize entries then one leaf level.
type Reader struct {
	src     io.ReaderAt
	closer  io.Closer
	root    []entry
	leaves  []byte // raw leaf-directory section (parsed on demand)
	leafOff uint64
	dataOff uint64
	dataLen uint64
	tileGz  bool
	meta    TileMeta
}

// TileMeta is an archive's display metadata (header fields + JSON metadata).
type TileMeta struct {
	MinZoom, MaxZoom uint8
	W, S, E, N       float64  // lon/lat bounds (degrees)
	Gzipped          bool     // tile bodies are gzip-compressed
	Scamin           []uint32 // distinct SCAMIN denominators present (from JSON metadata)
	// TileType is the stored tile encoding: "mvt" or "mlt" (a tile57 MLT-default
	// bake); "" for an unrecognised header type. Serving stays bytes-verbatim
	// either way — this only drives the TileJSON/style `encoding` hint (and the
	// tile content type) so maplibre-gl picks the matching decoder.
	TileType string
}

// Open opens a .pmtiles file for reading. Close releases the file handle.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	rd, err := NewReader(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	rd.closer = f
	return rd, nil
}

// NewReader parses the header + root directory from an archive of the given size.
func NewReader(src io.ReaderAt, size int64) (*Reader, error) {
	if size < 127 {
		return nil, errors.New("pmtiles: too small for a header")
	}
	var h [127]byte
	if _, err := src.ReadAt(h[:], 0); err != nil {
		return nil, err
	}
	if string(h[0:7]) != "PMTiles" || h[7] != 3 {
		return nil, errors.New("pmtiles: bad magic or version")
	}
	rootOff := binary.LittleEndian.Uint64(h[8:16])
	rootLen := binary.LittleEndian.Uint64(h[16:24])
	metaOff := binary.LittleEndian.Uint64(h[24:32])
	metaLen := binary.LittleEndian.Uint64(h[32:40])
	leafOff := binary.LittleEndian.Uint64(h[40:48])
	leafLen := binary.LittleEndian.Uint64(h[48:56])
	dataOff := binary.LittleEndian.Uint64(h[56:64])
	dataLen := binary.LittleEndian.Uint64(h[64:72])
	fileSize := uint64(size)

	rootEnd, rootOK := checkedSectionEnd(fileSize, rootOff, rootLen)
	metaEnd, metaOK := checkedSectionEnd(fileSize, metaOff, metaLen)
	leafEnd, leafOK := checkedSectionEnd(fileSize, leafOff, leafLen)
	_, dataOK := checkedSectionEnd(fileSize, dataOff, dataLen)
	if !rootOK || !metaOK || !leafOK || !dataOK ||
		rootOff < 127 || rootEnd > metaOff || metaEnd > leafOff || leafEnd > dataOff {
		return nil, errors.New("pmtiles: invalid or overlapping header sections")
	}
	if rootLen > maxReaderDirectoryBytes || leafLen > maxReaderDirectoryBytes {
		return nil, fmt.Errorf("pmtiles: directory section exceeds %d bytes", maxReaderDirectoryBytes)
	}
	if metaLen > maxReaderMetadataBytes {
		return nil, fmt.Errorf("pmtiles: metadata exceeds %d bytes", maxReaderMetadataBytes)
	}
	if h[97] != compressionNone {
		return nil, fmt.Errorf("pmtiles: unsupported directory compression %d", h[97])
	}
	if h[98] != compressionNone && h[98] != compressionGzip {
		return nil, fmt.Errorf("pmtiles: unsupported tile compression %d", h[98])
	}

	rootBytes := make([]byte, int(rootLen))
	if _, err := src.ReadAt(rootBytes, int64(rootOff)); err != nil {
		return nil, err
	}
	root, err := deserializeDir(rootBytes)
	if err != nil {
		return nil, err
	}

	rd := &Reader{
		src:     src,
		root:    root,
		leafOff: leafOff,
		dataOff: dataOff,
		dataLen: dataLen,
		tileGz:  h[98] == compressionGzip,
		meta: TileMeta{
			MinZoom:  h[100],
			MaxZoom:  h[101],
			W:        float64(int32(binary.LittleEndian.Uint32(h[102:106]))) / 1e7,
			S:        float64(int32(binary.LittleEndian.Uint32(h[106:110]))) / 1e7,
			E:        float64(int32(binary.LittleEndian.Uint32(h[110:114]))) / 1e7,
			N:        float64(int32(binary.LittleEndian.Uint32(h[114:118]))) / 1e7,
			Gzipped:  h[98] == compressionGzip,
			TileType: tileTypeName(h[99]),
		},
	}
	// JSON metadata (between metaOff and leafOff): parse the SCAMIN manifest so the
	// client can build per-SCAMIN bucket layers at load. Best-effort — absence just
	// means the older runtime-collection path is used.
	if metaLen > 0 {
		mb := make([]byte, int(metaLen))
		if _, err := src.ReadAt(mb, int64(metaOff)); err == nil {
			var md struct {
				Scamin []uint32 `json:"scamin"`
			}
			if json.Unmarshal(mb, &md) == nil {
				rd.meta.Scamin = md.Scamin
			}
		}
	}
	// Leaf directory bytes are bounded and header-validated above.
	if leafLen > 0 {
		rd.leaves = make([]byte, int(leafLen))
		if _, err := src.ReadAt(rd.leaves, int64(leafOff)); err != nil {
			return nil, err
		}
	}
	return rd, nil
}

func checkedSectionEnd(size, off, length uint64) (uint64, bool) {
	if off > size || length > size-off {
		return 0, false
	}
	return off + length, true
}

// Meta returns the archive's header metadata.
func (rd *Reader) Meta() TileMeta { return rd.meta }

// Close releases the underlying file, if Reader owns one.
func (rd *Reader) Close() error {
	if rd.closer != nil {
		return rd.closer.Close()
	}
	return nil
}

// Tile returns the (decompressed) MVT body for z/x/y, or nil if the archive holds
// no tile there (a missing tile reads as blank, matching the writer's contract).
func (rd *Reader) Tile(z uint8, x, y uint32) ([]byte, error) {
	id := ZxyToTileID(z, x, y)

	e, ok := findEntry(rd.root, id)
	if !ok {
		return nil, nil
	}
	if e.runLength == 0 { // leaf pointer: descend one level
		leafEnd, ok := checkedSectionEnd(uint64(len(rd.leaves)), e.offset, e.length)
		if !ok {
			return nil, errors.New("pmtiles: leaf pointer outside directory section")
		}
		leaf, err := deserializeDir(rd.leaves[int(e.offset):int(leafEnd)])
		if err != nil {
			return nil, err
		}
		e, ok = findEntry(leaf, id)
		if !ok || e.runLength == 0 {
			return nil, nil
		}
	}

	if e.length > maxReaderTileBytes {
		return nil, fmt.Errorf("pmtiles: tile exceeds %d bytes", maxReaderTileBytes)
	}
	if _, ok := checkedSectionEnd(rd.dataLen, e.offset, e.length); !ok {
		return nil, errors.New("pmtiles: tile points outside data section")
	}
	body := make([]byte, int(e.length))
	if _, err := rd.src.ReadAt(body, int64(rd.dataOff+e.offset)); err != nil {
		return nil, err
	}
	if rd.tileGz {
		return gunzip(body)
	}
	return body, nil
}

// findEntry returns the directory entry covering tileID: the greatest entry whose
// tileID <= target, provided target falls within its run (runLength 0 == a leaf
// pointer, which the caller descends). Entries are sorted by tileID.
func findEntry(entries []entry, target uint64) (entry, bool) {
	lo, hi := 0, len(entries)
	for lo < hi { // upper_bound: first entry with tileID > target
		mid := (lo + hi) / 2
		if entries[mid].tileID <= target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return entry{}, false
	}
	e := entries[lo-1]
	if e.runLength == 0 { // leaf pointer covers everything up to the next entry
		return e, true
	}
	if target-e.tileID < uint64(e.runLength) {
		return e, true
	}
	return entry{}, false
}

// deserializeDir is the inverse of serializeDir.
func deserializeDir(buf []byte) ([]entry, error) {
	r := &bytesReader{b: buf}
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	// Each entry needs at least one varint in each of the four encoded arrays.
	// Reject an impossible count before converting it to int or allocating.
	if n > uint64(len(buf))/4 {
		return nil, errors.New("pmtiles: impossible directory entry count")
	}
	entries := make([]entry, int(n))

	var prev uint64
	for i := range entries { // delta-encoded tile ids
		d, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		prev += d
		entries[i].tileID = prev
	}
	for i := range entries { // run lengths
		v, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		entries[i].runLength = uint32(v)
	}
	for i := range entries { // lengths
		v, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		entries[i].length = v
	}
	for i := range entries { // offsets: 0 == contiguous with previous, else value-1
		v, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		if v == 0 {
			if i == 0 {
				return nil, errors.New("pmtiles: first directory offset cannot be contiguous")
			}
			prev := entries[i-1]
			if prev.length > ^uint64(0)-prev.offset {
				return nil, errors.New("pmtiles: directory offset overflow")
			}
			entries[i].offset = prev.offset + prev.length
		} else {
			entries[i].offset = v - 1
		}
	}
	return entries, nil
}

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.i:])
	if n <= 0 {
		return 0, errors.New("pmtiles: truncated varint in directory")
	}
	r.i += n
	return v, nil
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
