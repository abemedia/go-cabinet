package mszip_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/abemedia/go-cabinet/mszip"
)

// TestRoundTrip verifies basic compress/decompress with CK prefix.
func TestRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("hello world this is a test of ms-zip compression "), 50)

	var buf bytes.Buffer
	w, err := mszip.NewWriter(&buf, mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify CK signature at start.
	if !bytes.HasPrefix(buf.Bytes(), []byte("CK")) {
		t.Fatalf("expected CK prefix, got %v", buf.Bytes()[:2])
	}

	r := mszip.NewReader(&buf)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(got), len(data))
	}
}

// TestMultiBlock verifies cross-block dictionary seeding with input > 32KB.
func TestMultiBlock(t *testing.T) {
	// 100KB of repetitive text (compressible, exercises dictionary seeding).
	base := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 2860)
	data := base[:100*1024]

	var buf bytes.Buffer
	w, err := mszip.NewWriter(&buf, mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Count CK prefixes to verify multiple blocks were emitted.
	// With 100KB input we expect at least 3 full blocks (3*32768 = 98304).
	count := bytes.Count(buf.Bytes(), []byte("CK"))
	if count < 3 {
		t.Errorf("expected at least 3 CK-prefixed blocks, found %d", count)
	}

	r := mszip.NewReader(&buf)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(got), len(data))
	}
}

// TestArbitraryWriteSizes verifies that many small writes produce the same
// compressed output as a single large write.
func TestArbitraryWriteSizes(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 2100) // ~54KB

	// Single large write.
	var buf1 bytes.Buffer
	w1, err := mszip.NewWriter(&buf1, mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w1.Write(data); err != nil {
		t.Fatalf("single write: %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("single close: %v", err)
	}

	// Many small writes (1-byte, 7-byte, 1000-byte pattern).
	var buf2 bytes.Buffer
	w2, err := mszip.NewWriter(&buf2, mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	rest := data
	sizes := []int{1, 7, 1000}
	for si := 0; len(rest) > 0; si++ {
		n := min(sizes[si%len(sizes)], len(rest))
		if _, err := w2.Write(rest[:n]); err != nil {
			t.Fatalf("small write: %v", err)
		}
		rest = rest[n:]
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("multi close: %v", err)
	}

	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Errorf(
			"compressed output differs between single write (%d bytes) and many small writes (%d bytes)",
			buf1.Len(),
			buf2.Len(),
		)
	}
}

// TestEmptyInput verifies that compressing and decompressing zero bytes works.
func TestEmptyInput(t *testing.T) {
	var buf bytes.Buffer
	w, err := mszip.NewWriter(&buf, mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// No blocks should have been emitted for empty input.
	if buf.Len() != 0 {
		t.Errorf("expected empty output for empty input, got %d bytes", buf.Len())
	}

	r := mszip.NewReader(&buf)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty decompressed output, got %d bytes", len(got))
	}
}
