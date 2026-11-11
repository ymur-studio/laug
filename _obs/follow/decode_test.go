package follow

// The tests of follow -decode (decode.go), on WAL segments, Badger tables and snappy blocks built here byte by byte,
// as the formats in decode.go's comment give them. A real store's files are read on the dev host by obs pack
// (cells/results/2026-10-17-obs-offline.txt section 7), never committed: a store's compressed bytes are what a grep
// of the tree cannot see into.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snappy elements, as the block format writes them.
func sLit(b string) []byte {
	n := len(b) - 1
	switch {
	case n < 60:
		return append([]byte{byte(n << 2)}, b...)
	case n < 1<<8:
		return append([]byte{60 << 2, byte(n)}, b...)
	default:
		return append([]byte{61 << 2, byte(n), byte(n >> 8)}, b...)
	}
}
func sCopy1(length, offset int) []byte { // length 4 to 11, offset under 2048
	return []byte{byte(1 | (length-4)<<2 | (offset>>8)<<5), byte(offset)}
}
func sCopy2(length, offset int) []byte {
	return []byte{byte(2 | (length-1)<<2), byte(offset), byte(offset >> 8)}
}
func sCopy4(length, offset int) []byte {
	b := []byte{byte(3 | (length-1)<<2), 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[1:], uint32(offset))
	return b
}
func sBlock(n int, elems ...[]byte) []byte {
	b := binary.AppendUvarint(nil, uint64(n))
	for _, e := range elems {
		b = append(b, e...)
	}
	return b
}

func TestSnappyDecode(t *testing.T) {
	long := strings.Repeat("0123456789", 30) // 300 B: a literal whose length takes 2 bytes
	cases := []struct {
		name, want string
		src        []byte
	}{
		{"a literal", "laug", sBlock(4, sLit("laug"))},
		{"a 1-byte-offset copy that overlaps itself", "abababab", sBlock(8, sLit("ab"), sCopy1(6, 2))},
		{"a 2-byte-offset copy", "store-laug-store", sBlock(16, sLit("store-laug-"), sCopy2(5, 11))},
		{"a 4-byte-offset copy", "head1head", sBlock(9, sLit("head1"), sCopy4(4, 5))},
		{"a literal of 61 B", strings.Repeat("x", 61), sBlock(61, sLit(strings.Repeat("x", 61)))},
		{"a literal of 300 B", long, sBlock(300, sLit(long))},
		{"an empty block", "", sBlock(0)},
	}
	for _, c := range cases {
		got, used, err := snappyDecode(append(append([]byte{}, c.src...), 0xff, 0xfe)) // 2 bytes after the block
		if err != nil || string(got) != c.want || used != len(c.src) {
			t.Errorf("%s: got %q, used %d of %d, err %v; want %q", c.name, got, used, len(c.src), err, c.want)
		}
	}
	bad := []struct {
		name string
		src  []byte
	}{
		{"no length", nil},
		{"a copy from offset 0", sBlock(6, sLit("ab"), sCopy1(4, 0))},
		{"a copy from before the start", sBlock(6, sLit("ab"), sCopy1(4, 3))},
		{"a copy past the decoded length", sBlock(5, sLit("ab"), sCopy1(4, 2))},
		{"a literal past the decoded length", sBlock(3, sLit("abcd"))},
		{"a literal past the block", sBlock(8, []byte{7 << 2, 'a', 'b'})},
		{"a block that ends early", sBlock(8, sLit("ab"))},
		{"a copy whose offset runs past the block", sBlock(8, sLit("ab"), []byte{2 | 3<<2, 1})},
		{"a decoded length over 1 GiB", sBlock(1<<31, sLit("a"))},
	}
	for _, c := range bad {
		if got, _, err := snappyDecode(c.src); err == nil {
			t.Errorf("%s: decoded %q, want an error", c.name, got)
		}
	}
}

// frag is one WAL fragment: its header (type and flags, big-endian length, CRC-32C) and its bytes.
func frag(typ byte, data []byte) []byte {
	h := make([]byte, walHeader)
	h[0] = typ
	binary.BigEndian.PutUint16(h[1:], uint16(len(data)))
	binary.BigEndian.PutUint32(h[3:], crc32.Checksum(data, castagnoli))
	return append(h, data...)
}

