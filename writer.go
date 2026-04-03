package cabinet

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"runtime"
	"time"
	"unicode/utf8"
)

var errClosed = errors.New("cabinet: writer is closed")

// Writer implements a Cabinet file writer.
//
// Nothing is written to the underlying [io.WriteSeeker] until [Writer.Close] is called.
type Writer struct {
	w           io.WriteSeeker
	compressors map[Compression]Compressor

	folders []*folderEntry
	current *folderEntry

	stagingLW    *limitWriter // open staged-file writer (nil if none)
	stagingEntry *fileEntry   // entry whose size is pending

	tmpDir  string // lazily-created temp directory for staged files
	cleanup runtime.Cleanup
	closed  bool
}

// NewWriter returns a new [Writer] writing to w.
func NewWriter(w io.WriteSeeker) *Writer {
	return &Writer{
		w:           w,
		compressors: map[Compression]Compressor{},
		current:     &folderEntry{method: MSZip},
	}
}

// RegisterCompressor registers or overrides a compressor for the given method.
// If a compressor for a given method is not found, [Writer] will default to
// looking up the compressor at the package level.
func (w *Writer) RegisterCompressor(method Compression, comp Compressor) {
	w.compressors[method] = comp
}

// SetCompression sets the compression method for subsequently added files.
// If the current folder already contains files, it starts a new folder.
func (w *Writer) SetCompression(c Compression) {
	if c == w.current.method {
		return
	}
	if len(w.current.files) > 0 {
		w.FlushFolder()
	}
	w.current.method = c
}

// FlushFolder starts a new folder. It is a no-op if the current folder is empty.
func (w *Writer) FlushFolder() {
	if len(w.current.files) == 0 {
		return
	}
	w.folders = append(w.folders, w.current)
	w.current = &folderEntry{method: w.current.method}
}

// Create adds a file to the archive with the given name and returns an
// [io.Writer] to which the file contents should be written. The file's
// contents must be written before the next call to [Writer.Create],
// [Writer.CreateHeader], or [Writer.Close].
func (w *Writer) Create(name string) (io.Writer, error) {
	return w.CreateHeader(&FileHeader{
		Name:     name,
		Modified: time.Now(),
		NonUTF8:  isNonUTF8(name),
	})
}

// CreateHeader adds a file to the archive using the provided [FileHeader] for
// the file metadata.
//
// This returns an [io.Writer] to which the file contents should be written.
// The file's contents must be written before the next call to
// [Writer.Create], [Writer.CreateHeader], or [Writer.Close].
func (w *Writer) CreateHeader(fh *FileHeader) (io.Writer, error) {
	if w.closed {
		return nil, errClosed
	}
	if err := w.closeStagedFile(); err != nil {
		return nil, err
	}

	if w.tmpDir == "" {
		dir, err := os.MkdirTemp("", "cab-stage-*")
		if err != nil {
			return nil, err
		}
		w.tmpDir = dir
		w.cleanup = runtime.AddCleanup(w, func(d string) { os.RemoveAll(d) }, dir)
	}

	tmp, err := os.CreateTemp(w.tmpDir, "f-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()

	lw := &limitWriter{f: tmp}
	w.stagingLW = lw

	entry := &fileEntry{
		fh: *fh,
		open: func() (io.ReadCloser, error) {
			return os.Open(tmpPath)
		},
	}
	w.current.files = append(w.current.files, entry)
	w.stagingEntry = entry
	return lw, nil
}

// AddPath adds a file or directory tree rooted at `path` to the archive.
// For directories, all files are added recursively with `name` as the path prefix.
func (w *Writer) AddPath(name, path string) error {
	if w.closed {
		return errClosed
	}
	if err := w.closeStagedFile(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		fsys := os.DirFS(path)
		return w.addFS(name, fsys)
	}
	return w.addFile(name, path, info, func(p string) (fs.File, error) { return os.Open(p) })
}

// AddFS adds the files from `fsys` to the archive, walking the [fs.FS]
// directory tree and maintaining the directory structure.
func (w *Writer) AddFS(fsys fs.FS) error {
	if w.closed {
		return errClosed
	}
	if err := w.closeStagedFile(); err != nil {
		return err
	}
	return w.addFS("", fsys)
}

// addFS recursively adds all files from fsys, prefixing each name with prefix.
func (w *Writer) addFS(prefix string, fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := path
		if prefix != "" {
			name = prefix + "/" + path
		}
		return w.addFile(name, path, info, fsys.Open)
	})
}

// addFile enqueues a single file entry in the current folder.
func (w *Writer) addFile(name, path string, info fs.FileInfo, open func(string) (fs.File, error)) error {
	size := info.Size()
	if size > math.MaxUint32 {
		return fmt.Errorf("cabinet: file %q too large (%d bytes)", name, size)
	}
	fh := FileHeader{
		Name:     name,
		Modified: info.ModTime(),
		NonUTF8:  isNonUTF8(name),
	}
	populateAttrs(&fh, info)
	w.current.files = append(w.current.files, &fileEntry{
		fh:   fh,
		size: uint32(size),
		open: func() (io.ReadCloser, error) { return open(path) },
	})
	return nil
}

