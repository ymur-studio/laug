package follow

// follow -decode: the store's and the trace store's compressed bytes, decompressed, for bin/obs pack's check
// (_obs/cmd/obs/doc.go, pack). A string in a pack is grepped as the file holds it, and much of what the stack writes is
// compressed: each record of Prometheus's write-ahead log, snappy-compressed on its own, and each block of a Badger
// table, where Jaeger's spans are after a clean stop. Seen on the dev host, with the 77 committed gates
// backfilled and the stack up 4 minutes: 180 of the WAL's 180 records were snappy-compressed; 1 085 of the store's
// 1 880 label names and values were in no file as written, and all 1 880 were in the files or their records decoded; a
// span's attribute value, 27 bytes, was in Badger's memtable file as written while Jaeger ran, and after make obs-down
// in no file as written, only in a table of 484 bytes decoded, snappy having copied its "e-span" from the span's name
// (docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt section 7). This reads every file of
// OBS_DIR/prometheus and OBS_DIR/jaeger, places each in a layout of its store, and writes each compressed one's
// contents, decompressed, to <out>/<its path>.decoded, so that the check greps both. A file or a directory it does not
// know is an error: the check cannot say what such a file holds, so bin/obs pack refuses.
//
// The formats, as the stores' source gives them (read, not run; what is seen is that this decoded the dev host's
// own WAL segment and Badger table, section 7, and the cases the tests build):
// - Prometheus's WAL (tsdb/wlog at v2.53.0): segments of 32 KiB pages; a record is one fragment or a first, middles
//   and a last, never across a page; each fragment has a 7-byte header, its type in bits 0 to 2 (0 the rest of the
//   page is zeros, 1 full, 2 first, 3 middle, 4 last), bit 3 snappy, bit 4 zstd, then a big-endian uint16 length and
//   a CRC-32C of the fragment's bytes. A compressed record is the snappy block of its fragments put together. Its
//   checkpoints, wbl/ and chunk snapshots are segments of the same form.
// - A Badger table, *.sst (github.com/dgraph-io/badger/v4 at v4.9.6, table/builder.go: the version Jaeger 2.21.0's
//   binary was built with, as go version -m reads it from the fetched binary, seen on the dev host): its
//   blocks one after another from byte 0, each compressed alone, then the index, its length as a big-endian uint32, a
//   checksum, and the checksum's length as a big-endian uint32. With snappy, the blocks are snappy blocks.
// - snappy's block format (github.com/golang/snappy, format_description.txt; Prometheus 2.53.0's binary is built with
//   v0.0.4 and Jaeger 2.21.0's with v1.0.0, go version -m, seen the same way): a uvarint of the decoded length, then
//   elements, each a literal or a copy of 1 to 64 bytes from at most 4 GiB back.
// zstd, which either store can be set to, is not in the standard library, so a zstd record or table is an error.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	walPage      = 32 * 1024
	walHeader    = 7
	walSnappy    = 1 << 3
	walZstd      = 1 << 4
	walTypeMask  = walSnappy - 1
	maxDecoded   = 1 << 30 // a snappy block that says it holds more is corrupt here: WAL records and table blocks are far smaller
	zstdMagicLE  = 0xFD2FB528
	decodeSuffix = ".decoded"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// errZstd is a record or a table compressed with zstd, which this cannot read.
var errZstd = errors.New("zstd-compressed, which follow cannot read (standard library only)")

