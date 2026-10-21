package lzx_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cabinet/lzx"
)

// benchFiles are the Silesia corpus files that the cabinet benchmarks download.
var benchFiles = []string{"ooffice", "xml", "dickens"}

func BenchmarkEncoder(b *testing.B) {
	for _, name := range benchFiles {
		for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
			b.Run(fmt.Sprintf("%s/window=%d", name, window), func(b *testing.B) {
				data := readBenchFile(b, name)
				e, err := lzx.NewEncoder(window)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()

				var frame []byte
				var size int
				for b.Loop() {
					e.Reset()
					size = 0
					for off := 0; off < len(data); off += frameSize {
						frame, err = e.Encode(frame[:0], data[off:min(off+frameSize, len(data))])
						if err != nil {
							b.Fatalf("Encode: %v", err)
						}
						size += len(frame)
					}
				}
				b.ReportMetric(100*float64(size)/float64(len(data)), "%size")
			})
		}
	}
}

func BenchmarkDecoder(b *testing.B) {
	for _, name := range benchFiles {
		for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
			b.Run(fmt.Sprintf("%s/window=%d", name, window), func(b *testing.B) {
				data := readBenchFile(b, name)
				frames := encode(b, data, window)
				d, err := lzx.NewDecoder(window)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()

				dst := make([]byte, frameSize)
				for b.Loop() {
					d.Reset()
					for i, frame := range frames {
						n := min(frameSize, len(data)-i*frameSize)
						if _, err := d.Decode(dst[:n], frame); err != nil {
							b.Fatalf("Decode frame %d: %v", i, err)
						}
					}
				}
			})
		}
	}
}

// readBenchFile reads a Silesia corpus file, skipping the benchmark if the
// cabinet benchmarks have not downloaded it.
func readBenchFile(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "bench", name))
	if errors.Is(err, fs.ErrNotExist) {
		b.Skipf("%s not found; run the cabinet benchmarks with -download to fetch it", name)
	}
	if err != nil {
		b.Fatal(err)
	}
	return data
}
