package report

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/tables"
)

// The gates TestPageCounts names by id, each the first gate of its boot: where a page's count starts or stops, where
// a session it leaves out starts, or the boot whose gates a count reads. Each is in gate/sets/warm-up-m8i.8xlarge.txt,
// whose citations give its transcript line; the stack's transcript is archived, and its line is the quotes row
// docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:490.
const (
	stackFirst = "g20261018-225322" // the stack's session, w0 (its quotes row, above)
	boot4First = "g20261024-085719" // boot 4's W1 (cells/results/2026-10-24-boot4.txt:58-59)
	boot6First = "g20261027-054034" // boot 6's W1 (cells/results/2026-10-27-boot6.txt:400)
)

// pageText is a committed page with its lines joined by spaces, so that a phrase a line break splits is found whole.
func pageText(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(data)), " ")
}

// tableRowsOn is every row of table name under docs/tables, as committed.
func tableRowsOn(t *testing.T, name string) []jsonx.Value {
	t.Helper()
	rows, err := tables.Read(os.DirFS(root), DocsDir+"/"+TablesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]jsonx.Value, len(rows))
	for i, r := range rows {
		out[i] = r.Value
	}
	return out
}

// rowBoot is a gates row's boot: its host boot as an id, b and the boot epoch (EpochOf) as a gate id writes a time,
// "" with none. Boots sort in time.
func rowBoot(g jsonx.Value) string { return str(at(g, "boot")) }

// near says two boots are one: both named, the same.
func near(a, b string) bool { return a != "" && a == b }

