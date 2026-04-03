package cabinet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	cabinetSignature = 0x4643534D // "MSCF" as little-endian uint32

	flagPrevCabinet    = 0x0001
	flagNextCabinet    = 0x0002
	flagReservePresent = 0x0004

	// File attribute flags.
	attrReadOnly  = 0x0001
	attrHidden    = 0x0002
	attrSystem    = 0x0004
	attrArchive   = 0x0020
	attrExec      = 0x0040
	attrNameIsUTF = 0x0080

	// Fixed sizes.
	cfHeaderSize = 36 // bytes in a base CFHEADER (no reserve, no cabinet names)
	cfFolderSize = 8  // bytes in a CFFOLDER entry (no reserve)
	cfFileSize   = 16 // bytes in a CFFILE entry (excluding szName)
	cfDataSize   = 8  // bytes in a CFDATA header (no reserve)
)

// cfHeader mirrors the on-disk CFHEADER layout.
type cfHeader struct {
	Signature    uint32 // must be cabinetSignature ("MSCF")
	CbCabinet    uint32 // total size of the cabinet file in bytes
	CoffFiles    uint32 // absolute offset of the first CFFILE entry
	VersionMinor uint8  // cabinet format minor version (3)
	VersionMajor uint8  // cabinet format major version (1)
	CFolders     uint16 // number of CFFOLDER entries
	CFiles       uint16 // number of CFFILE entries
	Flags        uint16 // cabinet flags (flagPrevCabinet, flagNextCabinet, flagReservePresent)
	SetID        uint16 // identifier shared by all cabinets in a set
	ICabinet     uint16 // zero-based index of this cabinet within the set
	// Only present when flagReservePresent:
	CbCFHeader uint16 // size in bytes of per-cabinet reserved area
	CbCFFolder uint8  // size in bytes of per-folder reserved area
	CbCFData   uint8  // size in bytes of per-datablock reserved area
}

func readCFHeader(r io.ReaderAt) (hdr cfHeader, err error) {
	var buf [cfHeaderSize]byte
	if _, err = r.ReadAt(buf[:], 0); err != nil {
		return hdr, err
	}

	hdr.Signature = binary.LittleEndian.Uint32(buf[0:4])
	hdr.CbCabinet = binary.LittleEndian.Uint32(buf[8:12])
	hdr.CoffFiles = binary.LittleEndian.Uint32(buf[16:20])
	hdr.VersionMinor = buf[24]
	hdr.VersionMajor = buf[25]
	hdr.CFolders = binary.LittleEndian.Uint16(buf[26:28])
	hdr.CFiles = binary.LittleEndian.Uint16(buf[28:30])
	hdr.Flags = binary.LittleEndian.Uint16(buf[30:32])
	hdr.SetID = binary.LittleEndian.Uint16(buf[32:34])
	hdr.ICabinet = binary.LittleEndian.Uint16(buf[34:36])

	if hdr.Signature != cabinetSignature {
		return hdr, fmt.Errorf("%w: bad signature %#x", ErrFormat, hdr.Signature)
	}
	if hdr.VersionMajor != 1 {
		return hdr, fmt.Errorf("%w: unsupported major version %d", ErrFormat, hdr.VersionMajor)
	}

	if hdr.Flags&flagReservePresent != 0 {
		var rbuf [4]byte
		if _, err = r.ReadAt(rbuf[:], cfHeaderSize); err != nil {
			return hdr, err
		}
		hdr.CbCFHeader = binary.LittleEndian.Uint16(rbuf[0:2])
		hdr.CbCFFolder = rbuf[2]
		hdr.CbCFData = rbuf[3]
	}
	return hdr, nil
}

func writeCFHeader(w io.Writer, hdr cfHeader) error {
	var buf [cfHeaderSize]byte
	binary.LittleEndian.PutUint32(buf[0:4], hdr.Signature)
	binary.LittleEndian.PutUint32(buf[8:12], hdr.CbCabinet)
	binary.LittleEndian.PutUint32(buf[16:20], hdr.CoffFiles)
	buf[24] = hdr.VersionMinor
	buf[25] = hdr.VersionMajor
	binary.LittleEndian.PutUint16(buf[26:28], hdr.CFolders)
	binary.LittleEndian.PutUint16(buf[28:30], hdr.CFiles)
	binary.LittleEndian.PutUint16(buf[30:32], hdr.Flags)
	binary.LittleEndian.PutUint16(buf[32:34], hdr.SetID)
	binary.LittleEndian.PutUint16(buf[34:36], hdr.ICabinet)
	_, err := w.Write(buf[:])
	return err
}

// cfFolderRecord holds parsed CFFOLDER data.
type cfFolderRecord struct {
	CoffCabStart uint32      // absolute offset of the first CFDATA block for this folder
	CCFData      uint16      // number of CFDATA blocks in this folder
	TypeCompress Compression // compression method used for all data in this folder
}