// padPage pads seg to the end of its page as Prometheus does: a 0 type byte and zeros.
func padPage(seg []byte) []byte {
	if r := len(seg) % walPage; r != 0 {
		seg = append(seg, make([]byte, walPage-r)...)
	}
	return seg
}

// hiddenWord is in a label value as a store holds it, compressed so that it is in no byte of the record as written:
// the "store" after "home-" is a copy of the record's first word.
const (
	hiddenWord    = "home-store"
	hiddenDecoded = "store-label, home-store label"
)

func hiddenBlock() []byte {
	return sBlock(len(hiddenDecoded), sLit("store-label, home-"), sCopy1(5, 18), sLit(" label"))
}

func TestHiddenBlock(t *testing.T) { // the fixture's own check
	b := hiddenBlock()
	got, used, err := snappyDecode(b)
	if err != nil || string(got) != hiddenDecoded || used != len(b) {
		t.Fatalf("got %q, used %d of %d, err %v", got, used, len(b), err)
	}
	if bytes.Contains(b, []byte(hiddenWord)) {
		t.Fatalf("the fixture holds %q as written: %q", hiddenWord, b)
	}
}

func TestWALDecode(t *testing.T) {
	var seg []byte
	seg = append(seg, frag(1, []byte("a record as written"))...)
	seg = append(seg, frag(1|walSnappy, hiddenBlock())...)
	// A record of 40 000 B in three fragments, compressed, across a page: its first fragment ends the first page.
	big := strings.Repeat("abcdefghij", 4000)
	whole := sBlock(len(big), sLit(big))
	room := walPage - len(seg) - walHeader
	seg = append(seg, frag(2|walSnappy, whole[:room])...)
	if len(seg) != walPage {
		t.Fatalf("the first fragment ends at %d, not at the page's end", len(seg))
	}
	mid := (len(whole) - room) / 2
	seg = append(seg, frag(3|walSnappy, whole[room:room+mid])...)
	seg = append(seg, frag(4|walSnappy, whole[room+mid:])...)
	seg = padPage(seg) // the second page padded to its end
	seg = append(seg, frag(1|walSnappy, sBlock(5, sLit("third")))...)

	out, records, decoded, err := walDecode(seg)
	if err != nil {
		t.Fatal(err)
	}
	if records != 4 || decoded != 3 {
		t.Errorf("records %d and decoded %d, want 4 and 3", records, decoded)
	}
	if want := hiddenDecoded + "\x00" + big + "\x00third\x00"; string(out) != want {
		t.Errorf("decoded %d B, want %d B", len(out), len(want))
	}
	if bytes.Contains(seg, []byte(hiddenWord)) || !bytes.Contains(out, []byte(hiddenWord)) {
		t.Error("the hidden word should be in the decoded bytes only")
	}
	if bytes.Contains(out, []byte("a record as written")) {
		t.Error("a record written uncompressed is grepped in the file, not copied out")
	}
}

func TestWALDecodeRefuses(t *testing.T) {
	ok := frag(1|walSnappy, sBlock(4, sLit("laug")))
	badCRC := frag(1, []byte("a record as written"))
	badCRC[walHeader] ^= 1 // its first byte changed, its checksum not
	cases := []struct {
		name string
		seg  []byte
	}{
		{"a checksum that does not match", badCRC},
		{"a zstd record", frag(1|walZstd, []byte("zstd bytes"))},
		{"a file that ends inside a record", frag(2, []byte("first"))},
		{"a middle fragment with no first", frag(3, []byte("middle"))},
		{"a last fragment with no first", frag(4, []byte("last"))},
		{"a first fragment inside a record", append(frag(2, []byte("a")), frag(2, []byte("b"))...)},
		{"a fragment type of 5", frag(5, []byte("five"))},
		{"a padded page with a byte that is not 0", append(append(append([]byte{}, ok...), 0, 0, 7), make([]byte, 10)...)},
		{"a header cut by the file's end", ok[:5]},
		{"a fragment cut by the file's end", ok[:len(ok)-1]},
		{"a compressed record that does not decode", frag(1|walSnappy, []byte{9, 0})},
		{"bytes after a record's snappy block", frag(1|walSnappy, append(sBlock(4, sLit("laug")), 0))},
	}
	for _, c := range cases {
		if _, _, _, err := walDecode(c.seg); err == nil {
			t.Errorf("%s: no error", c.name)
		} else if c.name == "a zstd record" && !errors.Is(err, errZstd) {
			t.Errorf("%s: %v, not errZstd", c.name, err)
		}
	}
	// A header that would cross a page is an error; a page padded to its end is not.
	seg := frag(1, []byte(strings.Repeat("x", walPage-3-walHeader))) // the page's first walPage-3 B
	seg = append(seg, frag(1, []byte("x"))...)                       // a header in its last 3 B and the next page's first 4
	if _, _, _, err := walDecode(seg); err == nil || !strings.Contains(err.Error(), "runs past its page") {
		t.Error("a header across a page: no error")
	}
	if _, recs, _, err := walDecode(padPage(frag(1, []byte("x")))); err != nil || recs != 1 {
		t.Errorf("a padded page: %d records, err %v", recs, err)
	}
}

