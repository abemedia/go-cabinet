package cabinet_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/abemedia/go-cabinet"
)

// seekBuffer is an in-memory io.WriteSeeker.
type seekBuffer struct {
	buf []byte
	pos int
}

func (b *seekBuffer) Write(p []byte) (int, error) {
	need := b.pos + len(p)
	if need > len(b.buf) {
		b.buf = append(b.buf, make([]byte, need-len(b.buf))...)
	}
	copy(b.buf[b.pos:], p)
	b.pos += len(p)
	return len(p), nil
}

func (b *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = int64(b.pos) + offset
	case io.SeekEnd:
		abs = int64(len(b.buf)) + offset
	}
	if abs < 0 {
		return 0, errors.New("negative seek position")
	}
	b.pos = int(abs)
	return abs, nil
}

// TestRoundTrip writes files and reads them back, verifying names, order, and contents.
func TestRoundTrip(t *testing.T) {
	type file struct {
		name string
		data []byte
	}
	tests := []struct {
		name        string
		compression cabinet.Compression
		files       []file
	}{
		{
			name:        "none",
			compression: cabinet.None,
			files: []file{
				{"a.txt", []byte("hello world")},
				{"b/data.bin", bytes.Repeat([]byte{0x01, 0x02, 0x03}, 1000)},
				{"empty.txt", nil},
			},
		},
		{
			name:        "mszip",
			compression: cabinet.MSZip,
			files: []file{
				{"f1.txt", bytes.Repeat([]byte("hello compressed world "), 500)},
				{"f2.txt", bytes.Repeat([]byte("second file content "), 200)},
				{"f3.bin", bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 300)},
			},
		},
		{
			name:        "empty",
			compression: cabinet.None,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf seekBuffer
			w := cabinet.NewWriter(&buf)
			w.SetCompression(tc.compression)
			for _, f := range tc.files {
				wr, err := w.Create(f.name)
				if err != nil {
					t.Fatalf("Create %q: %v", f.name, err)
				}
				if _, err := wr.Write(f.data); err != nil {
					t.Fatalf("Write %q: %v", f.name, err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			if len(r.Files) != len(tc.files) {
				t.Fatalf("got %d files, want %d", len(r.Files), len(tc.files))
			}
			for i, f := range r.Files {
				want := tc.files[i]
				if f.Name != want.name {
					t.Errorf("file %d: name %q, want %q", i, f.Name, want.name)
				}
				rc, err := f.Open()
				if err != nil {
					t.Fatalf("Open %q: %v", f.Name, err)
				}
				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatalf("ReadAll %q: %v", f.Name, err)
				}
				if !bytes.Equal(got, want.data) {
					t.Errorf("file %d %q: content mismatch (got %d bytes, want %d)", i, f.Name, len(got), len(want.data))
				}
				rc.Close()
			}
		})
	}
}