// TestPageCounts holds each count a page states, that the report's rows can give, to the committed rows under
// docs/tables, never to a run directory: the rejudge's counts line on docs/CAPABILITIES.md; the A/A gates to boot 6
// that BLOCKed, and the counted gates none of which read over a max, on README.md and docs/DESIGN.md; the validation
// list's R7 count, its bound and its largest d against its tolerance, and boot 4's noise ladder, on
// docs/CAPABILITIES.md. Each count is a query over gates, gate-items, gate-readings, compare and rejudge rows and the
// committed sets, written out here.
func TestPageCounts(t *testing.T) {
	sets, err := ReadSets(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]Set{}
	control := map[string]bool{}
	for _, s := range sets {
		set[s.Kind+" "+s.Label] = s
		if s.Kind == KindControl {
			for _, id := range s.IDs {
				control[id] = true
			}
		}
	}
	gates := map[string]jsonx.Value{}
	var ids []string
	for _, g := range tableRowsOn(t, "gates") {
		id := str(at(g, "id"))
		gates[id] = g
		ids = append(ids, id)
	}
	readme, design := pageText(t, "README.md"), pageText(t, "docs/DESIGN.md")
	capabilities := pageText(t, "docs/CAPABILITIES.md")
	has := func(page, name, phrase string) {
		t.Helper()
		if !strings.Contains(page, phrase) {
			t.Errorf("%s does not say %q, which the rows give", name, phrase)
		}
	}

	// The rejudge's counts line: this judge's rejudge rows by outcome.
	counts := map[string]int{}
	n := 0
	for _, r := range tableRowsOn(t, "rejudge") {
		if str(at(r, "judge_sha256")) == judge.Self().SHA256 {
			counts[str(at(r, "outcome"))]++
			n++
		}
	}
	has(capabilities, "docs/CAPABILITIES.md", fmt.Sprintf("%d runs: %d identical, %d known, %d re-rendered, "+
		"%d unexplained, %d not judged", n, counts["identical"], counts["known"], counts["re-rendered"],
		counts["unexplained"], counts["not judged"]))

	// The A/A gates: gate/plan.yaml with the release its own incumbent, 1.31.0, on an m8i.8xlarge, not refused and
	// in no control set; the stack's session left out, its gates a list of their own (_obs/aa/).
	epoch := func(id string) string { return rowBoot(gates[id]) }
	var aa []string
	for _, id := range ids {
		g := gates[id]
		if str(at(g, "plan")) == "gate/plan.yaml" && str(at(g, "server_tag")) == "1.31.0" &&
			str(at(g, "incumbent")) == "1.31.0" && gateHost(g).Type == "m8i.8xlarge" && str(at(g, "refused")) == "" &&
			!control[id] && !near(rowBoot(g), epoch(stackFirst)) {
			aa = append(aa, id)
		}
	}
	upTo := func(first string) (in []string, blocks []string) {
		for _, id := range aa {
			if b := rowBoot(gates[id]); b != "" && b <= epoch(first) {
				in = append(in, id)
				if str(at(gates[id], "verdict")) == "BLOCK" {
					blocks = append(blocks, id)
				}
			}
		}
		return in, blocks
	}
	to6, blocks6 := upTo(boot6First)
	has(readme, "README.md", fmt.Sprintf("%d BLOCKs in %d A/A gates to boot 6", len(blocks6), len(to6)))
	has(design, "docs/DESIGN.md", fmt.Sprintf("%d BLOCKs in %d A/A gates to boot 6, each a host's first or second gate",
		len(blocks6), len(to6)))
	has(design, "docs/DESIGN.md", fmt.Sprintf("False BLOCKs: %d BLOCKs in %d A/A gates to boot 6", len(blocks6),
		len(to6)))
	// Each BLOCK a box's first or second gate since its start: of its boot's gates that were not refused.
	for _, id := range blocks6 {
		var boot []string
		for _, other := range ids {
			if near(epoch(other), epoch(id)) && str(at(gates[other], "refused")) == "" {
				boot = append(boot, other)
			}
		}
		if i := slices.Index(boot, id); i < 0 || i > 1 {
			t.Errorf("%s BLOCKed as gate %d of its boot, not its first or second", id, i+1)
		}
	}

	// The counted gates to boot 6: the calibration and validation sets, and the A/A gates from boot 4 on, less their
	// warm-ups. None read over a max: no gate-readings row EXCEEDS, and R4 passed.
	counted := slices.Concat(set["calibration arm-3item-m8i.8xlarge"].IDs, set["validation arm-3item-m8i.8xlarge"].IDs)
	for _, id := range to6 {
		if epoch(id) >= epoch(boot4First) && !slices.Contains(set["warm-up m8i.8xlarge"].IDs, id) {
			counted = append(counted, id)
		}
	}
	read, over := map[string]bool{}, map[string]bool{}
	for _, r := range tableRowsOn(t, "gate-readings") {
		g := str(at(r, "gate"))
		if !slices.Contains(counted, g) {
			continue
		}
		read[g] = true
		if strings.HasPrefix(str(at(r, "result")), "EXCEEDS") {
			over[g] = true
		}
	}
	for _, id := range counted {
		if !read[id] {
			t.Errorf("counted gate %s has no gate-readings row", id)
		}
		if pass, _ := at(gates[id], "rules", "R4", "pass").Bool(); !pass {
			over[id] = true
		}
	}
	has(design, "docs/DESIGN.md", fmt.Sprintf("%d of %d counted over a max", len(over), len(counted)))

	// The validation list: its R7 BLOCKs, the bound they give (boundPct, below), and its largest judged d against
	// its tolerance.
	val := set["validation arm-3item-m8i.8xlarge"].IDs
	r7 := 0
	for _, id := range val {
		if r7Failed(gates[id]) {
			r7++
		}
	}
	pct, _ := boundPct(len(val), r7)
	has(capabilities, "docs/CAPABILITIES.md", fmt.Sprintf("%d validation A/A gates followed on fresh clones",
		len(val)))
	has(capabilities, "docs/CAPABILITIES.md", fmt.Sprintf("%d R7 BLOCKs, which bounds R7's false-BLOCK rate at %.1f%%",
		r7, pct))
	most := 0.0
	for _, r := range tableRowsOn(t, "compare") {
		d, tol := num(at(r, "d")), num(at(r, "tolerance"))
		if judged, _ := at(r, "judged").Bool(); judged && d.OK && slices.Contains(val, str(at(r, "gate"))) {
			most = math.Max(most, math.Abs(d.F)/tol.F)
		}
	}
	has(capabilities, "docs/CAPABILITIES.md", fmt.Sprintf("every d at most %.2f of its tolerance",
		math.Floor(most*100)/100))

	// Boot 4's noise ladder: gate/plans/noise.yaml's gates of boot 4, by N, their items' count; each N's items that
	// ADVANCEd, and the range of two readings over its items, to 2 places, with their maxima.
	type rung struct {
		gates, items, advance int
		s2s, e2e              []float64
		s2sMax, e2eMax        string
	}
	ladder := map[int]*rung{}
	var ns []int
	item := map[string]int{} // a noise gate's N
	for _, id := range ids {
		if str(at(gates[id], "plan")) == "gate/plans/noise.yaml" && near(epoch(id), epoch(boot4First)) {
			item[id] = 0
		}
	}
	itemVerdicts := map[string][]string{}
	for _, r := range tableRowsOn(t, "gate-items") {
		if g := str(at(r, "gate")); hasKey(item, g) {
			itemVerdicts[g] = append(itemVerdicts[g], str(at(r, "verdict")))
		}
	}
	for id := range item {
		item[id] = len(itemVerdicts[id])
		r := ladder[item[id]]
		if r == nil {
			r = &rung{}
			ladder[item[id]] = r
			ns = append(ns, item[id])
		}
		r.gates++
		r.items += len(itemVerdicts[id])
		for _, v := range itemVerdicts[id] {
			if v == "ADVANCE" {
				r.advance++
			}
		}
	}
	completed := []float64{}
	for _, r := range tableRowsOn(t, "gate-readings") {
		g := str(at(r, "gate"))
		n, ok := item[g]
		if !ok {
			continue
		}
		v := num(at(r, "value"))
		switch str(at(r, "reading")) {
		case "workflow_task_schedule_to_start_p99_ms":
			ladder[n].s2s = append(ladder[n].s2s, v.F)
			ladder[n].s2sMax = limitText(at(r, "limit"))
		case "workflow_end_to_end_p95_ms":
			ladder[n].e2e = append(ladder[n].e2e, v.F)
			ladder[n].e2eMax = limitText(at(r, "limit"))
		case "workflows_completed":
			completed = append(completed, v.F)
		}
	}
	span := func(xs []float64, places int) string {
		lo, hi := slices.Min(xs), slices.Max(xs)
		a, b := strconv.FormatFloat(lo, 'f', places, 64), strconv.FormatFloat(hi, 'f', places, 64)
		if a == b {
			return a
		}
		return a + " to " + b
	}
	slices.Sort(ns)
	for _, n := range ns {
		r := ladder[n]
		label := strconv.Itoa(n)
		switch r.gates {
		case 1:
		case 2:
			label += ", twice"
		default:
			label += fmt.Sprintf(", %d times", r.gates)
		}
		has(capabilities, "docs/CAPABILITIES.md", fmt.Sprintf("| %s | %d of %d | %s | %s |", label, r.advance,
			r.items, span(r.s2s, 2), span(r.e2e, 2)))
		has(capabilities, "docs/CAPABILITIES.md", "Workflow task s2s p99, ms (max "+r.s2sMax+")")
		has(capabilities, "docs/CAPABILITIES.md", "End to end p95, ms (max "+r.e2eMax+")")
	}
	if len(ns) == 0 {
		t.Errorf("no noise gate of boot 4 in the rows")
	}
	has(capabilities, "docs/CAPABILITIES.md", "Completed workflows stayed at "+span(completed, 0)+" a run")
}

