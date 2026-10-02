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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := cabinet.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			r.SkipChecksum = tt.skipChecksum
			rc, err := r.Files[0].Open()
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			_, err = io.ReadAll(rc)
			if err != tt.wantErr {
				t.Errorf("got error %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestBlockSizeMismatch corrupts the uncompressed size of a CFDATA block and
// verifies that reading it returns ErrFormat.
func TestBlockSizeMismatch(t *testing.T) {
	for _, comp := range []cabinet.Compression{cabinet.None, cabinet.MSZIP, cabinet.LZX} {
		t.Run(comp.String(), func(t *testing.T) {
			var buf seekBuffer
			w := cabinet.NewWriter(&buf)
			if err := w.SetCompression(comp); err != nil {
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

			// The uncompressed size is at offset 6 of the CFDATA block, whose offset
			// is the first field of the CFFOLDER entry following the 36-byte header.
			cfDataOff := int(binary.LittleEndian.Uint32(buf.buf[36:]))
			buf.buf[cfDataOff+6]--

			r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			r.SkipChecksum = true
			rc, err := r.Files[0].Open()
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			if _, err := io.ReadAll(rc); !errors.Is(err, cabinet.ErrFormat) {
				t.Fatalf("got error %v, want %v", err, cabinet.ErrFormat)
			}
		})
	}
}

// TestUnsupportedCompression verifies that opening a file in a folder with an
// unsupported compression method returns ErrAlgorithm.
func TestUnsupportedCompression(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
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

	// Mark the folder as Quantum compressed. The first CFFOLDER entry follows
	// the 36-byte header, with the compression type at offset 6.
	buf.buf[42] = 2

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := r.Files[0].Open(); !errors.Is(err, cabinet.ErrAlgorithm) {
		t.Fatalf("got error %v, want %v", err, cabinet.ErrAlgorithm)
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf seekBuffer
			w := cabinet.NewWriter(&buf)
			for _, name := range tt.files {
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

			for _, name := range tt.files {
				if _, err := r.Open(name); !errors.Is(err, cabinet.ErrDuplicate) {
					t.Errorf("Open(%q): want error %q, got %q", name, cabinet.ErrDuplicate, err)
				}
			}
		})
	}
}
