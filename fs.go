package cabinet

import (
	"cmp"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// fileInfo wraps File for use as fs.FileInfo / fs.DirEntry.
type fileInfo struct{ f *File }

func (fi fileInfo) Name() string { return path.Base(fi.f.Name) }
func (fi fileInfo) Size() int64  { return int64(fi.f.cbFile) }
func (fi fileInfo) Mode() fs.FileMode {
	if fi.f.ReadOnly {
		return 0o444
	}
	return 0o644
}
func (fi fileInfo) ModTime() time.Time         { return fi.f.Modified }
func (fi fileInfo) IsDir() bool                { return false }
func (fi fileInfo) Sys() any                   { return nil }
func (fi fileInfo) Type() fs.FileMode          { return 0 }
func (fi fileInfo) Info() (fs.FileInfo, error) { return fi, nil }

// dirTree is a lazily-built in-memory directory tree.
type dirTree struct{ root *dirNode }

type dirNode struct {
	name     string
	children map[string]*dirNode // nil for files
	file     *File               // non-nil for regular files
	isDup    bool                // true for conflicting entries
}

func (t *dirTree) build(files []*File) {
	t.root = &dirNode{name: ".", children: map[string]*dirNode{}}
	for _, f := range files {
		if !fs.ValidPath(f.Name) || f.Name == "." {
			continue
		}
		parts := strings.Split(f.Name, "/")
		cur, ok := t.root, true
		for _, p := range parts[:len(parts)-1] {
			child, exists := cur.children[p]
			if !exists {
				child = &dirNode{name: p, children: map[string]*dirNode{}}
				cur.children[p] = child
			} else if child.children == nil {
				// A file node occupies this path component; mark it as a
				// duplicate and skip the rest of this file's path.
				child.isDup = true
				child.file = nil
				ok = false
				break
			}
			cur = child
		}
		if ok {
			// Leaf: if already present, mark the existing node as a duplicate.
			p := parts[len(parts)-1]
			if existing, exists := cur.children[p]; exists {
				existing.isDup = true
				existing.file = nil
			} else {
				cur.children[p] = &dirNode{name: p, file: f}
			}
		}
	}
}

func (t *dirTree) find(name string) *dirNode {
	if name == "." {
		return t.root
	}
	parts := strings.Split(name, "/")
	cur := t.root
	for _, p := range parts {
		child, ok := cur.children[p]
		if !ok || child.isDup {
			return child
		}
		cur = child
	}
	return cur
}

// fsFileHandle is a fs.File for a regular file in the archive.
type fsFileHandle struct {
	file   *File
	rc     io.ReadCloser
	closed bool
}

func (h *fsFileHandle) Stat() (fs.FileInfo, error) {
	return fileInfo{h.file}, nil
}

func (h *fsFileHandle) Read(p []byte) (int, error) {
	if h.closed {
		return 0, &fs.PathError{Op: "read", Path: h.file.Name, Err: fs.ErrClosed}
	}
	return h.rc.Read(p)
}

func (h *fsFileHandle) Close() error {
	if h.closed {
		return &fs.PathError{Op: "close", Path: h.file.Name, Err: fs.ErrClosed}
	}
	h.closed = true
	return h.rc.Close()
}

// fsDirHandle is a fs.File for a synthesized directory.
type fsDirHandle struct {
	node    *dirNode
	name    string
	entries []fs.DirEntry
	pos     int
}

func (h *fsDirHandle) Stat() (fs.FileInfo, error) {
	return &dirInfo{name: path.Base(h.name)}, nil
}

func (h *fsDirHandle) Read(_ []byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: h.name, Err: fs.ErrInvalid}
}

func (h *fsDirHandle) Close() error { return nil }

func (h *fsDirHandle) ReadDir(n int) ([]fs.DirEntry, error) {
	if h.entries == nil {
		h.entries = make([]fs.DirEntry, 0, len(h.node.children))
		for _, child := range h.node.children {
			switch {
			case child.isDup:
				h.entries = append(h.entries, &dupEntry{name: child.name})
			case child.file != nil:
				h.entries = append(h.entries, fileInfo{f: child.file})
			default:
				h.entries = append(h.entries, &dirInfo{name: child.name})
			}
		}
		slices.SortFunc(h.entries, func(a, b fs.DirEntry) int {
			return cmp.Compare(a.Name(), b.Name())
		})
	}
	if n <= 0 {
		rest := h.entries[h.pos:]
		h.pos = len(h.entries)
		return rest, nil
	}
	if h.pos >= len(h.entries) {
		return nil, io.EOF
	}
	end := min(h.pos+n, len(h.entries))
	entries := h.entries[h.pos:end]
	h.pos = end
	return entries, nil
}

// dirInfo is a fs.FileInfo / fs.DirEntry for a synthesized directory.
type dirInfo struct{ name string }

func (d *dirInfo) Name() string               { return d.name }
func (d *dirInfo) Size() int64                { return 0 }
func (d *dirInfo) Mode() fs.FileMode          { return fs.ModeDir | 0o555 }
func (d *dirInfo) ModTime() time.Time         { return time.Time{} }
func (d *dirInfo) IsDir() bool                { return true }
func (d *dirInfo) Sys() any                   { return nil }
func (d *dirInfo) Type() fs.FileMode          { return fs.ModeDir }
func (d *dirInfo) Info() (fs.FileInfo, error) { return d, nil }

// dupEntry is a fs.DirEntry representing a duplicate file or directory entry in the archive.
type dupEntry struct{ name string }

func (d *dupEntry) Name() string      { return d.name }
func (d *dupEntry) IsDir() bool       { return false }
func (d *dupEntry) Type() fs.FileMode { return 0 }
func (d *dupEntry) Info() (fs.FileInfo, error) {
	return nil, &fs.PathError{Op: "stat", Path: d.name, Err: errDuplicate}
}