// knownSlowUs is the delay, in µs, that README.md and the social card give the known-slow control. Each gate they
// count records it (slowUs): g20261018-163914's known-slow.json says delay_us 25, and its transcript line, the quotes
// row docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:867, says "known-slow 25 us".
const knownSlowUs = "25"

// slowLine is a transcript line's delay for a known-slow gate, "known-slow <N> us". A line of the 1 ms latency toxic,
// "known-slow 1.0", gives none.
var slowLine = regexp.MustCompile(`known-slow ([0-9]+) us\b`)

// TestVerdictLine holds the numbers of README.md's verdict lines, of its two results lines on the sets, and of the
// social card's two verdict lines (docs/brand/laug-social.svg) to the committed sets and the gates rows. The ADVANCE
// count is the validation set's gates that ADVANCEd, out of the set's count; each must be a release against itself.
// The false-BLOCK bound is boundPct over those gates and their BLOCKs. TestPageCounts holds docs/CAPABILITIES.md's
// bound, over R7's BLOCKs; README.md's, over every BLOCK, is held here. The BLOCK count is the BLOCKs among the
// known-slow control's gates that slowUs gives knownSlowUs, out of those gates. Each of those BLOCKs passed R1 to R6,
// every rule but R7's comparison with the incumbent: that is README.md's "every fixed limit held". The card is read as
// the file holds it: a <text> element a line, its pieces of text (its own and its tspans') joined by one space. The
// public tree leaves out the known-slow control's records and the transcripts its set cites. There the test reads
// only the sets, docs/tables, README.md and the card. Where a cited transcript is on disk, the test reads its lines.
func TestVerdictLine(t *testing.T) {
	sets, err := ReadSets(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]Set{}
	for _, s := range sets {
		set[s.Kind+" "+s.Label] = s
	}
	// named is the set gate/sets/<kind>-<label>.txt. A set renamed or gone stops the test here, rather than at "0 of 0".
	named := func(kind, label string) Set {
		t.Helper()
		s, ok := set[kind+" "+label]
		if !ok {
			t.Fatalf("gate/sets/ has no %s-%s.txt: the %s set %s, which README.md's counts read, is gone or renamed",
				kind, label, kind, label)
		}
		return s
	}
	gates := map[string]jsonx.Value{}
	for _, g := range tableRowsOn(t, "gates") {
		gates[str(at(g, "id"))] = g
	}
	arch, err := readArchive(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}

	// The validation set: its gates of a release against itself that ADVANCEd.
	val := named(KindValidation, "arm-3item-m8i.8xlarge")
	advance := 0
	for _, id := range val.IDs {
		g, ok := gates[id]
		switch tag := str(at(g, "server_tag")); {
		case !ok:
			t.Errorf("%s: %s has no gates row", val.Path, id)
		case tag == "" || tag != str(at(g, "incumbent")):
			t.Errorf("%s: %s ran %q against %q, not a release against itself", val.Path, id, tag,
				str(at(g, "incumbent")))
		case str(at(g, "verdict")) == "ADVANCE":
			advance++
		}
	}

	// The known-slow control: its gates that slowUs gives knownSlowUs. Then the ones of those gates that BLOCKed, each
	// with R1 to R6 passed.
	ks := named(KindControl, "arm-known-slow")
	var slow []string
	blocks := 0
	transcripts := map[string][]string{} // the lines of each transcript setCite reads, by path
	for i, id := range ks.IDs {
		g, ok := gates[id]
		if !ok {
			t.Errorf("%s: %s has no gates row", ks.Path, id)
			continue
		}
		us, err := slowUs(transcripts, arch, g, id, ks.Cites[i])
		if err != nil {
			t.Errorf("%s: %s: %v", ks.Path, id, err)
			continue
		}
		if us != knownSlowUs {
			continue
		}
		slow = append(slow, id)
		if str(at(g, "verdict")) != "BLOCK" {
			continue
		}
		blocks++
		for _, r := range []string{"R1", "R2", "R3", "R4", "R5", "R6"} {
			if pass, _ := at(g, "rules", r, "pass").Bool(); !pass {
				t.Errorf("%s: %s BLOCKed with %s not passed, and README.md says every fixed limit held", ks.Path, id, r)
			}
		}
	}
	if len(slow) == 0 {
		t.Errorf("%s: no gate whose records say netem at %s µs", ks.Path, knownSlowUs)
	}

	readme := pageText(t, "README.md")
	pct, ok := boundPct(len(val.IDs), len(val.IDs)-advance)
	if !ok {
		t.Errorf("%s: no bound on %d BLOCKs in %d gates", val.Path, len(val.IDs)-advance, len(val.IDs))
	}
	for _, line := range []string{
		fmt.Sprintf("<code>ADVANCE</code> %d of %d gates of a release against itself<br> <code>BLOCK</code> %d of "+
			"%d gates with %s µs added to every packet into the release's Postgres", advance, len(val.IDs), blocks,
			len(slow), knownSlowUs),
		fmt.Sprintf("%d of %d gates of a release against itself ADVANCEd, which bounds the false-BLOCK rate at %.1f%% "+
			"with 95%% confidence.", advance, len(val.IDs), pct),
		fmt.Sprintf("%s µs of delay on every packet into the release's Postgres BLOCKed %d of %d gates while every "+
			"fixed limit held.", knownSlowUs, blocks, len(slow)),
	} {
		if !strings.Contains(readme, line) {
			t.Errorf("README.md does not say %q, which the sets give", line)
		}
	}
	// The card ships: it is tracked, and check/private.txt does not name it.
	const card = "docs/brand/laug-social.svg"
	lines := svgLines(t, card)
	for _, want := range []string{
		fmt.Sprintf("ADVANCE %d of %d gates of a release against itself", advance, len(val.IDs)),
		fmt.Sprintf("BLOCK %d of %d gates, %s µs delay into Postgres", blocks, len(slow), knownSlowUs),
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("%s has no line %q, which the sets give; its lines: %q", card, want, lines)
		}
	}
}