// table is a Badger table: its blocks, then the index, its length, a checksum and the checksum's length.
func table(blocks [][]byte, index, checksum []byte) []byte {
	var b []byte
	for _, x := range blocks {
		b = append(b, x...)
	}
	b = append(b, index...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(index)))
	b = append(b, checksum...)
	return binary.BigEndian.AppendUint32(b, uint32(len(checksum)))
}

func TestSSTDecode(t *testing.T) {
	b1, b2 := hiddenBlock(), sBlock(len("span laug-worker"), sLit("span laug-worker"))
	out, n, err := sstDecode(table([][]byte{b1, b2}, []byte("an index of block offsets"), []byte{0x10, 0x98, 0xe4}))
	if err != nil || n != 2 {
		t.Fatalf("%d blocks, err %v", n, err)
	}
	if want := hiddenDecoded + "\x00span laug-worker\x00"; string(out) != want {
		t.Errorf("decoded %q, want %q", out, want)
	}
	zstd := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0, 0, 0}
	bad := []struct {
		name string
		b    []byte
	}{
		{"a zstd block", table([][]byte{zstd}, []byte("ix"), []byte("ck"))},
		{"a block written with no compression", table([][]byte{[]byte("\x00\x00\x00\x05plain block bytes")}, []byte("ix"), []byte("ck"))},
		{"a checksum length past the file", binary.BigEndian.AppendUint32([]byte("abcd"), 99)},
		{"an index length past the file", binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 99), 0)},
		{"under 8 B", []byte{0, 0, 0, 0}},
	}
	for _, c := range bad {
		if _, _, err := sstDecode(c.b); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
	if _, _, err := sstDecode(table([][]byte{zstd}, nil, nil)); !errors.Is(err, errZstd) {
		t.Errorf("a zstd block: %v, not errZstd", err)
	}
	if out, n, err := sstDecode(table(nil, []byte("ix"), nil)); err != nil || n != 0 || len(out) != 0 {
		t.Errorf("a table with no block: %q, %d, %v", out, n, err)
	}
}

// fakeState is an OBS_DIR with a store and a trace store of every file kind decode.go places.
func fakeState(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	seg := frag(1|walSnappy, hiddenBlock())
	files := map[string][]byte{
		"prometheus/wal/00000000":                             seg,
		"prometheus/wal/checkpoint.00000001/00000000":         frag(1|walSnappy, sBlock(5, sLit("ckpt1"))),
		"prometheus/01JABCDEFGHJKMNPQRSTVWXYZ0/index":         []byte("symbols as written"),
		"prometheus/01JABCDEFGHJKMNPQRSTVWXYZ0/meta.json":     []byte(`{"ulid":"01JABCDEFGHJKMNPQRSTVWXYZ0"}`),
		"prometheus/01JABCDEFGHJKMNPQRSTVWXYZ0/tombstones":    {0, 0, 0, 0},
		"prometheus/01JABCDEFGHJKMNPQRSTVWXYZ0/chunks/000001": {1, 2, 3},
		"prometheus/chunks_head/000001":                       {1, 2, 3},
		"prometheus/lock":                                     nil,
		"prometheus/queries.active":                           []byte("[]"),
		"jaeger/keys/000001.sst":                              table([][]byte{sBlock(4, sLit("span"))}, []byte("ix"), []byte("ck")),
		"jaeger/keys/MANIFEST":                                []byte("Bdgr"),
		"jaeger/keys/KEYREGISTRY":                             {1},
		"jaeger/values/000001.vlog":                           []byte("an entry as written"),
		"jaeger/values/DISCARD":                               {0},
	}
	for p, b := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, p), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(d, "grafana"), 0o755); err != nil { // not read: not in a pack
		t.Fatal(err)
	}
	write(t, filepath.Join(d, "grafana", "stray.bin"), "x")
	return d
}

