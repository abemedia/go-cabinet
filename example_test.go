package cabinet_test

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/abemedia/go-cabinet"
	"github.com/abemedia/go-cabinet/mszip"
)

func ExampleWriter() {
	// Create a file to write our archive to.
	f, err := os.CreateTemp("", "example-*.cab")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Create a new cabinet archive.
	w := cabinet.NewWriter(f)

	// Add some files to the archive.
	files := []struct {
		Name, Body string
	}{
		{"readme.txt", "This archive contains some text files."},
		{"gopher.txt", "Gopher names:\nGeorge\nGeoffrey\nGonzo"},
		{"todo.txt", "Get animal handling licence.\nWrite more examples."},
	}
	for _, file := range files {
		f, err := w.Create(file.Name)
		if err != nil {
			log.Fatal(err)
		}
		_, err = f.Write([]byte(file.Body))
		if err != nil {
			log.Fatal(err)
		}
	}

	// Make sure to check the error on Close.
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	// Output:
}

func ExampleReader() {
	// Open a cabinet archive for reading.
	r, err := cabinet.OpenReader("testdata/example.cab")
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()

	// Iterate through the files in the archive,
	// printing some of their contents.
	for _, f := range r.Files {
		fmt.Printf("Contents of %s:\n", f.Name)
		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
		}
		_, err = io.Copy(os.Stdout, rc)
		if err != nil {
			log.Fatal(err)
		}
		rc.Close()
		fmt.Println()
	}
	// Output:
	// Contents of README.md:
	// This is an example cabinet file.
}

func ExampleWriter_RegisterCompressor() {
	// Override the default MS-ZIP compressor with a higher compression level.

	// Create a file to write our archive to.
	f, err := os.CreateTemp("", "example-*.cab")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Create a new cabinet archive.
	w := cabinet.NewWriter(f)

	// Register a custom MS-ZIP compressor.
	w.RegisterCompressor(cabinet.MSZip, func(out io.Writer) (io.WriteCloser, error) {
		return mszip.NewWriter(out, mszip.BestCompression)
	})

	// Proceed to add files to w.

	// Output:
}