// svgLines is each <text> element of the SVG at p, a repository-relative path, in the file's order. A line is the
// pieces of character data in its element, its own and its tspans', as the file holds them, joined by one space.
func svgLines(t *testing.T, p string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var lines, pieces []string
	depth := 0 // inside a <text>, the depth below it, from 1; 0 outside one
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return lines
		}
		if err != nil {
			t.Fatalf("%s: not well-formed: %v", p, err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if depth > 0 {
				depth++
			} else if tok.Name.Local == "text" {
				depth, pieces = 1, nil
			}
		case xml.EndElement:
			if depth > 0 {
				if depth--; depth == 0 {
					lines = append(lines, strings.Join(pieces, " "))
				}
			}
		case xml.CharData:
			if depth > 0 {
				pieces = append(pieces, string(tok))
			}
		}
	}
}

// slowUs is the delay, in µs, that known-slow gate id's records give it, "" for none (a latency toxic, or netem not
// applied). g is its gates row; c is the citation its set gives it. While its record, g's record, is kept, the delay is
// in each known-slow.json under it, one for each item g names in known_slow: all applied by netem, all one delay_us.
// A record marked kept false in docs/tables/records.jsonl leaves the public tree. For such a gate the delay is the
// "known-slow <N> us" on the line of c that names id. setCite reads that line as TestSets does: from the transcript
// while it is on disk, from its quotes row once it is archived. It returns an error when neither source names id.
func slowUs(transcripts map[string][]string, arch *archive, g jsonx.Value, id string, c Cite) (string, error) {
	record := str(at(g, "record"))
	if arch.has(record) {
		cited, err := setCite(transcripts, arch, c)
		if err != nil {
			return "", fmt.Errorf("its record %s is kept false, and %v", record, err)
		}
		for _, l := range cited {
			if !strings.Contains(l, id) {
				continue
			}
			if m := slowLine.FindStringSubmatch(l); m != nil {
				return m[1], nil
			}
			return "", nil
		}
		return "", fmt.Errorf("its record %s is kept false, and no line of %s:%d-%d names it", record, c.File, c.From,
			c.To)
	}
	us := ""
	for i, item := range strs(at(g, "known_slow")) {
		p := path.Join(record, item, "known-slow.json")
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return "", fmt.Errorf("its record %s is kept, and %v", record, err)
		}
		v, err := jsonx.Parse(data)
		if err != nil {
			return "", fmt.Errorf("%s: %v", p, err)
		}
		d := num(at(v, "delay_us")).String()
		if applied, _ := at(v, "ok").Bool(); !applied || str(at(v, "instrument")) != "netem" || i > 0 && d != us {
			return "", nil
		}
		us = d
	}
	return us, nil
}

