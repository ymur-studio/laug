package orchestrator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ymur-studio/laug/internal/denylist"
)

// laug orchestrator record: a completed Gate history, made fit to commit as a replay fixture. The scrub rewrites what
// names this host, its user, the home path and this checkout's queue; Leaks then looks for any of them left, inside
// the payloads too. protojson writes a Payload's data as base64, so neither a scan of the marshaled bytes nor the
// tree's scans would see a path inside one. Each ItemProbe history on the dev server held 15 to 21 decoded payload
// lines with an absolute path, none visible in the raw JSON. A history with any leak is not written.

// Scrub is what a history must not name: the host name, the user name, the home directory, the checkout's root (the
// resolved form is added), this checkout's queue, the boot ids (ScrubHistory adds each boot_id value it meets), and
// Deny, the rules of the -denylist file as internal/denylist reads them, each named by its line in the file.
type Scrub struct {
	Host, User, Home, Root, Queue string
	BootIDs                       []string
	Deny                          []denylist.Rule
}

// Leak is a string in a history that names something Scrub lists. Kind is host, user, home, home-root, root, queue,
// boot_id, "denylist line <n>", "encoding <name>" (a payload whose encoding Leaks cannot read) or sticky-host (a sticky
// queue's name that does not start "host:"). Path is the JSON path from the event down, a payload's decoded JSON after
// ":$". The text matched is never kept. A host or user match inside another check's match, the user name inside the
// home directory, is that check's leak alone: the string is refused either way.
type Leak struct {
	Kind    string `json:"kind"`
	EventID int64  `json:"event_id"`
	Path    string `json:"path"`
}

// LeakReport is Leaks with what it read: string fields, and payloads.
type LeakReport struct {
	Leaks             []Leak
	Strings, Payloads int
}

const (
	zeroBootID = "00000000-0000-0000-0000-000000000000"
	zeroQueue  = "laug-gate-00000000"
)

var anyQueueRE = regexp.MustCompile(`laug-gate-[0-9a-f]{8}`)

func (s Scrub) roots() []string {
	var rs []string
	for _, r := range []string{s.Root, resolved(s.Root)} {
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if r == "/" || r == "." {
			continue
		}
		if len(rs) == 0 || rs[0] != r {
			rs = append(rs, r)
		}
	}
	return rs
}

// homeRoot is the directory that holds the home directory, with a trailing slash: built at run time, never spelled
// in a tracked file, since a deny list may hold it as a line.
func (s Scrub) homeRoot() string {
	if s.Home == "" {
		return ""
	}
	d := filepath.Dir(filepath.Clean(s.Home))
	if d == "/" || d == "." {
		return ""
	}
	return d + "/"
}

// ScrubHistory rewrites h in place. It walks every message of every event by protoreflect, so no event type is
// missed. A string field named identity keeps laug-gate, laug-cli and laug-record, makes laug-worker@<commit> plain
// laug-worker, and makes anything else scrubbed (the CLI's default identity is temporal-cli:<user>@<host>). Any other
// string field: a leading "<Host>:" becomes "host:"; a laug-gate-<8 hex> queue becomes laug-gate-00000000; each boot id becomes the
// zero id; Root, both forms, becomes "." as replacePath does; Home becomes "~". A Payload with encoding json/plain is
// decoded with UseNumber (digits kept), and in it a string under the key boot_id becomes the zero id, a string under
// the key verdict stays as it is (S1 compares its bytes), and a string starting with "/" becomes a shown path (under
// Root relative, else "(outside the repository)/<base>") before the string rules; then it is encoded again, compact,
// keys sorted. Replay decodes by name, and compares neither activity inputs nor the result's payload (SDK
// v1.49.0 WorkflowReplayer.replayWorkflowHistoryRoot). binary_checksum, worker versions, sdk_metadata, request ids and
// event times stay: the replayer reads SDK flags from sdk_metadata (history.isNextWorkflowTaskFailed).
// A TaskQueue of kind sticky is scrubbed by its form, whatever host it names: the SDK names it <worker's
// host>:<uuid> (SDK v1.49.0 getWorkerTaskQueue, getHostName), and the worker may have run on another host
// than the recorder, or under another name, so everything before the last ":" becomes "host".
// counts has identities, sticky queues, queue names, boot ids and paths.
func ScrubHistory(h *historypb.History, s Scrub) (counts map[string]int, err error) {
	x := &scrubber{s: s, roots: s.roots(), counts: map[string]int{"identities": 0, "sticky queues": 0, "queue names": 0, "boot ids": 0, "paths": 0}}
	x.boot = append(x.boot, s.BootIDs...)
	// First every boot_id value, so that a string naming one before its key is met is scrubbed too.
	collect := visitor{
		str: func(_ protoreflect.FieldDescriptor, v, _ string) string { return v },
		payload: func(p *commonpb.Payload, _ string) error {
			if string(p.GetMetadata()["encoding"]) != "json/plain" {
				return nil
			}
			v, err := decodeJSON(p.GetData())
			if err != nil {
				return nil
			}
			eachString(v, "", func(key, str string) {
				if key == "boot_id" && str != "" && str != zeroBootID {
					x.boot = append(x.boot, str)
				}
			})
			return nil
		},
	}
	for i, e := range h.GetEvents() {
		if err := walk(e.ProtoReflect(), fmt.Sprintf("events[%d]", i), collect); err != nil {
			return x.counts, err
		}
	}
	scrub := visitor{str: x.field, payload: x.payload, msg: x.sticky}
	for i, e := range h.GetEvents() {
		if err := walk(e.ProtoReflect(), fmt.Sprintf("events[%d]", i), scrub); err != nil {
			return x.counts, err
		}
	}
	return x.counts, nil
}

