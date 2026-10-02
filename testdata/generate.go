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
	"encoding/binary"
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
	Compression       string     `json:"compression"`
	CompressionMemory int        `json:"compressionMemory"`
	Files             []FileSpec `json:"files"`
}

// Options sets per-cabinet makecab DDF directives. Zero values are omitted.
type Options struct {
	ReservePerCabinet   int `json:"reservePerCabinet"`
	ReservePerFolder    int `json:"reservePerFolder"`
	ReservePerDataBlock int `json:"reservePerDataBlock"`
}

type Fixture struct {
	Name    string   `json:"name"`
	Options Options  `json:"options"`
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
// "text" is prose and "code" resembles x86 machine code, both of which compress
// well. "binary" is random bytes, which do not compress.
func generateContent(fixtureName, fileName, typ string, size int) []byte {
	if size == 0 {
		return []byte{}
	}
	h := fnv.New64a()
	h.Write([]byte(fixtureName + "/" + fileName))
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	switch typ {
	case "text":
		return generateText(rng, size)
	case "code":
		return generateCode(rng, size)
	}
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(rng.IntN(256))
	}
	return buf
}

var words = strings.Fields(`the of and to in is that it was for on are as with his they at be this from
have or by one had not but what all were when we there can an your which their said if do will each
about how up out them then she many some so these would other into has more her two like him see time
could no make than first been its who now people my made over did down only way find use may water
long little very after words called just where most know`)

// generateText returns prose built from a small vocabulary. Now and then it
// repeats a passage from anywhere earlier in the text, so that matches occur
// at long distances as well as short ones.
func generateText(rng *rand.Rand, size int) []byte {
	buf := make([]byte, 0, size+1024)
	for len(buf) < size {
		if len(buf) > 0 && rng.IntN(400) == 0 {
			start := rng.IntN(len(buf))
			start += bytes.IndexAny(buf[start:], " \n") + 1
			end := min(start+64+rng.IntN(960), len(buf))
			end += bytes.IndexAny(buf[end-1:], " \n")
			buf = append(buf, buf[start:end]...)
			continue
		}
		buf = append(buf, words[int(rng.ExpFloat64()*12)%len(words)]...)
		switch rng.IntN(16) {
		case 0:
			buf = append(buf, ".\n"...)
		case 1:
			buf = append(buf, ", "...)
		default:
			buf = append(buf, ' ')
		}
	}
	return buf[:size]
}

// generateCode returns data resembling x86 machine code: functions aligned to
// 16 bytes that call a small set of addresses, and tables of 16-byte records.
// The calls exercise LZX's E8 translation and the alignment its aligned offsets.
func generateCode(rng *rand.Rand, size int) []byte {
	instructions := [][]byte{
		{0x90},
		{0x31, 0xC0},
		{0x85, 0xC0},
		{0x74, 0x0C},
		{0x75, 0xF2},
		{0x8B, 0x45, 0xFC},
		{0x89, 0x45, 0xFC},
		{0x0F, 0xB6, 0x00},
		{0x48, 0x89, 0xC7},
		{0x48, 0x8B, 0x45, 0xF8},
		{0x48, 0x89, 0x45, 0xF0},
		{0x48, 0x8D, 0x4D, 0xE0},
	}
	prologue := []byte{0x55, 0x48, 0x89, 0xE5, 0x48, 0x83, 0xEC, 0x20}
	epilogue := []byte{0x48, 0x83, 0xC4, 0x20, 0x5D, 0xC3}
	targets := make([]int, 64)
	for i := range targets {
		targets[i] = rng.IntN(size) &^ 15
	}

	buf := make([]byte, 0, size+4096)
	for len(buf) < size {
		if rng.IntN(16) == 0 {
			base := uint32(rng.IntN(size))
			for i := range uint32(8 + rng.IntN(56)) {
				buf = binary.LittleEndian.AppendUint32(buf, base+24*i)
				buf = binary.LittleEndian.AppendUint32(buf, 0)
				buf = binary.LittleEndian.AppendUint16(buf, uint16(i))
				buf = append(buf, 0, 0, 0x40, 0, 0, 0)
			}
			continue
		}
		buf = append(buf, prologue...)
		for range 4 + rng.IntN(60) {
			if rng.IntN(6) == 0 {
				target := targets[rng.IntN(len(targets))]
				buf = append(buf, 0xE8)
				buf = binary.LittleEndian.AppendUint32(buf, uint32(target-len(buf)-4))
				continue
			}
			buf = append(buf, instructions[rng.IntN(len(instructions))]...)
		}
		buf = append(buf, epilogue...)
		for len(buf)%16 != 0 {
			buf = append(buf, 0xCC)
		}
	}
	return buf[:size]
}

// writeDDF writes a makecab Directive Definition File.
func writeDDF(ddfPath, cabPath, srcDir string, fix Fixture) error {
	const ddfTmpl = `.OPTION EXPLICIT
.Set CabinetNameTemplate={{.CabPath}}
.Set DiskDirectoryTemplate=
.Set Cabinet=on
{{if .ReservePerCabinet}}.Set ReservePerCabinetSize={{.ReservePerCabinet}}
{{end}}{{if .ReservePerFolder}}.Set ReservePerFolderSize={{.ReservePerFolder}}
{{end}}{{if .ReservePerDataBlock}}.Set ReservePerDataBlockSize={{.ReservePerDataBlock}}
{{end}}{{range $i, $e := .Entries}}{{if gt $i 0}}.New Folder
{{end}}.Set Compress={{if eq $e.Comp "NONE"}}off{{else}}on
.Set CompressionType={{$e.Comp}}{{end}}{{if $e.Memory}}
.Set CompressionMemory={{$e.Memory}}{{end}}
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
		Comp   string
		Memory int
		Files  []fileEntry
	}
	type data struct {
		CabPath             string
		ReservePerCabinet   int
		ReservePerFolder    int
		ReservePerDataBlock int
		Entries             []folderEntry
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
			Comp:   strings.ToUpper(folder.Compression),
			Memory: folder.CompressionMemory,
			Files:  files,
		})
	}

	tmpl := template.Must(template.New("ddf").Parse(ddfTmpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data{
		CabPath:             cabPath,
		ReservePerCabinet:   fix.Options.ReservePerCabinet,
		ReservePerFolder:    fix.Options.ReservePerFolder,
		ReservePerDataBlock: fix.Options.ReservePerDataBlock,
		Entries:             entries,
	}); err != nil {
		return fmt.Errorf("execute ddf template: %w", err)
	}
	return os.WriteFile(ddfPath, buf.Bytes(), 0o644)
}
