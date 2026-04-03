package cabinet_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cabinet"
)

// TestFS exercises the fs.FS interface on a multi-directory cabinet.
func TestFS(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	w.SetCompression(cabinet.MSZip)

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
	w.SetCompression(cabinet.MSZip)
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
	for i := len(r.Files) - 1; i >= 0; i-- {
		rc, err := r.Files[i].Open()
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
	w.SetCompression(cabinet.MSZip)
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
