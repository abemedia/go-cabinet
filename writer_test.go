package cabinet_test

import (
	"bytes"
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

// TestAutoSplit verifies that a folder exceeding the threshold is split into multiple folders.
func TestAutoSplit(t *testing.T) {
	const threshold = 1 << 20 // 1 MB
	cabinet.SetAutoSplitThreshold(t, threshold)

	data := bytes.Repeat([]byte("x"), threshold/2+1) // just over half the threshold

	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	w.SetCompression(cabinet.None)
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

// TestAlgorithmError verifies that an unregistered compressor returns ErrAlgorithm.
func TestAlgorithmError(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	w.SetCompression(cabinet.Quantum)
	wr, err := w.Create("test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("expected ErrAlgorithm from Close, got nil")
	}
}

// TestDoubleClose verifies the second Close returns an error.
func TestDoubleClose(t *testing.T) {
	var buf seekBuffer
	w := cabinet.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("expected error on second Close, got nil")
	}
}