type scrubber struct {
	s      Scrub
	roots  []string
	boot   []string
	counts map[string]int
}

// sticky rewrites a sticky TaskQueue's name to host:<uuid>, before its fields are walked.
func (x *scrubber) sticky(m protoreflect.Message, _ string) {
	tq, ok := m.Interface().(*taskqueuepb.TaskQueue)
	if !ok || tq.GetKind() != enumspb.TASK_QUEUE_KIND_STICKY {
		return
	}
	name := tq.GetName()
	i := strings.LastIndex(name, ":")
	if i < 0 || name[:i] == "host" {
		return
	}
	tq.Name = "host" + name[i:]
	x.counts["sticky queues"]++
}

func (x *scrubber) field(fd protoreflect.FieldDescriptor, v, _ string) string {
	if fd.Name() == "identity" {
		return x.identity(v)
	}
	return x.text(v)
}

func (x *scrubber) identity(v string) string {
	switch {
	case v == "", v == "laug-worker", v == "laug-gate", v == "laug-cli", v == "laug-record":
		return v
	case strings.HasPrefix(v, "laug-worker@"):
		x.counts["identities"]++
		return "laug-worker"
	}
	x.counts["identities"]++
	return "scrubbed"
}

func (x *scrubber) text(v string) string {
	out := v
	if h := x.s.Host; h != "" && strings.HasPrefix(out, h+":") {
		out = "host:" + out[len(h)+1:]
		x.counts["sticky queues"]++
	}
	if n := anyQueueRE.ReplaceAllString(out, zeroQueue); n != out {
		out = n
		x.counts["queue names"]++
	}
	for _, b := range x.boot {
		if b != "" && b != zeroBootID && strings.Contains(out, b) {
			out = strings.ReplaceAll(out, b, zeroBootID)
			x.counts["boot ids"]++
		}
	}
	for _, r := range x.roots {
		if n := replacePath(out, r, "."); n != out {
			out = n
			x.counts["paths"]++
		}
	}
	if h := filepath.Clean(x.s.Home); x.s.Home != "" && h != "/" {
		if n := replacePath(out, h, "~"); n != out {
			out = n
			x.counts["paths"]++
		}
	}
	return out
}

// shown is gate/lib.sh's shown for an absolute path: relative under Root, else "(outside the repository)/<base>".
func (x *scrubber) shown(p string) string {
	c := filepath.Clean(p)
	for _, r := range x.roots {
		if rel, err := filepath.Rel(r, c); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel
		}
	}
	return "(outside the repository)/" + filepath.Base(c)
}

func (x *scrubber) payload(p *commonpb.Payload, path string) error {
	if string(p.GetMetadata()["encoding"]) != "json/plain" {
		return nil
	}
	v, err := decodeJSON(p.GetData())
	if err != nil {
		return fmt.Errorf("%s: a json/plain payload that does not parse: %w", path, err)
	}
	b, err := encodeJSON(x.jsonValue(v, ""))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	p.Data = b
	return nil
}