// Close finalizes and writes the archive. It does not close the underlying writer.
func (w *Writer) Close() error {
	if w.closed {
		return errClosed
	}
	w.closed = true
	defer func() {
		w.cleanup.Stop()
		os.RemoveAll(w.tmpDir)
	}()

	if err := w.closeStagedFile(); err != nil {
		return err
	}

	finalFolders := w.planFolders()

	if len(finalFolders) > math.MaxUint16 {
		return fmt.Errorf("cabinet: too many folders (%d, max %d)", len(finalFolders), math.MaxUint16)
	}
	var totalFiles int
	for _, ff := range finalFolders {
		totalFiles += len(ff.files)
	}
	if totalFiles > math.MaxUint16 {
		return fmt.Errorf("cabinet: too many files (%d, max %d)", totalFiles, math.MaxUint16)
	}

	compFuncs, err := w.resolveCompressors(finalFolders)
	if err != nil {
		return err
	}

	dataOffset := int64(cfHeaderSize + len(finalFolders)*cfFolderSize)
	for _, ff := range finalFolders {
		for _, f := range ff.files {
			dataOffset += int64(cfFileSize) + int64(len(f.fh.Name)) + 1
		}
	}
	if _, err := w.w.Seek(dataOffset, io.SeekStart); err != nil {
		return err
	}

	written := dataOffset
	for fi, ff := range finalFolders {
		ff.coffCabStart = uint32(written)
		cCFData, n, err := w.compressFolder(ff.files, compFuncs[fi])
		if err != nil {
			return err
		}
		written += n
		if written > math.MaxUint32 {
			return errors.New("cabinet: archive exceeds 4 GiB limit")
		}
		ff.cCFData = cCFData
		finalFolders[fi] = ff
	}

	if _, err := w.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return w.writeHeaders(finalFolders, uint32(written))
}

// closeStagedFile finalises the currently open staged file write, recording its size.
func (w *Writer) closeStagedFile() error {
	if w.stagingLW == nil {
		return nil
	}
	lw := w.stagingLW
	w.stagingLW = nil
	if lw.err != nil {
		// The error was already returned by Write; drop the broken entry and
		// continue so the next CreateHeader/Close call is not affected.
		lw.f.Close()
		w.current.files = w.current.files[:len(w.current.files)-1]
		w.stagingEntry = nil
		return nil //nolint:nilerr
	}
	w.stagingEntry.size = lw.n
	w.stagingEntry = nil
	return lw.f.Close()
}

// planFolders collects and flattens all staged folders, applying auto-split.
func (w *Writer) planFolders() []*folderEntry {
	all := w.folders
	if len(w.current.files) > 0 {
		all = append(all, w.current)
	}

	final := make([]*folderEntry, 0, len(all))
	for _, fe := range all {
		start := 0
		var offset uint32
		for i, f := range fe.files {
			if i > start && uint64(offset)+uint64(f.size) > autoSplitThreshold {
				final = append(final, &folderEntry{method: fe.method, files: fe.files[start:i]})
				start = i
				offset = 0
			}
			f.uoffFolderStart = offset
			f.folderIdx = uint16(len(final))
			offset += f.size
		}
		if start < len(fe.files) {
			final = append(final, &folderEntry{method: fe.method, files: fe.files[start:]})
		}
	}
	return final
}