func readCFFolder(r io.ReaderAt, off int64) (cfFolderRecord, error) {
	var buf [cfFolderSize]byte
	if _, err := r.ReadAt(buf[:], off); err != nil {
		return cfFolderRecord{}, err
	}
	return cfFolderRecord{
		CoffCabStart: binary.LittleEndian.Uint32(buf[0:4]),
		CCFData:      binary.LittleEndian.Uint16(buf[4:6]),
		TypeCompress: Compression(binary.LittleEndian.Uint16(buf[6:8])),
	}, nil
}

func writeCFFolder(w io.Writer, rec cfFolderRecord) error {
	var buf [cfFolderSize]byte
	binary.LittleEndian.PutUint32(buf[0:4], rec.CoffCabStart)
	binary.LittleEndian.PutUint16(buf[4:6], rec.CCFData)
	binary.LittleEndian.PutUint16(buf[6:8], uint16(rec.TypeCompress))
	_, err := w.Write(buf[:])
	return err
}

// cfFileRecord holds parsed CFFILE data.
type cfFileRecord struct {
	CbFile          uint32 // uncompressed size of the file in bytes
	UoffFolderStart uint32 // byte offset of the file's data within the uncompressed folder stream
	IFolder         uint16 // index of the CFFOLDER containing this file's data
	Date            uint16 // MS-DOS encoded date of last modification
	Time            uint16 // MS-DOS encoded time of last modification
	Attribs         uint16 // file attribute flags (attrReadOnly, attrHidden, etc.)
	Name            string // null-terminated filename; UTF-8 if attrNameIsUTF is set, otherwise ASCII
}

func readCFFile(r io.ReaderAt, off int64) (cfFileRecord, int64, error) {
	var fixed [cfFileSize]byte
	if _, err := r.ReadAt(fixed[:], off); err != nil {
		return cfFileRecord{}, 0, err
	}
	rec := cfFileRecord{
		CbFile:          binary.LittleEndian.Uint32(fixed[0:4]),
		UoffFolderStart: binary.LittleEndian.Uint32(fixed[4:8]),
		IFolder:         binary.LittleEndian.Uint16(fixed[8:10]),
		Date:            binary.LittleEndian.Uint16(fixed[10:12]),
		Time:            binary.LittleEndian.Uint16(fixed[12:14]),
		Attribs:         binary.LittleEndian.Uint16(fixed[14:16]),
	}

	// Read null-terminated filename in one call. Per spec, names are at most 256 bytes.
	var nameBuf [257]byte
	n, err := r.ReadAt(nameBuf[:], off+cfFileSize)
	if n == 0 {
		return cfFileRecord{}, 0, err
	}
	name, _, ok := bytes.Cut(nameBuf[:n], []byte{0})
	if !ok {
		return cfFileRecord{}, 0, fmt.Errorf("%w: filename not null-terminated", ErrFormat)
	}
	rec.Name = string(name)
	return rec, int64(cfFileSize) + int64(len(name)) + 1, nil
}

func writeCFFile(w io.Writer, rec cfFileRecord) error {
	if strings.IndexByte(rec.Name, 0) >= 0 {
		return errors.New("cabinet: file name contains NUL byte")
	}
	if len(rec.Name) > 256 {
		return fmt.Errorf("cabinet: file name too long (%d bytes, max 256)", len(rec.Name))
	}

	var buf [cfFileSize]byte
	binary.LittleEndian.PutUint32(buf[0:4], rec.CbFile)
	binary.LittleEndian.PutUint32(buf[4:8], rec.UoffFolderStart)
	binary.LittleEndian.PutUint16(buf[8:10], rec.IFolder)
	binary.LittleEndian.PutUint16(buf[10:12], rec.Date)
	binary.LittleEndian.PutUint16(buf[12:14], rec.Time)
	binary.LittleEndian.PutUint16(buf[14:16], rec.Attribs)
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, rec.Name); err != nil {
		return err
	}
	_, err := w.Write([]byte{0})
	return err
}

// readCFDataBlock reads one CFDATA block from r at off, skipping cbCFData
// reserved bytes after the fixed header. It returns the compressed payload
// and the total number of bytes consumed (header + reserved + payload).
// If verifyChecksum is true and the stored checksum does not match, it returns ErrChecksum.
func readCFDataBlock(
	r io.ReaderAt,
	off int64,
	cbCFData uint8,
	buf []byte,
	verifyChecksum bool,
) (payload []byte, n int64, err error) {
	var hdr [cfDataSize]byte
	if _, err = r.ReadAt(hdr[:], off); err != nil {
		return nil, 0, err
	}
	csum := binary.LittleEndian.Uint32(hdr[0:4])
	cbData := binary.LittleEndian.Uint16(hdr[4:6])
	cbUncomp := binary.LittleEndian.Uint16(hdr[6:8])

	payload = buf[:cbData]
	if _, err = r.ReadAt(payload, off+cfDataSize+int64(cbCFData)); err != nil {
		return nil, 0, err
	}
	if verifyChecksum && csum != 0 && checksumData(payload, cbUncomp) != csum {
		return nil, 0, ErrChecksum
	}
	return payload, cfDataSize + int64(cbCFData) + int64(cbData), nil
}