func (x *scrubber) jsonValue(v any, key string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = x.jsonValue(e, k)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = x.jsonValue(e, "")
		}
		return t
	case string:
		switch key {
		case "boot_id":
			if t == "" || t == zeroBootID {
				return t
			}
			x.counts["boot ids"]++
			return zeroBootID
		case "verdict":
			return t
		}
		s := t
		if strings.HasPrefix(s, "/") {
			s = x.shown(s)
			x.counts["paths"]++
		}
		return x.text(s)
	}
	return v
}

// Leaks walks a scrubbed history as ScrubHistory does and returns every string that names something s lists: every
// string field, and in every Payload, json/plain data decoded and each string in it (a string under the key verdict
// base64-decoded and read too), binary/plain data as bytes, binary/null nothing, and any other encoding a Leak of its
// own. The host name matches as a word only: the byte before and the byte after are not [A-Za-z0-9_], or are the
// string's edge. A short name matches ordinary words as a substring: a 3-letter user name inside mem_available_gib, in
// a real gate's verdict bytes, refused its history
// (docs/tables/records.jsonl#cells/results/2026-10-16-gate6-runs.txt, section 3). The user name matches as a word, and
// then only where it names the user: a home-path component (after the home root, or ~<user>) or <user>@. As a bare word
// it is no leak: on the lab host a 3-letter user name matched "-lab-" in the evidence path
// cells/results/2026-10-17-lab-arm-calibration.txt (its row in docs/tables/records.jsonl) and refused two histories
// (G2r and F9, cells/results/2026-10-24-boot4.txt). The home directory, the home root, the checkout's root, the queue
// and the boot ids are paths and long strings, and match anywhere. A deny list line matches by internal/denylist's
// rule, as check/denylist.sh reads it: a plain line anywhere, ASCII case folded, and a re: line a line of the string at
// a time. A map's key, a JSON object's or a proto map's such as a memo field's name, is checked as a string too, and a
// key that matches shows in a leak's path as [key <i>], its place among the keys in order, so no path shows what
// leaked.
func Leaks(h *historypb.History, s Scrub) []Leak { return ScanLeaks(h, s).Leaks }