// writeHeaders writes the complete CFHEADER, CFFOLDER array, and CFFILE array
// to w.w. coffCabStart and cCFData must be set on each folderEntry before calling.
func (w *Writer) writeHeaders(folders []*folderEntry, cbCabinet uint32) error {
	nFolders := len(folders)
	filesOffset := int64(cfHeaderSize + nFolders*cfFolderSize)

	var nFiles int
	var setID uint16
	for _, ff := range folders {
		nFiles += len(ff.files)
		for _, f := range ff.files {
			for _, c := range []byte(slashToBackslash(f.fh.Name)) {
				setID += uint16(c)
			}
		}
	}

	bw := bufio.NewWriter(w.w)
	if err := writeCFHeader(bw, cfHeader{
		Signature:    cabinetSignature,
		CbCabinet:    cbCabinet,
		CoffFiles:    uint32(filesOffset),
		VersionMinor: 3,
		VersionMajor: 1,
		CFolders:     uint16(nFolders),
		CFiles:       uint16(nFiles),
		SetID:        setID,
	}); err != nil {
		return err
	}
	for _, ff := range folders {
		if err := writeCFFolder(bw, cfFolderRecord{
			CoffCabStart: ff.coffCabStart,
			CCFData:      ff.cCFData,
			TypeCompress: ff.method,
		}); err != nil {
			return err
		}
	}
	for _, ff := range folders {
		for _, f := range ff.files {
			dosDate, dosTime := encodeDOSTime(f.fh.Modified)
			if err := writeCFFile(bw, cfFileRecord{
				CbFile:          f.size,
				UoffFolderStart: f.uoffFolderStart,
				IFolder:         f.folderIdx,
				Date:            dosDate,
				Time:            dosTime,
				Attribs:         headerToAttrs(&f.fh),
				Name:            slashToBackslash(f.fh.Name),
			}); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

// resolveCompressors looks up a compressor for each folder, returning an error
// if any method is unregistered.
func (w *Writer) resolveCompressors(folders []*folderEntry) ([]Compressor, error) {
	comps := make([]Compressor, len(folders))
	for i, ff := range folders {
		c := w.compressors[ff.method]
		if c == nil {
			c = compressor(ff.method)
		}
		if c == nil {
			return nil, fmt.Errorf("%w for method %s", ErrAlgorithm, ff.method)
		}
		comps[i] = c
	}
	return comps, nil
}

// compressFolder compresses all data from files using comp, writing CFDATA
// blocks to w.w. Returns the number of blocks written and total bytes written.
func (w *Writer) compressFolder(files []*fileEntry, comp Compressor) (cCFData uint16, written int64, err error) {
	var inputBuf [32768]byte
	var compBuf bytes.Buffer

	src := &seqFileReader{files: files}
	defer src.close()

	fw, err := comp(&compBuf)
	if err != nil {
		return 0, 0, fmt.Errorf("cabinet: create compressor: %w", err)
	}
	flusher, hasFlusher := fw.(interface{ Flush() error })

	for {
		n, rerr := io.ReadFull(src, inputBuf[:])
		if n > 0 { //nolint:nestif
			compBuf.Reset()
			if _, werr := fw.Write(inputBuf[:n]); werr != nil {
				return 0, 0, fmt.Errorf("cabinet: compress: %w", werr)
			}
			if hasFlusher {
				if ferr := flusher.Flush(); ferr != nil {
					return 0, 0, fmt.Errorf("cabinet: flush compressor: %w", ferr)
				}
			}
			if compBuf.Len() > 0 {
				n, err := writeCFDataBlock(w.w, compBuf.Bytes(), uint16(n))
				if err != nil {
					return 0, 0, err
				}
				written += n
				cCFData++
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return 0, 0, fmt.Errorf("cabinet: read source: %w", rerr)
		}
	}

	// Finalize the compressor; emit any trailing output.
	compBuf.Reset()
	if err := fw.Close(); err != nil {
		return 0, 0, fmt.Errorf("cabinet: close compressor: %w", err)
	}
	if compBuf.Len() > 0 {
		n, err := writeCFDataBlock(w.w, compBuf.Bytes(), 0)
		if err != nil {
			return 0, 0, err
		}
		written += n
		cCFData++
	}
	return cCFData, written, nil
}

// isNonUTF8 returns true if the file name should be marked as not UTF-8 encoded in the Cabinet format.
// This is the case for ASCII-only names (for compatibility with legacy tools) or names that are not valid UTF-8.
func isNonUTF8(name string) bool {
	return len(name) == len([]rune(name)) || !utf8.ValidString(name)
}

// seqFileReader opens and reads files one at a time, closing each after EOF,
// so only one file descriptor is open at any moment.
type seqFileReader struct {
	files   []*fileEntry
	current io.ReadCloser
}

func (s *seqFileReader) Read(p []byte) (int, error) {
	for {
		if s.current == nil {
			if len(s.files) == 0 {
				return 0, io.EOF
			}
			var err error
			f := s.files[0]
			s.files = s.files[1:]
			s.current, err = f.open()
			if err != nil {
				return 0, fmt.Errorf("cabinet: open %q: %w", f.fh.Name, err)
			}
		}
		n, err := s.current.Read(p)
		if err == io.EOF {
			s.current.Close()
			s.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *seqFileReader) close() {
	if s.current != nil {
		s.current.Close()
		s.current = nil
	}
}

// folderEntry represents a CFFOLDER group.
type folderEntry struct {
	method Compression
	files  []*fileEntry

	// Assigned during Close():
	coffCabStart uint32
	cCFData      uint16
}

// fileEntry represents a file queued for writing.
type fileEntry struct {
	fh   FileHeader
	size uint32
	open func() (io.ReadCloser, error)

	// Assigned during planFolders():
	uoffFolderStart uint32
	folderIdx       uint16
}

// limitWriter caps writes at MaxUint32 and counts bytes for staged files.
type limitWriter struct {
	f   *os.File
	n   uint32
	err error
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if int64(lw.n)+int64(len(p)) > math.MaxUint32 {
		lw.err = errors.New("cabinet: file too large (exceeds 4 GiB)")
		return 0, lw.err
	}
	n, err := lw.f.Write(p)
	lw.n += uint32(n)
	lw.err = err
	return n, lw.err
}

var autoSplitThreshold uint64 = 0x7FFF8000 // ~2 GB