func TestDecodeTree(t *testing.T) {
	d := fakeState(t)
	out := t.TempDir()
	sum, err := decodeTree(d, out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 14 || sum.Raw != 11 || sum.WALSegments != 2 || sum.WALRecords != 2 || sum.WALDecoded != 2 || sum.Tables != 1 || sum.TableBlocks != 1 {
		t.Errorf("summary %+v", sum)
	}
	if got := read(t, filepath.Join(out, "prometheus", "wal", "00000000.decoded")); got != hiddenDecoded+"\x00" {
		t.Errorf("the segment decoded to %q", got)
	}
	if got := read(t, filepath.Join(out, "jaeger", "keys", "000001.sst.decoded")); got != "span\x00" {
		t.Errorf("the table decoded to %q", got)
	}
	// A file the layouts do not hold stops it, named; so does a link.
	for name, mk := range map[string]func(string) error{
		"prometheus/stray.bin":       func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) },
		"jaeger/keys/000002.sst.tmp": func(p string) error { return os.WriteFile(p, []byte("x"), 0o644) },
		"prometheus/01JABCDEFGHJKMNPQRSTVWXYZ1.tmp-for-creation": func(p string) error { // a compaction's leftover
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(p, "index"), []byte("x"), 0o644)
		},
		"prometheus/chunks_head/000002": func(p string) error { return os.Symlink("000001", p) },
		"jaeger/notes":                  func(p string) error { return os.MkdirAll(p, 0o755) },
	} {
		d := fakeState(t)
		if err := mk(filepath.Join(d, name)); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeTree(d, t.TempDir()); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err %v, want one naming it", name, err)
		}
	}
	// A segment that does not read stops it, named.
	d = fakeState(t)
	write(t, filepath.Join(d, "prometheus", "wal", "00000001"), string(frag(2, []byte("first"))))
	if _, err := decodeTree(d, t.TempDir()); err == nil || !strings.Contains(err.Error(), "prometheus/wal/00000001") {
		t.Errorf("a torn segment: err %v", err)
	}
}

func TestRunDecode(t *testing.T) {
	d := fakeState(t)
	var o, e bytes.Buffer
	out := filepath.Join(t.TempDir(), "decoded")
	if rc := run(context.Background(), []string{"-decode", d, "-out", out}, &o, &e); rc != 0 {
		t.Fatalf("exit %d: %s", rc, e.String())
	}
	var sum decodeSummary
	if err := json.Unmarshal(o.Bytes(), &sum); err != nil || sum.WALSegments != 2 || sum.Tables != 1 {
		t.Errorf("stdout %q: %+v, %v", o.String(), sum, err)
	}
	for _, args := range [][]string{
		{"-decode", d}, // no -out
		{"-decode", d, "-out", filepath.Join(d, "prometheus", "x")}, // -out inside what it reads
		{"-decode", d, "-out", out},                                 // -out not empty now
		{"-gate-running", "-out", t.TempDir()},                      // -out without -decode
		{"-decode", d, "-gate-running"},                             // two modes
	} {
		o.Reset()
		e.Reset()
		if rc := run(context.Background(), args, &o, &e); rc != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, rc, e.String())
		}
	}
	write(t, filepath.Join(d, "prometheus", "stray.bin"), "x")
	if rc := run(context.Background(), []string{"-decode", d, "-out", t.TempDir()}, &o, &e); rc != 2 || !strings.Contains(e.String(), "prometheus/stray.bin") {
		t.Errorf("a stray file: exit %d, %q", rc, e.String())
	}
}