// ScanLeaks is Leaks with the count of strings and payloads it read.
func ScanLeaks(h *historypb.History, s Scrub) LeakReport {
	l := &leakScan{seen: map[Leak]bool{}}
	add := func(kind, needle string) {
		if needle != "" {
			l.checks = append(l.checks, check{kind: kind, needle: needle})
		}
	}
	if s.Host != "" {
		l.checks = append(l.checks, check{kind: "host", needle: s.Host, match: matchWord})
	}
	if s.User != "" {
		l.checks = append(l.checks, check{kind: "user", needle: s.User, match: matchUser})
	}
	l.homeRoot = s.homeRoot()
	if s.Home != "" {
		add("home", filepath.Clean(s.Home))
	}
	add("home-root", s.homeRoot())
	for _, r := range s.roots() {
		add("root", r)
	}
	add("queue", s.Queue)
	for _, b := range s.BootIDs {
		if b != zeroBootID {
			add("boot_id", b)
		}
	}
	for _, r := range s.Deny {
		l.checks = append(l.checks, check{kind: fmt.Sprintf("denylist line %d", r.Line), rule: r, match: matchDeny})
	}
	// The boot ids a payload names are leaks too, unless the scrub zeroed them.
	for _, e := range h.GetEvents() {
		_ = walk(e.ProtoReflect(), "", visitor{
			str: func(_ protoreflect.FieldDescriptor, v, _ string) string { return v },
			payload: func(p *commonpb.Payload, _ string) error {
				if v, err := decodeJSON(p.GetData()); err == nil && string(p.GetMetadata()["encoding"]) == "json/plain" {
					eachString(v, "", func(key, str string) {
						if key == "boot_id" && str != "" && str != zeroBootID {
							add("boot_id", str)
						}
					})
				}
				return nil
			},
		})
	}
	for i, e := range h.GetEvents() {
		l.event = e.GetEventId()
		_ = walk(e.ProtoReflect(), fmt.Sprintf("events[%d]", i), visitor{
			str: func(_ protoreflect.FieldDescriptor, v, path string) string {
				l.scan(v, path)
				return v
			},
			payload: l.payload,
			key:     l.key,
			msg: func(m protoreflect.Message, path string) {
				if tq, ok := m.Interface().(*taskqueuepb.TaskQueue); ok && tq.GetKind() == enumspb.TASK_QUEUE_KIND_STICKY &&
					!strings.HasPrefix(tq.GetName(), "host:") {
					l.add(Leak{Kind: "sticky-host", EventID: l.event, Path: path + ".name"})
				}
			},
		})
	}
	sort.Slice(l.leaks, func(i, j int) bool {
		a, b := l.leaks[i], l.leaks[j]
		if a.EventID != b.EventID {
			return a.EventID < b.EventID
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Kind < b.Kind
	})
	return LeakReport{Leaks: l.leaks, Strings: l.strings, Payloads: l.payloads}
}

// check is one thing Leaks looks for: as a substring, as a word (the host name), as the user (the user name), or as a
// rule of the deny list.
type check struct {
	kind, needle string
	match        int
	rule         denylist.Rule
}

const (
	matchSub = iota
	matchWord
	matchUser
	matchDeny
)

type leakScan struct {
	checks            []check
	homeRoot          string
	leaks             []Leak
	seen              map[Leak]bool
	strings, payloads int
	event             int64
}

// scan reports each check str matches, at path.
func (l *leakScan) scan(str, path string) {
	l.strings++
	for _, kind := range l.kinds(str) {
		l.add(Leak{Kind: kind, EventID: l.event, Path: path})
	}
}

// kinds are the kinds of the checks str matches: the substring checks and the deny list's rules first, then the word
// checks outside their matches.
func (l *leakScan) kinds(str string) []string {
	var out []string
	var spans [][2]int
	for _, c := range l.checks {
		if c.match == matchDeny {
			if sp := c.rule.Spans(str); len(sp) > 0 {
				spans = append(spans, sp...)
				out = append(out, c.kind)
			}
			continue
		}
		if c.match != matchSub {
			continue
		}
		found := false
		for i := 0; ; {
			j := strings.Index(str[i:], c.needle)
			if j < 0 {
				break
			}
			spans = append(spans, [2]int{i + j, i + j + len(c.needle)})
			found = true
			i += j + 1
		}
		if found {
			out = append(out, c.kind)
		}
	}
	for _, c := range l.checks {
		var hit bool
		switch c.match {
		case matchWord:
			hit = hasWord(str, c.needle, spans, nil)
		case matchUser:
			hit = hasWord(str, c.needle, spans, func(i, end int) bool { return namesUser(str, i, end, l.homeRoot) })
		}
		if hit {
			out = append(out, c.kind)
		}
	}
	return out
}

// key scans k, a map's key, as a string, and returns the path below it: seg, the key as written, or "[key <i>]", i
// its place among the map's keys in order, when a check matches k, so a leak's path never shows what leaked.
func (l *leakScan) key(path, k, seg string, i int) string {
	kp := path + seg
	if len(l.kinds(k)) > 0 {
		kp = fmt.Sprintf("%s[key %d]", path, i)
	}
	l.scan(k, kp)
	return kp
}

// namesUser says whether the word s[i:end] names the user: after ~, before @, or a home-path component, after root
// (the home root, with its trailing slash).
func namesUser(s string, i, end int, root string) bool {
	if i > 0 && s[i-1] == '~' || end < len(s) && s[end] == '@' {
		return true
	}
	return root != "" && strings.HasSuffix(s[:i], root)
}

// hasWord says whether w is in s as a word, the bytes either side of it not [A-Za-z0-9_] or the string's edge, at
// least once outside every span in inside and, when ok is not nil, where ok(start, end) holds.
func hasWord(s, w string, inside [][2]int, ok func(start, end int) bool) bool {
	for i := 0; ; i++ {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		i += j
		end := i + len(w)
		if (i > 0 && wordByte(s[i-1])) || (end < len(s) && wordByte(s[end])) || ok != nil && !ok(i, end) {
			continue
		}
		covered := false
		for _, sp := range inside {
			if sp[0] <= i && end <= sp[1] {
				covered = true
				break
			}
		}
		if !covered {
			return true
		}
	}
}

func wordByte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

func (l *leakScan) add(k Leak) {
	if !l.seen[k] {
		l.seen[k] = true
		l.leaks = append(l.leaks, k)
	}
}

func (l *leakScan) payload(p *commonpb.Payload, path string) error {
	l.payloads++
	enc := string(p.GetMetadata()["encoding"])
	switch enc {
	case "json/plain":
		v, err := decodeJSON(p.GetData())
		if err != nil {
			l.add(Leak{Kind: "encoding json/plain that does not parse", EventID: l.event, Path: path})
			return nil
		}
		l.json(v, path+".data:$", "")
	case "binary/plain":
		l.scan(string(p.GetData()), path+".data")
	case "binary/null":
	default:
		l.add(Leak{Kind: "encoding " + enc, EventID: l.event, Path: path})
	}
	return nil
}

func (l *leakScan) json(v any, path, key string) {
	switch t := v.(type) {
	case map[string]any:
		ks := make([]string, 0, len(t))
		for k := range t {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for i, k := range ks {
			l.json(t[k], l.key(path, k, jsonKey(k), i), k)
		}
	case []any:
		for i, e := range t {
			l.json(e, fmt.Sprintf("%s[%d]", path, i), "")
		}
	case string:
		l.scan(t, path)
		if key == "verdict" {
			if b, err := base64.StdEncoding.DecodeString(t); err == nil {
				l.scan(string(b), path)
			}
		}
	}
}

var jsonIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func jsonKey(k string) string {
	if jsonIdentRE.MatchString(k) {
		return "." + k
	}
	return fmt.Sprintf("[%q]", k)
}

// eachString calls f with every string in a decoded JSON value and the key it sits under ("" in an array).
func eachString(v any, key string, f func(key, s string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			eachString(e, k, f)
		}
	case []any:
		for _, e := range t {
			eachString(e, "", f)
		}
	case string:
		f(key, t)
	}
}

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("data after the JSON value")
	}
	return v, nil
}

