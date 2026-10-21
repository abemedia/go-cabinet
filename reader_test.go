package cabinet_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cabinet"
)

// TestFS exercises the fs.FS interface on a multi-directory cabinet.
func TestFS(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)

	fsys := fstest.MapFS{
		"a.txt":       &fstest.MapFile{Data: []byte("root file")},
		"sub/b.txt":   &fstest.MapFile{Data: []byte("sub file")},
		"sub/c.txt":   &fstest.MapFile{Data: []byte("another sub")},
		"sub/d/e.txt": &fstest.MapFile{Data: []byte("nested")},
	}
	if err := w.AddFS(fsys); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if err := fstest.TestFS(r, "a.txt", "sub/b.txt", "sub/c.txt", "sub/d/e.txt"); err != nil {
		t.Fatal(err)
	}
}

// TestOutOfOrder opens files in the same folder in reverse order.
func TestOutOfOrder(t *testing.T) {
	data := [][]byte{
		bytes.Repeat([]byte("first file data "), 100),
		bytes.Repeat([]byte("second file data "), 200),
		bytes.Repeat([]byte("third file data "), 150),
	}

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	for i, d := range data {
		name := strings.Repeat("x", i) + ".txt"
		wr, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wr.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	// All three files should be in the same folder.
	for _, f := range r.Files {
		if f.FolderIndex() != 0 {
			t.Fatalf("expected all files in folder 0, got folder %d", f.FolderIndex())
		}
	}

	// Read in reverse order.
	for i, f := range slices.Backward(r.Files) {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, data[i]) {
			t.Errorf("file %d: content mismatch (got %d bytes, want %d)", i, len(got), len(data[i]))
		}
	}
}

// TestChecksum corrupts the stored checksum of a CFDATA block and
// verifies that checksum verification and SkipChecksum behave correctly.
func TestChecksum(t *testing.T) {
	tests := []struct {
		name         string
		skipChecksum bool
		wantErr      error
	}{
		{name: "verify", skipChecksum: false, wantErr: cabinet.ErrChecksum},
		{name: "skip", skipChecksum: true, wantErr: nil},
	}

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	wr, _ := w.Create("test.txt")
	if _, err := wr.Write(bytes.Repeat([]byte("checksum test data "), 200)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, len(buf.buf))
	copy(data, buf.buf)
	// The first CFFOLDER entry starts at byte 36 of the cabinet header. Its
	// first field (coffCabStart, 4 bytes little-endian) is the absolute offset
	// of the first CFDATA block. Corrupt the first byte of the stored checksum.
	cfDataOff := int(data[36]) | int(data[37])<<8 | int(data[38])<<16 | int(data[39])<<24
	data[cfDataOff] ^= 0xFF

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, err := cabinet.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			r.SkipChecksum = test.skipChecksum
			rc, err := r.Files[0].Open()
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			_, err = io.ReadAll(rc)
			if err != test.wantErr {
				t.Errorf("got error %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestReaderErrors verifies the errors for a cabinet whose folder or data block is corrupt.
func TestReaderErrors(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	if err := w.SetCompression(cabinet.None); err != nil {
		t.Fatal(err)
	}
	wr, err := w.Create("test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	src := buf.buf

	tests := []struct {
		name   string
		offset int
		data   uint16
		err    error
	}{
		// The first CFFOLDER entry follows the 36-byte header, with the compression type at offset 6.
		{"unknown compression type", 42, 5, cabinet.ErrAlgorithm},
		{"invalid LZX window", 42, uint16(cabinet.LZXWindow(14)), cabinet.ErrFormat},
		// The CFFOLDER entry starts with the offset of its first CFDATA block, whose uncompressed size is at offset 6.
		{"block size mismatch", int(binary.LittleEndian.Uint32(src[36:])) + 6, 3, cabinet.ErrFormat},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := bytes.Clone(src)
			binary.LittleEndian.PutUint16(data[test.offset:], test.data)
			r, err := cabinet.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			r.SkipChecksum = true
			rc, err := r.Files[0].Open()
			if err == nil {
				_, err = io.ReadAll(rc)
				rc.Close()
			}
			if !errors.Is(err, test.err) {
				t.Errorf("got error %v, want %v", err, test.err)
			}
		})
	}
}

// TestReader_RegisterDecompressor verifies that a Reader's decompressor overrides the package-level one.
func TestReader_RegisterDecompressor(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	if _, err := w.Create("test.txt"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	orig, _ := cabinet.Decompressors.Load(cabinet.MSZIP)
	t.Cleanup(func() { cabinet.Decompressors.Store(cabinet.MSZIP, orig) })

	errPkg, errReader := errors.New("package"), errors.New("reader")
	cabinet.RegisterDecompressor(cabinet.MSZIP, func(int, int) (cabinet.Decoder, error) { return nil, errPkg })

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Files[0].Open(); !errors.Is(err, errPkg) {
		t.Error("Open did not use the package-level decompressor")
	}
	r.RegisterDecompressor(cabinet.MSZIP, func(int, int) (cabinet.Decoder, error) { return nil, errReader })
	if _, err := r.Files[0].Open(); !errors.Is(err, errReader) {
		t.Error("Open did not use the Reader's decompressor")
	}
	r.RegisterDecompressor(cabinet.MSZIP, nil)
	if _, err := r.Files[0].Open(); !errors.Is(err, errPkg) {
		t.Error("Open did not use the package-level decompressor after restoring it")
	}
}

// TestFSConflicts verifies dirTree conflict resolution behaviour via Open.
func TestFSConflicts(t *testing.T) {
	tests := []struct {
		name  string
		files []string
	}{
		{"duplicate name", []string{"a.txt", "a.txt"}},
		{"file then directory conflict", []string{"a", "a/b"}},
		{"directory then file conflict", []string{"a/b", "a"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buf seekBuffer
			w := cabinet.NewWriter(&buf)
			for _, name := range test.files {
				wr, err := w.Create(name)
				if err != nil {
					t.Fatalf("Create %q: %v", name, err)
				}
				if _, err := wr.Write([]byte(name)); err != nil {
					t.Fatalf("Write %q: %v", name, err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			for _, name := range test.files {
				if _, err := r.Open(name); !errors.Is(err, cabinet.ErrDuplicate) {
					t.Errorf("Open(%q): want error %q, got %q", name, cabinet.ErrDuplicate, err)
				}
			}
		})
	}
}
