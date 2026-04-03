package cabinet

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sync"
)

var (
	// ErrFormat is returned when the data is not a valid Cabinet file.
	ErrFormat = errors.New("cabinet: not a valid cabinet file")

	// ErrAlreadyOpen is returned by [File.Open] when another reader for the same folder is still open.
	ErrAlreadyOpen = errors.New("cabinet: folder already has an open reader")

	// ErrChecksum is returned when a data block fails its checksum.
	ErrChecksum = errors.New("cabinet: checksum error")
)

var errDuplicate = errors.New("duplicate entries in cabinet file")

// A ReadCloser is a [Reader] that must be closed when no longer needed.
type ReadCloser struct {
	*Reader

	f *os.File
}

// OpenReader opens the named CAB file.
func OpenReader(name string) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	r, err := NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &ReadCloser{Reader: r, f: f}, nil
}

// Close closes the Cabinet file, rendering it unusable for I/O.
func (rc *ReadCloser) Close() error {
	return rc.f.Close()
}

// A Reader serves content from a Cabinet archive.
type Reader struct {
	// Files is the list of files in the archive.
	Files []*File

	// SkipChecksum disables data block checksum verification.
	SkipChecksum bool

	r             io.ReaderAt
	decompressors map[Compression]Decompressor
	folderStates  []*folderState

	// Lazy fs.FS state.
	fsOnce sync.Once
	fsTree *dirTree
}

// NewReader creates a new [Reader] reading from r.
func NewReader(r io.ReaderAt) (*Reader, error) {
	hdr, err := readCFHeader(r)
	if err != nil {
		return nil, err
	}
	folderOff, err := folderArrayOffset(r, hdr)
	if err != nil {
		return nil, err
	}

	rd := &Reader{
		r:             r,
		decompressors: map[Compression]Decompressor{},
	}
	if err := rd.parseFolders(r, hdr, folderOff); err != nil {
		return nil, err
	}
	if err := rd.parseFiles(r, hdr); err != nil {
		return nil, err
	}
	return rd, nil
}

// folderArrayOffset returns the byte offset of the first CFFOLDER entry,
// stepping past the optional reserve block and cabinet-chain strings.
func folderArrayOffset(r io.ReaderAt, hdr cfHeader) (int64, error) {
	off := int64(cfHeaderSize)
	if hdr.Flags&flagReservePresent != 0 {
		off += 4 + int64(hdr.CbCFHeader) // 4 = cbCFHeader(2) + cbCFFolder(1) + cbCFData(1)
	}
	if hdr.Flags&flagPrevCabinet != 0 {
		var err error
		if off, err = skipNullStr(r, off); err != nil {
			return 0, err
		}
		if off, err = skipNullStr(r, off); err != nil {
			return 0, err
		}
	}
	if hdr.Flags&flagNextCabinet != 0 {
		var err error
		if off, err = skipNullStr(r, off); err != nil {
			return 0, err
		}
		if off, err = skipNullStr(r, off); err != nil {
			return 0, err
		}
	}
	return off, nil
}

// parseFolders reads all CFFOLDER entries and initialises folderStates.
func (rd *Reader) parseFolders(r io.ReaderAt, hdr cfHeader, off int64) error {
	folderSize := int64(cfFolderSize) + int64(hdr.CbCFFolder)
	rd.folderStates = make([]*folderState, hdr.CFolders)
	for i := range rd.folderStates {
		rec, err := readCFFolder(r, off+int64(i)*folderSize)
		if err != nil {
			return err
		}
		rd.folderStates[i] = &folderState{
			reader:       rd,
			coffCabStart: rec.CoffCabStart,
			cCFData:      rec.CCFData,
			cbCFData:     hdr.CbCFData,
			method:       rec.TypeCompress,
		}
	}
	return nil
}