// snappyDecode decodes the snappy block at the start of src: it returns the decoded bytes and how many bytes of src
// the block took. An element that runs past src or the decoded length, or a copy from before the start, is an error.
func snappyDecode(src []byte) ([]byte, int, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 {
		return nil, 0, errors.New("snappy: no decoded length")
	}
	if n > maxDecoded {
		return nil, 0, fmt.Errorf("snappy: a decoded length of %d B", n)
	}
	dst := make([]byte, 0, n)
	s := k
	for uint64(len(dst)) < n {
		if s >= len(src) {
			return nil, 0, errors.New("snappy: the block ends early")
		}
		tag := src[s]
		var length, offset int
		switch tag & 3 {
		case 0: // a literal: its length less 1 in the tag, or in the 1 to 4 bytes after it
			x := int(tag >> 2)
			s++
			if x >= 60 {
				w := x - 59
				if s+w > len(src) {
					return nil, 0, errors.New("snappy: a literal's length runs past the block")
				}
				x = 0
				for i := w - 1; i >= 0; i-- {
					x = x<<8 | int(src[s+i])
				}
				s += w
			}
			length = x + 1
			if length <= 0 || s+length > len(src) || uint64(len(dst)+length) > n {
				return nil, 0, errors.New("snappy: a literal runs past the block or the decoded length")
			}
			dst = append(dst, src[s:s+length]...)
			s += length
			continue
		case 1: // a copy, 4 to 11 bytes, an 11-bit offset
			if s+2 > len(src) {
				return nil, 0, errors.New("snappy: a copy runs past the block")
			}
			length = 4 + int(tag>>2)&7
			offset = int(tag&0xe0)<<3 | int(src[s+1])
			s += 2
		case 2: // a copy, 1 to 64 bytes, a 16-bit offset
			if s+3 > len(src) {
				return nil, 0, errors.New("snappy: a copy runs past the block")
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint16(src[s+1:]))
			s += 3
		case 3: // a copy, 1 to 64 bytes, a 32-bit offset
			if s+5 > len(src) {
				return nil, 0, errors.New("snappy: a copy runs past the block")
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint32(src[s+1:]))
			s += 5
		}
		if offset <= 0 || offset > len(dst) || uint64(len(dst)+length) > n {
			return nil, 0, fmt.Errorf("snappy: a copy of %d B from %d B back, with %d B decoded", length, offset, len(dst))
		}
		for i := 0; i < length; i++ { // byte by byte: a copy may overlap what it writes
			dst = append(dst, dst[len(dst)-offset])
		}
	}
	return dst, s, nil
}

// walDecode reads one WAL segment and returns each compressed record's bytes, decoded, each followed by a 0 byte, and
// how many records it read and decoded. A checksum that does not match, a page that is not zeros after its
// terminator, a record that ends with the file, or a zstd record is an error.
func walDecode(b []byte) (out []byte, records, decoded int, err error) {
	var frag []byte
	inRec, compressed := false, false
	for pos := 0; pos < len(b); {
		hdr := b[pos]
		typ := hdr & walTypeMask
		if typ == 0 { // the rest of the page is padding
			end := (pos/walPage + 1) * walPage
			if end > len(b) {
				end = len(b)
			}
			for _, c := range b[pos+1 : end] {
				if c != 0 {
					return nil, 0, 0, fmt.Errorf("byte %d: a padded page with a byte that is not 0", pos)
				}
			}
			pos = end
			continue
		}
		if pos+walHeader > len(b) || pos/walPage != (pos+walHeader-1)/walPage {
			return nil, 0, 0, fmt.Errorf("byte %d: a fragment header that runs past its page or the file", pos)
		}
		length := int(binary.BigEndian.Uint16(b[pos+1:]))
		crc := binary.BigEndian.Uint32(b[pos+3:])
		start := pos + walHeader
		if start+length > len(b) || (length > 0 && start/walPage != (start+length-1)/walPage) {
			return nil, 0, 0, fmt.Errorf("byte %d: a fragment of %d B that runs past its page or the file", pos, length)
		}
		data := b[start : start+length]
		if got := crc32.Checksum(data, castagnoli); got != crc {
			return nil, 0, 0, fmt.Errorf("byte %d: checksum %08x, the header says %08x", pos, got, crc)
		}
		if hdr&walZstd != 0 {
			return nil, 0, 0, fmt.Errorf("byte %d: a record %w", pos, errZstd)
		}
		switch typ {
		case 1, 2:
			if inRec {
				return nil, 0, 0, fmt.Errorf("byte %d: a record starts before the last one ended", pos)
			}
			frag, inRec = append(frag[:0], data...), true
		case 3, 4:
			if !inRec {
				return nil, 0, 0, fmt.Errorf("byte %d: a middle or last fragment with no first", pos)
			}
			frag = append(frag, data...)
		default:
			return nil, 0, 0, fmt.Errorf("byte %d: fragment type %d", pos, typ)
		}
		compressed = hdr&walSnappy != 0
		pos = start + length
		if typ == 1 || typ == 4 {
			inRec = false
			records++
			if compressed && len(frag) > 0 {
				d, used, err := snappyDecode(frag)
				if err != nil {
					return nil, 0, 0, fmt.Errorf("the record ending at byte %d: %v", pos, err)
				}
				if used != len(frag) {
					return nil, 0, 0, fmt.Errorf("the record ending at byte %d: %d B after its snappy block", pos, len(frag)-used)
				}
				out = append(append(out, d...), 0)
				decoded++
			}
		}
	}
	if inRec {
		return nil, 0, 0, errors.New("the file ends inside a record")
	}
	return out, records, decoded, nil
}

