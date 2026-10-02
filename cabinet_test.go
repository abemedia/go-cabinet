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
			compression: cabinet.MSZIP,
			files: []file{
				{"f1.txt", bytes.Repeat([]byte("hello compressed world "), 500)},
				{"f2.txt", bytes.Repeat([]byte("second file content "), 200)},
				{"f3.bin", bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 300)},
			},
		},
		{
			name:        "lzx",
			compression: cabinet.LZX,
			files: []file{
				{"f1.txt", bytes.Repeat([]byte("hello compressed world "), 5000)},
				{"f2.bin", bytes.Repeat([]byte{0xE8, 0x10, 0x00, 0x00, 0x00, 0x90}, 20000)},
				{"empty.txt", nil},
				{"f3.txt", []byte("short")},
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
			if err := w.SetCompression(tc.compression); err != nil {
				t.Fatal(err)
			}
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

// TestCompression verifies the constructors and accessors of Compression.
func TestCompression(t *testing.T) {
	tests := []struct {
		comp   cabinet.Compression
		typ    cabinet.Compression
		level  int
		memory int
		str    string
	}{
		{cabinet.None, cabinet.None, 0, 0, "NONE"},
		{cabinet.MSZIP, cabinet.MSZIP, 0, 0, "MSZIP"},
		{cabinet.Quantum, cabinet.Quantum, 0, 0, "QUANTUM"},
		{cabinet.LZX, cabinet.LZX, 0, 0, "LZX"},
		{cabinet.LZXWindow(21), cabinet.LZX, 0, 21, "LZX:21"},
		{cabinet.TypeLevelMemory(cabinet.Quantum, 3, 16), cabinet.Quantum, 3, 16, "QUANTUM:3,16"},
		{cabinet.Compression(5), cabinet.Compression(5), 0, 0, "Compression(5)"},
	}
	for _, tt := range tests {
		if got := tt.comp.Type(); got != tt.typ {
			t.Errorf("%#04x: Type() = %#04x, want %#04x", uint16(tt.comp), uint16(got), uint16(tt.typ))
		}
		if got := tt.comp.Level(); got != tt.level {
			t.Errorf("%#04x: Level() = %d, want %d", uint16(tt.comp), got, tt.level)
		}
		if got := tt.comp.Memory(); got != tt.memory {
			t.Errorf("%#04x: Memory() = %d, want %d", uint16(tt.comp), got, tt.memory)
		}
		if got := tt.comp.String(); got != tt.str {
			t.Errorf("%#04x: String() = %q, want %q", uint16(tt.comp), got, tt.str)
		}
	}
}
