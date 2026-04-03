//go:build windows

package cabinet

import (
	"os"
	"syscall"
)

// populateAttrs sets FileHeader attribute fields from os.FileInfo on Windows.
func populateAttrs(fh *FileHeader, info os.FileInfo) {
	sys, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return
	}
	a := sys.FileAttributes
	fh.ReadOnly = a&syscall.FILE_ATTRIBUTE_READONLY != 0
	fh.Hidden = a&syscall.FILE_ATTRIBUTE_HIDDEN != 0
	fh.System = a&syscall.FILE_ATTRIBUTE_SYSTEM != 0
	fh.Archive = a&syscall.FILE_ATTRIBUTE_ARCHIVE != 0
}
