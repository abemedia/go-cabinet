package cabinet_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/abemedia/go-cabinet"
)

func FuzzReader(f *testing.F) {
	paths, err := filepath.Glob("testdata/*.cab")
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}

	f.Fuzz(func(_ *testing.T, data []byte) {
		r, err := cabinet.NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}

		for _, file := range r.Files {
			rc, err := file.Open()
			if err != nil {
				continue
			}
			defer rc.Close()
			_, _ = io.Copy(io.Discard, rc)
		}
	})
}
