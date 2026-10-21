package cabinet_test

import (
	"bytes"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/abemedia/go-cabinet"
)

// benchFiles are the Silesia corpus files the benchmarks use, with their SHA-256 checksums.
var benchFiles = []struct{ name, sum string }{
	{"ooffice", "e7ee013880d34dd5208283d0d3d91b07f442e067454276095ded14f322a656eb"}, // Windows DLL
	{"xml", "0e82e54e695c1938e4193448022543845b33020c8be6bf3bf3ead2224903e08c"},     // tar of XML files
	{"dickens", "b24c37886142e11d0ee687db6ab06f936207aa7f2ea1fd1d9a36763c7a507e6a"}, // English text
}

var benchMethods = []cabinet.Compression{cabinet.None, cabinet.MSZIP}

func BenchmarkWriter(b *testing.B) {
	for _, file := range benchFiles {
		data := readBenchFile(b, file.name, file.sum)
		for _, method := range benchMethods {
			b.Run(method.String()+"/"+file.name, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()

				var out discardSeeker
				for b.Loop() {
					out = discardSeeker{}
					w := cabinet.NewWriter(&out)
					w.SetCompression(method)
					if err := w.AddFS(fstest.MapFS{file.name: {Data: data}}); err != nil {
						b.Fatalf("AddFS: %v", err)
					}
					if err := w.Close(); err != nil {
						b.Fatalf("Close: %v", err)
					}
				}
				b.ReportMetric(100*float64(out.size)/float64(len(data)), "%size")
			})
		}
	}
}

func BenchmarkReader(b *testing.B) {
	for _, file := range benchFiles {
		data := readBenchFile(b, file.name, file.sum)
		for _, method := range benchMethods {
			b.Run(method.String()+"/"+file.name, func(b *testing.B) {
				var buf seekBuffer
				w := cabinet.NewWriter(&buf)
				w.SetCompression(method)
				if err := w.AddFS(fstest.MapFS{file.name: {Data: data}}); err != nil {
					b.Fatalf("AddFS: %v", err)
				}
				if err := w.Close(); err != nil {
					b.Fatalf("Close: %v", err)
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()

				for b.Loop() {
					r, err := cabinet.NewReader(bytes.NewReader(buf.buf))
					if err != nil {
						b.Fatalf("NewReader: %v", err)
					}
					rc, err := r.Files[0].Open()
					if err != nil {
						b.Fatalf("Open: %v", err)
					}
					if _, err := io.Copy(io.Discard, rc); err != nil {
						b.Fatalf("Copy: %v", err)
					}
					rc.Close()
				}
			})
		}
	}
}

// readBenchFile returns the named corpus file, downloading it if it is missing.
func readBenchFile(b *testing.B, name, sum string) []byte {
	b.Helper()
	path := filepath.Join("testdata", "bench", name)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = downloadBenchFile(b, name, path)
	} else if err != nil {
		b.Fatal(err)
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sum {
		b.Fatalf("%s: SHA-256 %x, want %s", path, got, sum)
	}
	return data
}

func downloadBenchFile(b *testing.B, name, path string) []byte {
	b.Helper()
	url := "https://sun.aei.polsl.pl/~sdeor/corpus/" + name + ".bz2"
	req, err := http.NewRequestWithContext(b.Context(), http.MethodGet, url, nil)
	if err != nil {
		b.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(bzip2.NewReader(resp.Body))
	if err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Fatal(err)
	}
	return data
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