// writeCFDataBlock writes one CFDATA block to w and returns the number of
// bytes written (header + payload).
func writeCFDataBlock(w io.Writer, payload []byte, cbUncomp uint16) (int64, error) {
	if len(payload) > math.MaxUint16 {
		return 0, fmt.Errorf("cabinet: compressed block too large (%d bytes)", len(payload))
	}

	csum := checksumData(payload, cbUncomp)
	var hdr [cfDataSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], csum)
	binary.LittleEndian.PutUint16(hdr[4:6], uint16(len(payload)))
	binary.LittleEndian.PutUint16(hdr[6:8], cbUncomp)

	if _, err := w.Write(hdr[:]); err != nil {
		return 0, err
	}
	if _, err := w.Write(payload); err != nil {
		return 0, err
	}
	return int64(cfDataSize) + int64(len(payload)), nil
}

// checksumData computes the Microsoft Cabinet checksum for a CFDATA block.
//
// The checksum initializes with cbData | (cbUncomp << 16), then processes
// the compressed payload in 4-byte little-endian chunks XORed into an
// accumulator, with remainder bytes left-shifted into the accumulator.
func checksumData(payload []byte, cbUncomp uint16) uint32 {
	csum := uint32(len(payload)) | uint32(cbUncomp)<<16

	for len(payload) >= 4 {
		csum ^= binary.LittleEndian.Uint32(payload[:4])
		payload = payload[4:]
	}

	ul := uint32(0)
	for _, b := range payload {
		ul = (ul << 8) | uint32(b)
	}
	return csum ^ ul
}

// attrsToHeader converts a CFFILE attribs bitmask to FileHeader boolean fields.
func attrsToHeader(attribs uint16, fh *FileHeader) {
	fh.ReadOnly = attribs&attrReadOnly != 0
	fh.Hidden = attribs&attrHidden != 0
	fh.System = attribs&attrSystem != 0
	fh.Archive = attribs&attrArchive != 0
	fh.Exec = attribs&attrExec != 0
	fh.NonUTF8 = attribs&attrNameIsUTF == 0
}

// headerToAttrs converts FileHeader boolean fields to a CFFILE attribs bitmask.
func headerToAttrs(fh *FileHeader) uint16 {
	var a uint16
	if fh.ReadOnly {
		a |= attrReadOnly
	}
	if fh.Hidden {
		a |= attrHidden
	}
	if fh.System {
		a |= attrSystem
	}
	if fh.Archive {
		a |= attrArchive
	}
	if fh.Exec {
		a |= attrExec
	}
	if !fh.NonUTF8 {
		a |= attrNameIsUTF
	}
	return a
}

// decodeDOSTime decodes MS-DOS date and time fields to a time.Time.
func decodeDOSTime(dosDate, dosTime uint16) time.Time {
	return time.Date(
		int(dosDate>>9+1980),
		time.Month(dosDate>>5&0xf),
		int(dosDate&0x1f),
		int(dosTime>>11),
		int(dosTime>>5&0x3f),
		int(dosTime&0x1f*2),
		0,
		time.UTC,
	)
}

var (
	dosMin = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	dosMax = time.Date(2107, 12, 31, 23, 59, 58, 0, time.UTC)
)

// encodeDOSTime encodes a time.Time as MS-DOS date and time fields.
// The DOS date/time format represents years in [1980, 2107]; times outside
// that range are clamped to the nearest representable value.
func encodeDOSTime(t time.Time) (dosDate, dosTime uint16) {
	t = t.UTC()
	if t.Before(dosMin) {
		t = dosMin
	} else if t.After(dosMax) {
		t = dosMax
	}
	sec := t.Second() &^ 1 // truncate to 2-second resolution
	dosDate = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	dosTime = uint16(t.Hour()<<11 | t.Minute()<<5 | sec/2)
	return dosDate, dosTime
}

func backslashToSlash(s string) string { return replaceStringByte(s, '\\', '/') }
func slashToBackslash(s string) string { return replaceStringByte(s, '/', '\\') }

func replaceStringByte(s string, before, after byte) string {
	if strings.IndexByte(s, before) == -1 {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] == before {
			b[i] = after
		}
	}
	return string(b)
}

// skipNullStr advances past a null-terminated string at off and returns the offset of the following byte.
func skipNullStr(r io.ReaderAt, off int64) (int64, error) {
	var buf [256]byte
	n, err := r.ReadAt(buf[:], off)
	if n == 0 {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	before, _, ok := bytes.Cut(buf[:n], []byte{0})
	if !ok {
		return 0, ErrFormat
	}
	return off + int64(len(before)) + 1, nil
}