// parseFiles reads all CFFILE entries and populates Files.
func (rd *Reader) parseFiles(r io.ReaderAt, hdr cfHeader) error {
	off := int64(hdr.CoffFiles)
	rd.Files = make([]*File, 0, hdr.CFiles)
	for range hdr.CFiles {
		rec, consumed, err := readCFFile(r, off)
		if err != nil {
			return err
		}
		off += consumed

		fh := FileHeader{
			Name:     backslashToSlash(rec.Name),
			Modified: decodeDOSTime(rec.Date, rec.Time),
		}
		attrsToHeader(rec.Attribs, &fh)

		rd.Files = append(rd.Files, &File{
			FileHeader:      fh,
			cbFile:          rec.CbFile,
			uoffFolderStart: rec.UoffFolderStart,
			iFolderIdx:      rec.IFolder,
			r:               rd,
		})
	}
	return nil
}

// RegisterDecompressor registers or overrides a decompressor for the given method.
// If a decompressor for a given method is not found, [Reader] will default to
// looking up the decompressor at the package level.
func (rd *Reader) RegisterDecompressor(method Compression, dcomp Decompressor) {
	rd.decompressors[method] = dcomp
}

// Open opens the named file in the archive, using the semantics of [fs.FS.Open]:
// paths are always slash-separated, with no leading slash or dot-dot elements.
//
// For best performance, open files in the order they appear in [Reader.Files],
// as reading out of order may require restarting decompression from the
// beginning of the folder.
func (rd *Reader) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	rd.fsOnce.Do(func() {
		rd.fsTree = &dirTree{}
		rd.fsTree.build(rd.Files)
	})

	node := rd.fsTree.find(name)
	switch {
	case node == nil:
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	case node.isDup:
		return nil, &fs.PathError{Op: "open", Path: name, Err: errDuplicate}
	case node.file != nil:
		rc, err := node.file.Open()
		if err != nil {
			return nil, err
		}
		return &fsFileHandle{file: node.file, rc: rc}, nil
	default:
		return &fsDirHandle{node: node, name: name}, nil
	}
}

// decompressor returns the decompressor for method, falling back to the package-level registry.
func (rd *Reader) decompressor(method Compression) Decompressor {
	if d := rd.decompressors[method]; d != nil {
		return d
	}
	return decompressor(method)
}

// A File is a single file in a Cabinet archive. The file information is in the
// embedded [FileHeader]. The file content can be accessed by calling [File.Open].
type File struct {
	FileHeader

	cbFile          uint32
	uoffFolderStart uint32
	iFolderIdx      uint16

	r *Reader
}

// Size returns the uncompressed size in bytes.
func (f *File) Size() uint32 { return f.cbFile }

// FolderIndex returns the index of the folder containing the file.
func (f *File) FolderIndex() uint16 { return f.iFolderIdx }

// OffsetInFolder returns the uncompressed byte offset of the file within its folder.
func (f *File) OffsetInFolder() uint32 { return f.uoffFolderStart }

// Open returns an [io.ReadCloser] that provides access to the file's contents.
// Only one file within the same folder may be open at a time;
// opening a second returns [ErrAlreadyOpen].
func (f *File) Open() (io.ReadCloser, error) {
	if int(f.iFolderIdx) >= len(f.r.folderStates) {
		return nil, fmt.Errorf("cabinet: folder index %d out of range", f.iFolderIdx)
	}

	folder := f.r.folderStates[f.iFolderIdx]

	if !folder.mu.TryLock() {
		return nil, ErrAlreadyOpen
	}
	dcomp := f.r.decompressor(folder.method)
	if dcomp == nil {
		folder.mu.Unlock()
		return nil, ErrAlgorithm
	}
	if err := folder.seekTo(dcomp, f.uoffFolderStart); err != nil {
		folder.mu.Unlock()
		return nil, err
	}

	return &fileReader{
		lr:     io.LimitReader(folder.decomp, int64(f.cbFile)),
		fs:     folder,
		size:   f.cbFile,
		target: f.uoffFolderStart + f.cbFile,
	}, nil
}

