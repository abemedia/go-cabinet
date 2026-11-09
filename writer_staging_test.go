package cabinet_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cabinet"
)

func TestFailedStagedWriteAfterFolderChange(t *testing.T) {
	for _, change := range []string{"flush", "compression"} {
		for _, next := range []string{"Create", "CreateHeader", "AddPath", "AddFS", "Close"} {
			t.Run(change+"/"+next, func(t *testing.T) {
				dst, err := os.CreateTemp(t.TempDir(), "archive-*.cab")
				if err != nil {
					t.Fatal(err)
				}
				defer dst.Close()
				w := cabinet.NewWriter(dst)
				first, err := w.Create("good.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = first.Write([]byte("kept")); err != nil {
					t.Fatal(err)
				}
				failed, err := w.Create("bad.txt")
				if err != nil {
					t.Fatal(err)
				}
				// Exercise Write's error path without allocating a multi-gigabyte file.
				cabinet.ExhaustStagedFileSizeLimit(w)
				if _, err = failed.Write([]byte("x")); err == nil {
					t.Fatal("expected size-limit error")
				}
				if change == "flush" {
					w.FlushFolder()
				} else if err = w.SetCompression(cabinet.None); err != nil {
					t.Fatal(err)
				}
				switch next {
				case "Create":
					_, err = w.Create("next.txt")
				case "CreateHeader":
					_, err = w.CreateHeader(&cabinet.FileHeader{Name: "next.txt"})
				case "AddPath":
					source := filepath.Join(t.TempDir(), "source.txt")
					if err = os.WriteFile(source, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					err = w.AddPath("next.txt", source)
				case "AddFS":
					err = w.AddFS(fstest.MapFS{"next.txt": &fstest.MapFile{}})
				case "Close":
					err = w.Close()
				}
				if err != nil {
					t.Fatal(err)
				}
				if next != "Close" {
					if err = w.Close(); err != nil {
						t.Fatal(err)
					}
				}
				encoded, err := os.ReadFile(dst.Name())
				if err != nil {
					t.Fatal(err)
				}
				r, err := cabinet.NewReader(bytes.NewReader(encoded))
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if next == "Close" {
					want = 1
				}
				if len(r.Files) != want {
					t.Fatalf("got %d files, want %d", len(r.Files), want)
				}
				for _, f := range r.Files {
					if f.Name == "bad.txt" {
						t.Fatal("failed file retained")
					}
					if f.Name == "good.txt" {
						rc, openErr := f.Open()
						if openErr != nil {
							t.Fatal(openErr)
						}
						data, readErr := io.ReadAll(rc)
						rc.Close()
						if readErr != nil {
							t.Fatal(readErr)
						}
						if string(data) != "kept" {
							t.Fatalf("good file changed: %q", data)
						}
					}
				}
			})
		}
	}
}
