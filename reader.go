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

	rd := &Reader{r: r}
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
			comp:         rec.TypeCompress,
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
	if rd.decompressors == nil {
		rd.decompressors = map[Compression]Decompressor{}
	}
	rd.decompressors[method.Type()] = dcomp
}

// decompressor returns the decompressor for method, falling back to the package-level registry.
func (rd *Reader) decompressor(method Compression) Decompressor {
	typ := method.Type()
	if d := rd.decompressors[typ]; d != nil {
		return d
	}
	if v, ok := decompressors.Load(typ); ok {
		return v.(Decompressor)
	}
	return nil
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

// Compression returns the compression type of the folder containing the file,
// or zero if the folder is continued from or to another cabinet.
func (f *File) Compression() Compression {
	if int(f.iFolderIdx) >= len(f.r.folderStates) {
		return 0
	}
	return f.r.folderStates[f.iFolderIdx].comp
}

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
	if err := folder.seekTo(f.uoffFolderStart); err != nil {
		folder.mu.Unlock()
		return nil, err
	}
	return &fileReader{fs: folder, remaining: f.cbFile}, nil
}

// fileReader reads a file's data from its folder.
// It holds the folder mutex for its entire lifetime.
type fileReader struct {
	fs        *folderState
	remaining uint32
	closed    bool
}

func (fr *fileReader) Read(p []byte) (int, error) {
	if fr.closed {
		return 0, fs.ErrClosed
	}
	if fr.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > int64(fr.remaining) {
		p = p[:fr.remaining]
	}
	n, err := fr.fs.read(p)
	fr.remaining -= uint32(n)
	return n, err
}

func (fr *fileReader) Close() error {
	if fr.closed {
		return nil
	}
	fr.closed = true
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
	comp         Compression

	// Mutable decoding state (protected by mu).
	dec    Decoder
	off    int64  // offset of the next CFDATA block
	blocks uint16 // CFDATA blocks left to read
	in     []byte // buffer for the compressed data of a block
	out    []byte // uncompressed data of the current block
	outPos int    // read position within out
	pos    uint32 // uncompressed bytes consumed so far
}

// seekTo ensures the decompressor is positioned at the given uncompressed byte offset within the folder,
// rewinding and replaying from the start if the current position is past the target.
func (fs *folderState) seekTo(target uint32) error {
	// If the cache is ahead of target, we must rewind from the start.
	if fs.dec == nil || fs.pos > target {
		if err := fs.rewind(); err != nil {
			return err
		}
	}
	// Skip forward to target.
	for fs.pos < target {
		if fs.outPos == len(fs.out) {
			if err := fs.nextBlock(); err != nil {
				return err
			}
		}
		n := min(len(fs.out)-fs.outPos, int(target-fs.pos))
		fs.outPos += n
		fs.pos += uint32(n)
	}
	return nil
}

// rewind positions the folder at its first CFDATA block.
func (fs *folderState) rewind() error {
	if fs.dec == nil {
		dcomp := fs.reader.decompressor(fs.comp)
		if dcomp == nil {
			return ErrAlgorithm
		}
		dec, err := dcomp(fs.comp.Level(), fs.comp.Memory())
		if err != nil {
			return fmt.Errorf("%w: %w", ErrFormat, err)
		}
		fs.dec = dec
		fs.in = make([]byte, math.MaxUint16)
		fs.out = make([]byte, 0, blockSize)
	} else {
		fs.dec.Reset()
	}
	fs.off = int64(fs.coffCabStart)
	fs.blocks = fs.cCFData
	fs.out = fs.out[:0]
	fs.outPos = 0
	fs.pos = 0
	return nil
}

// read reads uncompressed data from the current position into p.
func (fs *folderState) read(p []byte) (int, error) {
	if fs.outPos == len(fs.out) {
		if err := fs.nextBlock(); err != nil {
			return 0, err
		}
	}
	n := copy(p, fs.out[fs.outPos:])
	fs.outPos += n
	fs.pos += uint32(n)
	return n, nil
}

// nextBlock reads and decompresses the next CFDATA block.
func (fs *folderState) nextBlock() error {
	if fs.blocks == 0 {
		return io.ErrUnexpectedEOF
	}
	payload, cbUncomp, n, err := readCFDataBlock(fs.reader.r, fs.off, fs.cbCFData, fs.in, !fs.reader.SkipChecksum)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if cbUncomp == 0 || cbUncomp > blockSize {
		return fmt.Errorf("%w: invalid data block size", ErrFormat)
	}
	out, err := fs.dec.Decode(fs.out[:cbUncomp], payload)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFormat, err)
	}
	fs.off += n
	fs.blocks--
	fs.out = out
	fs.outPos = 0
	return nil
}
