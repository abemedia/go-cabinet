package cabinet_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abemedia/go-cabinet"
)

// CabTestFile represents a file in the test case.
type CabTestFile struct {
	Header  cabinet.FileHeader
	Content []byte
}

// CabTestFolder represents a folder in the test case.
type CabTestFolder struct {
	Comp  cabinet.Compression
	Files []CabTestFile
}

// CabTest represents a complete test case.
type CabTest struct {
	Name    string
	Folders []CabTestFolder
}

// loadAllCabTests loads all CabTest cases from fixtures.json.
func loadAllCabTests(t *testing.T) []CabTest {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures.json: %v", err)
	}

	var fixtures []struct {
		Name    string `json:"name"`
		Folders []struct {
			Compression string `json:"compression"`
			Files       []struct {
				Name  string    `json:"name"`
				Attrs string    `json:"attrs,omitempty"`
				MTime time.Time `json:"mtime,omitzero"`
			} `json:"files"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse fixtures.json: %v", err)
	}

	tests := make([]CabTest, 0, len(fixtures))
	for _, fixture := range fixtures {
		var test CabTest
		test.Name = fixture.Name
		for _, fixtureFolder := range fixture.Folders {
			var folder CabTestFolder

			switch strings.ToLower(fixtureFolder.Compression) {
			case "none":
				folder.Comp = cabinet.None
			case "mszip":
				folder.Comp = cabinet.MSZip
			case "lzx":
				folder.Comp = cabinet.LZX
			case "quantum":
				folder.Comp = cabinet.Quantum
			default:
				t.Fatalf("invalid compression: %s", fixtureFolder.Compression)
			}

			for _, fixtureFile := range fixtureFolder.Files {
				var file CabTestFile
				mtime := fixtureFile.MTime
				if mtime.IsZero() {
					mtime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				file.Header = cabinet.FileHeader{
					Name:     fixtureFile.Name,
					Modified: mtime.Truncate(2 * time.Second).UTC(),
					ReadOnly: strings.Contains(fixtureFile.Attrs, "R"),
					Hidden:   strings.Contains(fixtureFile.Attrs, "H"),
					System:   strings.Contains(fixtureFile.Attrs, "S"),
					Archive:  strings.Contains(fixtureFile.Attrs, "A"),
					NonUTF8:  true,
				}
				srcPath := filepath.Join("testdata", fixture.Name, filepath.FromSlash(fixtureFile.Name))
				content, err := os.ReadFile(srcPath)
				if err != nil {
					t.Fatalf("read source file %s: %v (run internal/testdata/generate.go on Windows to regenerate)", srcPath, err)
				}
				file.Content = content
				folder.Files = append(folder.Files, file)
			}
			test.Folders = append(test.Folders, folder)
		}
		tests = append(tests, test)
	}
	return tests
}

// TestReader opens each makecab-generated .cab and validates the Reader.
func TestReader(t *testing.T) {
	tests := loadAllCabTests(t)

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			cab, err := cabinet.OpenReader(filepath.Join("testdata", test.Name+".cab"))
			if err != nil {
				t.Fatalf("OpenReader: %v", err)
			}
			defer cab.Close()

			wantCount := 0
			for _, testFolder := range test.Folders {
				wantCount += len(testFolder.Files)
			}
			if len(cab.Files) != wantCount {
				t.Fatalf("file count: got %d, want %d", len(cab.Files), wantCount)
			}

			idx := 0
			for fi, testFolder := range test.Folders {
				for _, testFile := range testFolder.Files {
					f := cab.Files[idx]
					if f.FileHeader != testFile.Header {
						t.Errorf("file %d header mismatch:\n  got  %+v\n  want %+v", idx, f.FileHeader, testFile.Header)
					}
					if int(f.FolderIndex()) != fi {
						t.Errorf("%q FolderIndex: got %d, want %d", f.Name, f.FolderIndex(), fi)
					}

					rc2, err := f.Open()
					if err != nil {
						t.Errorf("%q Open: %v", f.Name, err)
						idx++
						continue
					}
					got, err := io.ReadAll(rc2)
					rc2.Close()
					if err != nil {
						t.Errorf("%q ReadAll: %v", f.Name, err)
						idx++
						continue
					}
					if !bytes.Equal(got, testFile.Content) {
						t.Errorf("%q content mismatch: got %d bytes, want %d bytes", f.Name, len(got), len(testFile.Content))
					}
					idx++
				}
			}
		})
	}
}

// TestWriter builds a cabinet from each fixture using our Writer and extracts
// it with an external tool, asserting byte-level equality with source files.
func TestWriter(t *testing.T) {
	tool := "cabextract"
	if runtime.GOOS == "windows" {
		tool = "expand.exe"
	}
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not found in PATH", tool)
	}

	tests := loadAllCabTests(t)

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			tmp, err := os.CreateTemp(t.TempDir(), "test-*.cab")
			if err != nil {
				t.Fatal(err)
			}
			w := cabinet.NewWriter(tmp)
			for _, testFolder := range test.Folders {
				w.FlushFolder()
				w.SetCompression(testFolder.Comp)
				for _, testFile := range testFolder.Files {
					wr, err := w.CreateHeader(&testFile.Header)
					if err != nil {
						t.Fatalf("CreateHeader %q: %v", testFile.Header.Name, err)
					}
					if _, err := wr.Write(testFile.Content); err != nil {
						t.Fatalf("Write: %v", err)
					}
				}
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := tmp.Close(); err != nil {
				t.Fatalf("close temp cab: %v", err)
			}

			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(t.Context(), "expand.exe", "-r", tmp.Name(), "-f:*", ".")
			} else {
				cmd = exec.CommandContext(t.Context(), "cabextract", tmp.Name())
			}
			out, err := cmd.CombinedOutput()
			t.Logf("%s output:\n%s", cmd.Path, out)
			if err != nil {
				t.Fatalf("%s error: %v", cmd.Path, err)
			}

			for _, testFolder := range test.Folders {
				for _, testFile := range testFolder.Files {
					got, err := os.ReadFile(testFile.Header.Name)
					if err != nil {
						t.Errorf("%q: read extracted file: %v", testFile.Header.Name, err)
						continue
					}
					if !bytes.Equal(got, testFile.Content) {
						t.Errorf(
							"%q: extracted content mismatch (%d bytes vs %d bytes)",
							testFile.Header.Name,
							len(got),
							len(testFile.Content),
						)
					}

					info, err := os.Stat(testFile.Header.Name)
					if err != nil {
						t.Errorf("%q: stat extracted file: %v", testFile.Header.Name, err)
						continue
					}

					var fh cabinet.FileHeader
					cabinet.PopulateAttrs(&fh, info)
					fh.Name = testFile.Header.Name
					fh.NonUTF8 = true

					// cabextract interprets DOS timestamps as local time, so the extracted file's mtime may be in a non-UTC zone.
					m := info.ModTime()
					fh.Modified = time.Date(m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute(), m.Second(), 0, time.UTC)

					// Hidden, System, and Archive have no Unix equivalent; only ReadOnly (file permission) is testable.
					if runtime.GOOS != "windows" {
						fh.Hidden = testFile.Header.Hidden
						fh.System = testFile.Header.System
						fh.Archive = testFile.Header.Archive
					}

					if fh != testFile.Header {
						t.Errorf("%q attrs mismatch:\n  got  %+v\n  want %+v", testFile.Header.Name, fh, testFile.Header)
					}
				}
			}
		})
	}
}