// sstDecode reads one Badger table and returns its blocks, decoded, each followed by a 0 byte, and how many.
func sstDecode(b []byte) (out []byte, blocks int, err error) {
	if len(b) < 8 {
		return nil, 0, fmt.Errorf("%d B, too short for a table", len(b))
	}
	ck := int(binary.BigEndian.Uint32(b[len(b)-4:]))
	if ck < 0 || 4+ck+4 > len(b) {
		return nil, 0, fmt.Errorf("a checksum length of %d B in a table of %d B", ck, len(b))
	}
	ixEnd := len(b) - 4 - ck - 4
	ix := int(binary.BigEndian.Uint32(b[ixEnd:]))
	end := ixEnd - ix
	if ix < 0 || end < 0 {
		return nil, 0, fmt.Errorf("an index length of %d B in a table of %d B", ix, len(b))
	}
	for pos := 0; pos < end; {
		if pos+4 <= end && binary.LittleEndian.Uint32(b[pos:]) == zstdMagicLE {
			return nil, 0, fmt.Errorf("the block at byte %d is %w", pos, errZstd)
		}
		d, used, err := snappyDecode(b[pos:end])
		if err != nil {
			return nil, 0, fmt.Errorf("the block at byte %d: %v (a table compressed with nothing, or with zstd, is not read here)", pos, err)
		}
		out = append(append(out, d...), 0)
		blocks++
		pos += used
	}
	return out, blocks, nil
}

// The layouts. A path is relative to OBS_DIR; each pattern is anchored. What a raw file holds is in it as written:
// a block's index (its symbol table is every string of the block, uncompressed: Prometheus 2.53.0 tsdb/docs/format),
// meta.json, tombstones and chunks (samples, no strings), the head's m-mapped chunks, the lock and the
// active query log; Badger's value log and memtable files (entries as written), MANIFEST, KEYREGISTRY, DISCARD, LOCK.
var (
	walFile = regexp.MustCompile(`^prometheus/(wal|wbl)(/checkpoint\.[0-9]+)?/[0-9]{8}$|^prometheus/chunk_snapshot\.[0-9]+\.[0-9]+/[0-9]{8}$`)
	rawProm = regexp.MustCompile(`^prometheus/(lock|queries\.active|[0-9A-HJKMNP-TV-Z]{26}/(meta\.json|index|tombstones|chunks/[0-9]{6})|chunks_head/[0-9]{6})$`)
	sstFile = regexp.MustCompile(`^jaeger/(keys|values)/[0-9]+\.sst$`)
	rawJaeg = regexp.MustCompile(`^jaeger/(keys|values)/([0-9]+\.(vlog|mem)|MANIFEST|KEYREGISTRY|DISCARD|LOCK)$`)
	dirs    = regexp.MustCompile(`^prometheus(/((wal|wbl)(/checkpoint\.[0-9]+)?|chunk_snapshot\.[0-9]+\.[0-9]+|[0-9A-HJKMNP-TV-Z]{26}(/chunks)?|chunks_head))?$|^jaeger(/(keys|values))?$`)
)

