//go:build !windows

package cabinet

import (
	"io/fs"
	"os"
)

// populateAttrs sets FileHeader attribute fields from os.FileInfo.
func populateAttrs(fh *FileHeader, info os.FileInfo) {
	// On Unix, derive ReadOnly from the absence of owner write permission.
	// Hidden, System, Archive, Exec have no Unix equivalents; default false.
	fh.ReadOnly = info.Mode()&fs.FileMode(0o200) == 0
}
