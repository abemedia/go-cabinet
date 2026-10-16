package cabinet_test

import (
	"bytes"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cabinet"
)

var download = flag.Bool("download", false, "download missing benchmark files from the Silesia corpus")

// CabBench represents a benchmark case.
type CabBench struct {
	Name    string
	Comp    cabinet.Compression
	File    string
	Content []byte
	Cabinet []byte
}

// loadAllCabBenchmarks loads a CabBench for each Silesia corpus file and compression method.
func loadAllCabBenchmarks(b *testing.B) []CabBench {
	b.Helper()

	const dir = "testdata/bench"
	files := []struct{ name, sum string }{
		{"ooffice", "e7ee013880d34dd5208283d0d3d91b07f442e067454276095ded14f322a656eb"}, // Windows DLL
		{"xml", "0e82e54e695c1938e4193448022543845b33020c8be6bf3bf3ead2224903e08c"},     // tar of XML files
		{"dickens", "b24c37886142e11d0ee687db6ab06f936207aa7f2ea1fd1d9a36763c7a507e6a"}, // English text
	}
	methods := []cabinet.Compression{cabinet.None, cabinet.MSZIP, cabinet.LZX}

	benches := make([]CabBench, 0, len(files)*len(methods))
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		content, err := os.ReadFile(path)
		missing := errors.Is(err, fs.ErrNotExist)
		if missing { //nolint:nestif
			if !*download {
				b.Skipf("%s not found; run with -download to fetch the benchmark files", path)
			}
			url := "https://sun.aei.polsl.pl/~sdeor/corpus/" + file.name + ".bz2"
			req, err := http.NewRequestWithContext(b.Context(), http.MethodGet, url, nil)
			if err != nil {
				b.Fatalf("download %s: %v", file.name, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				b.Fatalf("download %s: %v", file.name, err)
			}
			if resp.StatusCode != http.StatusOK {
				b.Fatalf("download %s: %s", file.name, resp.Status)
			}
			content, err = io.ReadAll(bzip2.NewReader(resp.Body))
			resp.Body.Close()
			if err != nil {
				b.Fatalf("download %s: %v", file.name, err)
			}
		} else if err != nil {
			b.Fatalf("read %s: %v", path, err)
		}
		if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != file.sum {
			b.Fatalf("%s: unexpected SHA-256 %x", file.name, sum)
		}
		if missing {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatalf("create %s: %v", dir, err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				b.Fatalf("write %s: %v", path, err)
			}
		}

		for _, method := range methods {
			cabPath := path + "." + strings.ToLower(method.String()) + ".cab"
			cab, err := os.ReadFile(cabPath)
			if err != nil {
				var buf seekBuffer
				w := cabinet.NewWriter(&buf)
				if err := w.SetCompression(method); err != nil {
					b.Fatalf("SetCompression: %v", err)
				}
				if err := w.AddFS(fstest.MapFS{file.name: {Data: content}}); err != nil {
					b.Fatalf("AddFS: %v", err)
				}
				if err := w.Close(); err != nil {
					b.Fatalf("Close: %v", err)
				}
				if err := os.WriteFile(cabPath, buf.buf, 0o644); err != nil {
					b.Fatalf("write %s: %v", cabPath, err)
				}
				cab = buf.buf
			}
			benches = append(benches, CabBench{
				Name:    method.String() + "/" + file.name,
				Comp:    method,
				File:    file.name,
				Content: content,
				Cabinet: cab,
			})
		}
	}
	return benches
}

// BenchmarkReader reads the file in each cached cabinet.
func BenchmarkReader(b *testing.B) {
	benches := loadAllCabBenchmarks(b)

	for _, bench := range benches {
		b.Run(bench.Name, func(b *testing.B) {
			b.SetBytes(int64(len(bench.Content)))
			b.ReportAllocs()

			var out discardSeeker
			buf := make([]byte, 32<<10)
			for b.Loop() {
				r, err := cabinet.NewReader(bytes.NewReader(bench.Cabinet))
				if err != nil {
					b.Fatalf("NewReader: %v", err)
				}
				rc, err := r.Files[0].Open()
				if err != nil {
					b.Fatalf("Open: %v", err)
				}
				n, err := io.CopyBuffer(&out, rc, buf)
				if err != nil {
					b.Fatalf("Copy: %v", err)
				}
				if n != int64(len(bench.Content)) {
					b.Fatalf("read %d bytes, want %d", n, len(bench.Content))
				}
				rc.Close()
			}
		})
	}
}

// BenchmarkWriter builds a cabinet from each corpus file using our Writer.
func BenchmarkWriter(b *testing.B) {
	benches := loadAllCabBenchmarks(b)

	for _, bench := range benches {
		b.Run(bench.Name, func(b *testing.B) {
			b.SetBytes(int64(len(bench.Content)))
			b.ReportAllocs()

			var out discardSeeker
			for b.Loop() {
				out = discardSeeker{}
				w := cabinet.NewWriter(&out)
				if err := w.SetCompression(bench.Comp); err != nil {
					b.Fatalf("SetCompression: %v", err)
				}
				if err := w.AddFS(fstest.MapFS{bench.File: {Data: bench.Content}}); err != nil {
					b.Fatalf("AddFS: %v", err)
				}
				if err := w.Close(); err != nil {
					b.Fatalf("Close: %v", err)
				}
			}
			b.ReportMetric(100*float64(out.size)/float64(len(bench.Content)), "%size")
		})
	}
}

// discardSeeker is an io.WriteSeeker that only records how much was written.
type discardSeeker struct{ pos, size int64 }

func (d *discardSeeker) Write(p []byte) (int, error) {
	d.pos += int64(len(p))
	d.size = max(d.size, d.pos)
	return len(p), nil
}

func (d *discardSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += d.pos
	case io.SeekEnd:
		offset += d.size
	}
	d.pos = offset
	return d.pos, nil
}