// decodeSummary is what -decode prints, one JSON object.
type decodeSummary struct {
	Files       int `json:"files"`
	Raw         int `json:"raw"`
	WALSegments int `json:"wal_segments"`
	WALRecords  int `json:"wal_records"`
	WALDecoded  int `json:"wal_records_decoded"`
	WALIn       int `json:"wal_bytes"`
	WALOut      int `json:"wal_bytes_decoded"`
	Tables      int `json:"tables"`
	TableBlocks int `json:"table_blocks"`
	TableIn     int `json:"table_bytes"`
	TableOut    int `json:"table_bytes_decoded"`
}

// decodeTree reads OBS_DIR/prometheus and OBS_DIR/jaeger (either may be absent) and writes each compressed file's
// decoded bytes under out, at its own path plus .decoded. The first file or directory it cannot place, or file it
// cannot read, stops it; so every path a pack holds is one of these layouts, ULIDs, digits and fixed names.
func decodeTree(dir, out string) (decodeSummary, error) {
	var sum decodeSummary
	for _, top := range []string{"prometheus", "jaeger"} {
		root := filepath.Join(dir, top)
		if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if !dirs.MatchString(rel) {
					return fmt.Errorf("%s: a directory the check does not know", rel)
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s: not a regular file", rel)
			}
			sum.Files++
			var dec []byte
			switch {
			case rawProm.MatchString(rel), rawJaeg.MatchString(rel):
				sum.Raw++
				return nil
			case walFile.MatchString(rel):
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				var recs, n int
				if dec, recs, n, err = walDecode(b); err != nil {
					return fmt.Errorf("%s: %v", rel, err)
				}
				sum.WALSegments++
				sum.WALRecords += recs
				sum.WALDecoded += n
				sum.WALIn += len(b)
				sum.WALOut += len(dec)
			case sstFile.MatchString(rel):
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				var n int
				if dec, n, err = sstDecode(b); err != nil {
					return fmt.Errorf("%s: %v", rel, err)
				}
				sum.Tables++
				sum.TableBlocks += n
				sum.TableIn += len(b)
				sum.TableOut += len(dec)
			default:
				return fmt.Errorf("%s: a file the check does not know, so it cannot say what the file holds", rel)
			}
			dst := filepath.Join(out, filepath.FromSlash(rel)+decodeSuffix)
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			return os.WriteFile(dst, dec, 0o600)
		})
		if err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// runDecode is follow -decode <OBS_DIR> -out <dir>: 0 and the summary on stdout, 2 and why on stderr.
func runDecode(dir, out string, stdout, stderr io.Writer) int {
	if dir == "" || out == "" {
		fmt.Fprintln(stderr, "follow: -decode <OBS_DIR> needs -out <dir>, an empty directory outside it")
		return 2
	}
	absDir, err1 := filepath.Abs(dir)
	absOut, err2 := filepath.Abs(out)
	if err1 != nil || err2 != nil {
		fmt.Fprintln(stderr, "follow: -decode: a path that does not resolve")
		return 2
	}
	for _, top := range []string{"prometheus", "jaeger"} {
		if t := filepath.Join(absDir, top); absOut == t || strings.HasPrefix(absOut, t+string(filepath.Separator)) {
			fmt.Fprintf(stderr, "follow: -decode: -out inside %s, which it reads\n", top)
			return 2
		}
	}
	if ents, err := os.ReadDir(absOut); err == nil && len(ents) > 0 {
		fmt.Fprintln(stderr, "follow: -decode: -out is not empty")
		return 2
	}
	sum, err := decodeTree(absDir, absOut)
	if err != nil {
		fmt.Fprintln(stderr, "follow: -decode: "+err.Error())
		return 2
	}
	b, _ := json.Marshal(sum)
	fmt.Fprintln(stdout, string(bytes.TrimSpace(b)))
	return 0
}
