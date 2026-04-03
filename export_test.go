package cabinet

import (
	"os"
	"testing"
)

// PopulateAttrs exposes populateAttrs for testing.
func PopulateAttrs(fh *FileHeader, info os.FileInfo) { populateAttrs(fh, info) }

// SetAutoSplitThreshold overrides the folder auto-split threshold for testing.
func SetAutoSplitThreshold(t *testing.T, n uint64) {
	t.Helper()
	orig := autoSplitThreshold
	autoSplitThreshold = n
	t.Cleanup(func() { autoSplitThreshold = orig })
}

var ErrDuplicate = errDuplicate