func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// visitor sees each message before its fields (msg, when set), each string field of a message, which it may rewrite,
// each Payload, which it may rewrite in place, and each string key of a map (key, when set), whose path below it,
// from path and seg, the key as written, it returns.
type visitor struct {
	str     func(fd protoreflect.FieldDescriptor, v, path string) string
	payload func(p *commonpb.Payload, path string) error
	msg     func(m protoreflect.Message, path string)
	key     func(path, k, seg string, i int) string
}

// walk visits every string field and every Payload of m and the messages it holds, lists and maps included. path is
// the JSON path down to m, protojson's field names.
func walk(m protoreflect.Message, path string, v visitor) error {
	if p, ok := m.Interface().(*commonpb.Payload); ok {
		return v.payload(p, path)
	}
	if v.msg != nil {
		v.msg(m, path)
	}
	type set struct {
		fd  protoreflect.FieldDescriptor
		val protoreflect.Value
	}
	var sets []set
	var err error
	isMsg := func(k protoreflect.Kind) bool { return k == protoreflect.MessageKind || k == protoreflect.GroupKind }
	m.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		p := path + "." + fd.JSONName()
		switch {
		case fd.IsList():
			l := val.List()
			for i := 0; i < l.Len() && err == nil; i++ {
				ip := fmt.Sprintf("%s[%d]", p, i)
				switch {
				case isMsg(fd.Kind()):
					err = walk(l.Get(i).Message(), ip, v)
				case fd.Kind() == protoreflect.StringKind:
					s := l.Get(i).String()
					if ns := v.str(fd, s, ip); ns != s {
						l.Set(i, protoreflect.ValueOfString(ns))
					}
				}
			}
		case fd.IsMap():
			mp := val.Map()
			var keys []protoreflect.MapKey
			mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool { keys = append(keys, k); return true })
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for i, k := range keys {
				kp := fmt.Sprintf("%s[%q]", p, k.String())
				if v.key != nil && fd.MapKey().Kind() == protoreflect.StringKind {
					kp = v.key(p, k.String(), fmt.Sprintf("[%q]", k.String()), i)
				}
				switch vd := fd.MapValue(); {
				case isMsg(vd.Kind()):
					err = walk(mp.Get(k).Message(), kp, v)
				case vd.Kind() == protoreflect.StringKind:
					s := mp.Get(k).String()
					if ns := v.str(vd, s, kp); ns != s {
						mp.Set(k, protoreflect.ValueOfString(ns))
					}
				}
				if err != nil {
					break
				}
			}
		case isMsg(fd.Kind()):
			err = walk(val.Message(), p, v)
		case fd.Kind() == protoreflect.StringKind:
			s := val.String()
			if ns := v.str(fd, s, p); ns != s {
				sets = append(sets, set{fd, protoreflect.ValueOfString(ns)})
			}
		}
		return err == nil
	})
	for _, s := range sets {
		m.Set(s.fd, s.val)
	}
	return err
}
