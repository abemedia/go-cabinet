//go:build ignore

// generate.go creates the testdata fixture .cab files using makecab.exe.
// Run on a Windows machine with makecab.exe in PATH:
//
//	go run ./testdata/generate.go
//
// It reads testdata/fixtures.json, generates source files, invokes makecab
// for each fixture, and copies the resulting .cab files into testdata/.
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"
)

type FileSpec struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int       `json:"size"`
	Content string    `json:"content"`
	Attrs   string    `json:"attrs"`
	MTime   time.Time `json:"mtime"`
}

type Folder struct {
	Compression string     `json:"compression"`
	Files       []FileSpec `json:"files"`
}

type Fixture struct {
	Name    string   `json:"name"`
	Folders []Folder `json:"folders"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run() error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("this generator must be run on Windows with makecab.exe available")
	}

	dir, err := sourceDir()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		return err
	}

	var fixtures []Fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		return fmt.Errorf("parse fixtures.json: %w", err)
	}

	// Clean up old testdata directories and .cab files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".cab") {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}

	for _, fix := range fixtures {
		fmt.Printf("Generating fixture %q...\n", fix.Name)
		if err := generateFixture(dir, fix); err != nil {
			return err
		}
	}
	fmt.Println("Done.")
	return nil
}

func sourceDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("could not determine source file location")
	}
	return filepath.Dir(file), nil
}

func generateFixture(testdataDir string, fix Fixture) error {
	srcDir := filepath.Join(testdataDir, fix.Name)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}

	// Generate source files.
	for _, folder := range fix.Folders {
		for _, f := range folder.Files {
			path := filepath.Join(srcDir, filepath.FromSlash(f.Name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if strings.Contains(f.Attrs, "R") {
				perm = 0o444
			}
			content := []byte(f.Content)
			if len(content) == 0 {
				content = generateContent(fix.Name, f.Name, f.Type, f.Size)
			}
			if err := os.WriteFile(path, content, perm); err != nil {
				return err
			}
			mtime := cmp.Or(f.MTime, time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)).Truncate(2 * time.Second)
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				return err
			}
		}
	}

	// Write .ddf directive file and run makecab.
	ddfPath := filepath.Join(testdataDir, fix.Name+".ddf")
	cabPath := filepath.Join(testdataDir, fix.Name+".cab")
	if err := writeDDF(ddfPath, cabPath, srcDir, fix); err != nil {
		return err
	}

	cmd := exec.Command("makecab", "/F", ddfPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("makecab for %s: %w", fix.Name, err)
	}

	// Clean up .ddf and intermediate files.
	os.Remove(ddfPath)
	os.Remove("setup.inf")
	os.Remove("setup.rpt")
	return nil
}

// generateContent returns deterministic pseudo-random content for a fixture file.
// The output is fully determined by fixtureName+fileName so re-running the generator
// produces identical source files and identical .cab outputs.
// "text" uses printable ASCII, "binary" uses raw bytes.
func generateContent(fixtureName, fileName, typ string, size int) []byte {
	if size == 0 {
		return []byte{}
	}
	h := fnv.New64a()
	h.Write([]byte(fixtureName + "/" + fileName))
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(rng.IntN(256))
	}
	if typ == "text" {
		const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n"
		for i, b := range buf {
			buf[i] = charset[int(b)%len(charset)]
		}
	}
	return buf
}

// writeDDF writes a makecab Directive Definition File.
func writeDDF(ddfPath, cabPath, srcDir string, fix Fixture) error {
	const ddfTmpl = `.OPTION EXPLICIT
.Set CabinetNameTemplate={{.CabPath}}
.Set DiskDirectoryTemplate=
.Set Cabinet=on
{{range $i, $e := .Entries}}{{if gt $i 0}}.New Folder
{{end}}.Set Compress={{if eq $e.Comp "NONE"}}off{{else}}on
.Set CompressionType={{$e.Comp}}{{end}}
{{range $e.Files}}.Set DestinationDir="{{.Dir}}"
"{{.Src}}" "{{.Base}}" /attr={{.Attr}}
{{end}}{{end}}`

	type fileEntry struct {
		Src  string
		Dir  string
		Base string
		Attr string
	}
	type folderEntry struct {
		Comp  string
		Files []fileEntry
	}
	type data struct {
		CabPath string
		Entries []folderEntry
	}

	entries := make([]folderEntry, 0, len(fix.Folders))
	for _, folder := range fix.Folders {
		var files []fileEntry
		for _, f := range folder.Files {
			src := filepath.Join(srcDir, filepath.FromSlash(f.Name))
			dir := filepath.Dir(filepath.FromSlash(f.Name))
			if dir == "." {
				dir = ""
			}
			files = append(files, fileEntry{
				Src:  src,
				Dir:  dir,
				Base: filepath.Base(f.Name),
				Attr: strings.ToUpper(f.Attrs),
			})
		}
		entries = append(entries, folderEntry{
			Comp:  strings.ToUpper(folder.Compression),
			Files: files,
		})
	}

	tmpl := template.Must(template.New("ddf").Parse(ddfTmpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data{CabPath: cabPath, Entries: entries}); err != nil {
		return fmt.Errorf("execute ddf template: %w", err)
	}
	return os.WriteFile(ddfPath, buf.Bytes(), 0o644)
}