// fileReader wraps a bounded read from a folder decompressor.
// It holds the folder mutex for its entire lifetime.
type fileReader struct {
	lr     io.Reader
	fs     *folderState
	size   uint32
	target uint32 // expected folder pos after full read
	read   uint32
	closed bool
}

func (fr *fileReader) Read(p []byte) (int, error) {
	n, err := fr.lr.Read(p)
	fr.read += uint32(n)
	fr.fs.pos += uint32(n)
	return n, err
}

func (fr *fileReader) Close() error {
	if fr.closed {
		return nil
	}
	fr.closed = true
	if fr.read < fr.size {
		// Partial read: invalidate the cache so next opener starts fresh.
		if fr.fs.decomp != nil {
			fr.fs.decomp.Close()
			fr.fs.decomp = nil
			fr.fs.pos = 0
		}
	}
	fr.fs.mu.Unlock()
	return nil
}

// folderState holds the decompression state for one CFFOLDER.
type folderState struct {
	mu sync.Mutex

	// Static config (set once by NewReader).
	reader       *Reader
	coffCabStart uint32
	cCFData      uint16
	cbCFData     uint8
	method       Compression

	// Mutable decompressor cache (protected by mu).
	decomp io.ReadCloser
	cfdr   cfDataReader
	pos    uint32 // uncompressed bytes consumed so far
}

// resetCFDataReader reinitialises cfdr to read from the start of this folder's CFDATA blocks.
func (fs *folderState) resetCFDataReader() {
	fs.cfdr.reader = fs.reader
	fs.cfdr.off = int64(fs.coffCabStart)
	fs.cfdr.cbCFData = fs.cbCFData
	fs.cfdr.remaining = fs.cCFData
	fs.cfdr.bufPos = 0
	if fs.cfdr.buf == nil {
		fs.cfdr.buf = make([]byte, 0, math.MaxUint16)
	} else {
		fs.cfdr.buf = fs.cfdr.buf[:0]
	}
}

// seekTo ensures the decompressor is positioned at the given uncompressed byte offset within the folder,
// rewinding and replaying from the start if the current position is past the target.
func (fs *folderState) seekTo(decomp Decompressor, target uint32) error {
	// If the cache is ahead of target, we must rewind from the start.
	if fs.decomp == nil || fs.pos > target {
		if fs.decomp != nil {
			fs.decomp.Close()
			fs.decomp = nil
		}
		fs.resetCFDataReader()
		fs.decomp = decomp(&fs.cfdr)
		fs.pos = 0
	}
	// Skip forward to target.
	if fs.pos < target {
		toSkip := int64(target - fs.pos)
		n, err := io.CopyN(io.Discard, fs.decomp, toSkip)
		fs.pos += uint32(n)
		if err != nil {
			return err
		}
	}
	return nil
}

// cfDataReader reads compressed payloads from sequential CFDATA blocks.
type cfDataReader struct {
	reader    *Reader
	off       int64
	cbCFData  uint8
	remaining uint16 // blocks left to read
	buf       []byte
	bufPos    int
}

func (cr *cfDataReader) Read(p []byte) (int, error) {
	for cr.bufPos >= len(cr.buf) {
		if cr.remaining == 0 {
			return 0, io.EOF
		}
		if err := cr.nextBlock(); err != nil {
			return 0, err
		}
	}
	n := copy(p, cr.buf[cr.bufPos:])
	cr.bufPos += n
	return n, nil
}

// nextBlock reads the next CFDATA block and resets the read position within it.
func (cr *cfDataReader) nextBlock() error {
	payload, n, err := readCFDataBlock(cr.reader.r, cr.off, cr.cbCFData, cr.buf, !cr.reader.SkipChecksum)
	if err != nil {
		return err
	}
	cr.off += n
	cr.buf = payload
	cr.bufPos = 0
	cr.remaining--
	return nil
}
