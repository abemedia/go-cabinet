package cabinet_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"testing"
	"testing/fstest"
	"time"

	"github.com/abemedia/go-cabinet"
)

// TestWriter_CreateHeader verifies that FileHeader fields round-trip correctly.
func TestWriter_CreateHeader(t *testing.T) {
	fh := cabinet.FileHeader{
		Name:     "test.txt",
		Modified: time.Date(2024, 6, 15, 12, 30, 42, 0, time.UTC),
		ReadOnly: true,
		Hidden:   true,
		System:   true,
		Archive:  true,
		Exec:     true,
		NonUTF8:  true,
	}

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	wr, err := w.CreateHeader(&fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := r.Files[0].FileHeader
	if got != fh {
		t.Errorf("header mismatch:\ngot %+v\nwant %+v", got, fh)
	}
}

// TestWriter_AddFS verifies that AddFS adds all files from an fs.FS.
func TestWriter_AddFS(t *testing.T) {
	fsys := fstest.MapFS{
		"a.txt":       &fstest.MapFile{Data: []byte("hello")},
		"sub/b.txt":   &fstest.MapFile{Data: []byte("world")},
		"sub/c/d.txt": &fstest.MapFile{Data: []byte("nested")},
	}
	want := map[string][]byte{
		"a.txt":       []byte("hello"),
		"sub/b.txt":   []byte("world"),
		"sub/c/d.txt": []byte("nested"),
	}

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	if err := w.AddFS(fsys); err != nil {
		t.Fatalf("AddFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got := make(map[string][]byte, len(r.Files))
	for _, f := range r.Files {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Open %q: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("ReadAll %q: %v", f.Name, err)
		}
		got[f.Name] = data
	}
	if !maps.EqualFunc(got, want, bytes.Equal) {
		t.Errorf("files mismatch:\n got  %v\n want %v", got, want)
	}
}

// TestWriter_AddFS_Roundtrip verifies that AddFS over a Reader's fs.FS
// preserves FileHeader attribute fields.
func TestWriter_AddFS_Roundtrip(t *testing.T) {
	fh := cabinet.FileHeader{
		Name:     "test.txt",
		Modified: time.Date(2024, 6, 15, 12, 30, 42, 0, time.UTC),
		ReadOnly: true,
		Hidden:   true,
		System:   true,
		Archive:  true,
		Exec:     true,
		NonUTF8:  true,
	}

	var src seekBuffer
	w := cabinet.NewWriter(&src)
	wr, err := w.CreateHeader(&fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(src.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var dst seekBuffer
	w = cabinet.NewWriter(&dst)
	if err := w.AddFS(r); err != nil {
		t.Fatalf("AddFS: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err = cabinet.NewReader(bytes.NewReader(dst.buf))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.Files[0].FileHeader; got != fh {
		t.Errorf("header mismatch:\ngot %+v\nwant %+v", got, fh)
	}
}

// TestWriter_AddPath tests adding a single file and a directory tree.
func TestWriter_AddPath(t *testing.T) {
	tests := []struct {
		name  string
		arg   string
		files map[string][]byte
	}{
		{
			name:  "file",
			arg:   "hello.txt",
			files: map[string][]byte{"hello.txt": []byte("hello world")},
		},
		{
			name: "dir",
			arg:  "prefix",
			files: map[string][]byte{
				"prefix/a.txt":     []byte("file a"),
				"prefix/sub/b.txt": []byte("file b"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			for name, content := range test.files {
				if err := os.MkdirAll(path.Dir(name), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(name, content, 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			var buf seekBuffer
			w := cabinet.NewWriter(&buf)
			if err := w.AddPath(test.arg, test.arg); err != nil {
				t.Fatalf("AddPath: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			got := make(map[string][]byte, len(r.Files))
			for _, f := range r.Files {
				rc, err := f.Open()
				if err != nil {
					t.Fatalf("Open %q: %v", f.Name, err)
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					t.Fatalf("ReadAll %q: %v", f.Name, err)
				}
				got[f.Name] = data
			}
			if !maps.EqualFunc(got, test.files, bytes.Equal) {
				t.Errorf("files mismatch:\n got  %v\n want %v", got, test.files)
			}
		})
	}
}

// TestWriter_RegisterCompressor verifies that a Writer's compressor overrides the package-level one.
func TestWriter_RegisterCompressor(t *testing.T) {
	orig, _ := cabinet.Compressors.Load(cabinet.MSZIP)
	t.Cleanup(func() { cabinet.Compressors.Store(cabinet.MSZIP, orig) })

	errPkg, errWriter := errors.New("package"), errors.New("writer")
	cabinet.RegisterCompressor(cabinet.MSZIP, func(int, int) (cabinet.Encoder, error) { return nil, errPkg })

	w := cabinet.NewWriter(&seekBuffer{})
	if _, err := w.Create("test.txt"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, errPkg) {
		t.Error("package-level compressor not used")
	}

	w = cabinet.NewWriter(&seekBuffer{})
	w.RegisterCompressor(cabinet.MSZIP, func(int, int) (cabinet.Encoder, error) { return nil, errWriter })
	if _, err := w.Create("test.txt"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, errWriter) {
		t.Error("Writer's compressor not used")
	}
}

// TestWriter_SetCompression verifies that changing the compression starts a new folder.
func TestWriter_SetCompression(t *testing.T) {
	tests := []struct {
		comp   cabinet.Compression
		folder uint16
		want   cabinet.Compression
	}{
		{cabinet.MSZIP, 0, cabinet.MSZIP},
		{cabinet.LZX, 1, cabinet.LZXWindow(21)},
		{cabinet.LZXWindow(21), 1, cabinet.LZXWindow(21)},
		{cabinet.LZXWindow(15), 2, cabinet.LZXWindow(15)},
	}
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	for i, test := range tests {
		if err := w.SetCompression(test.comp); err != nil {
			t.Fatal(err)
		}
		wr, err := w.Create(fmt.Sprintf("file%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wr.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
	if err != nil {
		t.Fatal(err)
	}
	for i, test := range tests {
		if got := r.Files[i].FolderIndex(); got != test.folder {
			t.Errorf("file %d: folder index %d, want %d", i, got, test.folder)
		}
		if got := r.Files[i].Compression(); got != test.want {
			t.Errorf("file %d: Compression() = %s, want %s", i, got, test.want)
		}
	}
}

// TestWriter_SetCompression_Errors verifies that SetCompression rejects a value the writer cannot compress.
func TestWriter_SetCompression_Errors(t *testing.T) {
	tests := []struct {
		comp cabinet.Compression
		want string
	}{
		{cabinet.Compression(5), "cabinet: invalid compression type 5"},
		{cabinet.Compression(0x2001), "cabinet: reserved compression bits set"},
		{cabinet.TypeLevelMemory(cabinet.None, 1, 0), "cabinet: invalid None level: want 0"},
		{cabinet.TypeLevelMemory(cabinet.None, 0, 15), "cabinet: invalid None memory: want 0"},
		{cabinet.TypeLevelMemory(cabinet.MSZIP, 9, 0), "cabinet: invalid MSZIP level: want 0"},
		{cabinet.TypeLevelMemory(cabinet.MSZIP, 0, 15), "cabinet: invalid MSZIP memory: want 0"},
		{cabinet.TypeLevelMemory(cabinet.LZX, 1, 21), "cabinet: invalid LZX level: want 0"},
		{cabinet.LZXWindow(14), "cabinet: invalid LZX memory: want value in range [15, 21]"},
		{cabinet.LZXWindow(32), "cabinet: invalid LZX memory: want value in range [15, 21]"},
		{cabinet.TypeLevelMemory(cabinet.Quantum, 0, 10), "cabinet: invalid Quantum level: want value in range [1, 7]"},
		{cabinet.TypeLevelMemory(cabinet.Quantum, 1, 22), "cabinet: invalid Quantum memory: want value in range [10, 21]"},
		{cabinet.TypeLevelMemory(cabinet.Quantum, 1, 10), "cabinet: unsupported compression algorithm"},
	}
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	for _, test := range tests {
		if err := w.SetCompression(test.comp); err == nil || err.Error() != test.want {
			t.Errorf("SetCompression(%#v) = %v, want %q", test.comp, err, test.want)
		}
	}
}

// TestAutoSplit verifies that a folder exceeding the threshold is split into multiple folders.
func TestAutoSplit(t *testing.T) {
	const threshold = 1 << 20 // 1 MB
	cabinet.SetAutoSplitThreshold(t, threshold)

	data := bytes.Repeat([]byte("x"), threshold/2+1) // just over half the threshold

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		wr, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wr.Write(data); err != nil {
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

	// a.txt fits in folder 0; b.txt would exceed the threshold so splits to folder 1;
	// c.txt similarly splits to folder 2.
	if len(r.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(r.Files))
	}
	for i, f := range r.Files {
		if got := f.FolderIndex(); got != uint16(i) {
			t.Errorf("file %d (%q): expected folder %d, got %d", i, f.Name, i, got)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Open %q: %v", f.Name, err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("ReadAll %q: %v", f.Name, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("file %d (%q): content mismatch (%d bytes, want %d)", i, f.Name, len(got), len(data))
		}
	}
}

// TestWriterUseAfterClose verifies that every method that can fail returns ErrClosed after Close.
func TestWriterUseAfterClose(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("Close: got %v, want %v", err, cabinet.ErrClosed)
	}
	if err := w.SetCompression(cabinet.LZX); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("SetCompression: got %v, want %v", err, cabinet.ErrClosed)
	}
	if _, err := w.Create("a.txt"); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("Create: got %v, want %v", err, cabinet.ErrClosed)
	}
	if _, err := w.CreateHeader(&cabinet.FileHeader{Name: "a.txt"}); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("CreateHeader: got %v, want %v", err, cabinet.ErrClosed)
	}
	if err := w.AddFS(fstest.MapFS{}); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("AddFS: got %v, want %v", err, cabinet.ErrClosed)
	}
	if err := w.AddPath("a.txt", "writer_test.go"); !errors.Is(err, cabinet.ErrClosed) {
		t.Errorf("AddPath: got %v, want %v", err, cabinet.ErrClosed)
	}
}