// hasKey says m holds k.
func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}

// limitText is a reading's limit as the row holds it, a number or the text load/verdict.sh wrote.
func limitText(v jsonx.Value) string {
	if n := num(v); n.OK {
		return n.String()
	}
	return str(v)
}

// r7Failed says a gates row records rule R7 and it did not pass: an R7 BLOCK as docs/CAPABILITIES.md counts them.
func r7Failed(g jsonx.Value) bool {
	rules, ok := at(g, "rules").Obj()
	if !ok {
		return false
	}
	r7, ok := rules.Get("R7")
	if !ok {
		return false
	}
	pass, _ := at(r7, "pass").Bool()
	return !pass
}

// boundPct is the one-sided 95% upper bound, in percent, on the rate of an event seen b times in n trials: 1 -
// 0.05^(1/n) when b is 0, else the Clopper-Pearson bound, the p at which seeing b or fewer has a chance of 0.05, by
// bisection. Not OK when n is 0 or b is outside 0 to n. docs/CAPABILITIES.md's false-BLOCK bound is this figure.
func boundPct(n, b int) (float64, bool) {
	switch {
	case n <= 0 || b < 0 || b > n:
		return 0, false
	case b == n:
		return 100, true
	case b == 0:
		return float64(100 * (1 - math.Pow(0.05, 1/float64(n)))), true
	}
	lo, hi := 0.0, 1.0
	for range 200 {
		mid := (lo + hi) / 2
		if binomCDF(n, b, mid) > 0.05 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return float64(100 * ((lo + hi) / 2)), true
}

// binomCDF is the chance of b or fewer events in n trials at rate p, each term from logarithms so that no term
// overflows.
func binomCDF(n, b int, p float64) float64 {
	sum := 0.0
	for k := 0; k <= b; k++ {
		lg := lchoose(n, k) + float64(float64(k)*math.Log(p)) + float64(float64(n-k)*math.Log1p(-p))
		sum += math.Exp(lg)
	}
	return sum
}

func lchoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}
