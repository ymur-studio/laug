package report

// evidence.go renders the committed documents report -docs writes under docs/: EVIDENCE.md, the Markdown ledger
// of every gate run, drawn from the summary tables' rows (gates and rejudge), and the documents' figures,
// docs/img/<name>.svg, with their index, docs/img/README.md. make docs runs report -docs, which writes the tables
// too, adding rows only (docstables.go), and TestDocs fails a committed copy that is not what Docs renders now. Two
// renders of the same rows are the same bytes: runs by gate id, recorded text as recorded, LF line ends, and no time,
// host or path but the records' own.

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/ymur-studio/laug/internal/jsonx"
	"github.com/ymur-studio/laug/internal/judge"
	"github.com/ymur-studio/laug/internal/rejudge"
)

// DocsDir is the directory, from the checkout's root, that Docs' paths are under.
const DocsDir = "docs"

// Evidence is the ledger's path under DocsDir.
const Evidence = "EVIDENCE.md"

// figuresDir is the documents' figures' directory under DocsDir.
const figuresDir = "img"

// Learning is the page, under DocsDir, that shows the documents' figures. A checkout without it, such as a test's
// fixture of runs alone, gets EVIDENCE.md and no figure.
const Learning = "LEARNING.md"

// A File is one file Docs renders: its path under DocsDir, slash-separated, and its bytes.
type File struct {
	Path string
	Data []byte
}

// hostLabel is a host as its runs record it, never inferred: "m8i.8xlarge (32 vCPU)", or "32 vCPU, type not
// recorded".
func hostLabel(h Host) string {
	vcpu := "vCPU not recorded"
	if h.VCPU.OK {
		vcpu = h.VCPU.String() + " vCPU"
	}
	if h.Type == "" {
		return vcpu + ", type not recorded"
	}
	return h.Type + " (" + vcpu + ")"
}

// orNot is s, or what says it was not recorded.
func orNot(s, what string) string {
	if s == "" {
		return what + " not recorded"
	}
	return s
}

// plural is n and the word for it.
func plural(n int, one, more string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, more)
}

// Docs renders the committed documents, each path under DocsDir: EVIDENCE.md first, from ts, the tables as -docs
// writes them (AddRows), then, when the checkout rs was read under holds docs/LEARNING.md, the lessons' figures,
// img/<name>.svg in the lessons' order, the pages' figures after them, and their index, img/README.md (lessons.go).
// Each figure is drawn from the committed files it names, one by one, never a glob, so runs added to gate/results
// change no figure. rs must have been judged again, every run (Records.NotRun empty), so that ts holds this judge's
// rejudge rows: the ledger ends in their counts line.
func Docs(rs *Records, ts []Table) ([]File, error) {
	if rs == nil || len(rs.Runs) == 0 {
		return nil, errors.New("report: no gate run to write docs/" + Evidence + " from")
	}
	if rs.NotRun != "" || rs.Counts == nil {
		return nil, fmt.Errorf("report: docs/%s ends in the rejudge's counts line, and the rejudge did not run: %s",
			Evidence, rs.NotRun)
	}
	ev, err := evidence(ts, judge.Self())
	if err != nil {
		return nil, err
	}
	files := []File{{Evidence, ev}}
	if rs.checkout == nil {
		return files, nil
	}
	if _, err := fs.Stat(rs.checkout, DocsDir+"/"+Learning); errors.Is(err, fs.ErrNotExist) {
		return files, nil
	} else if err != nil {
		return nil, err
	}
	figs, err := lessonFiles(rs.checkout)
	if err != nil {
		return nil, err
	}
	for _, f := range figs {
		files = append(files, File{figuresDir + "/" + f.Path, f.Data})
	}
	return files, nil
}

// Stale lists the SVG files under DocsDir's img/ in checkout, the checkout's root, that files does not hold: a figure
// Docs writes no longer. Each is its path from the checkout's root, sorted. A checkout with no docs/img has none.
func Stale(checkout fs.FS, files []File) ([]string, error) {
	ents, err := fs.ReadDir(checkout, DocsDir+"/"+figuresDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, e := range ents {
		p := figuresDir + "/" + e.Name()
		if path.Ext(e.Name()) != ".svg" || slices.ContainsFunc(files, func(f File) bool { return f.Path == p }) {
			continue
		}
		stale = append(stale, DocsDir+"/"+p)
	}
	return stale, nil
}

// evidence is EVIDENCE.md, from the gates, rejudge and records rows of ts: what the page is, the runs by verdict, a
// row a gates row with its host as recorded, the judge version its recorded judge names (the judge's name is applied
// here, never stored), the verdict and the deciding rule, then the judge that judged every run again, where the runs
// it counts are kept (a clone holds the gate records the records rows mark kept), and the counts line of the rejudge
// rows that judge keys. It refuses ts with no gates row or no rejudge row of id's judge.
func evidence(ts []Table, id judge.Identity) ([]byte, error) {
	gates, ok, err := tableValues(ts, "gates")
	if err != nil {
		return nil, err
	}
	if !ok || len(gates) == 0 {
		return nil, errors.New("report: no gates row to write docs/" + Evidence + " from")
	}
	rejudged, _, err := tableValues(ts, "rejudge")
	if err != nil {
		return nil, err
	}
	counts := rejudge.Counts{}
	for _, r := range rejudged {
		if str(at(r, "judge_sha256")) == id.SHA256 {
			counts[rejudge.Outcome(str(at(r, "outcome")))]++
		}
	}
	if len(counts) == 0 {
		return nil, fmt.Errorf("report: docs/%s ends in the rejudge's counts line, and no rejudge row is of the "+
			"judge with sources' sha256 %s", Evidence, id.SHA256)
	}
	judged := 0
	for _, n := range counts {
		judged += n
	}
	recs, _, err := tableValues(ts, "records")
	if err != nil {
		return nil, err
	}
	kept := 0
	for _, r := range recs {
		if k, _ := at(r, "kept").Bool(); k && str(at(r, "kind")) == kindGate {
			kept++
		}
	}
	var b strings.Builder
	advance, block := 0, 0
	for _, g := range gates {
		switch str(at(g, "verdict")) {
		case "ADVANCE":
			advance++
		case "BLOCK":
			block++
		}
	}
	fmt.Fprintf(&b, `# Evidence

This page is an appendix, for checking a gate run that a page names by its id.

Every gate run, a row a run by gate id, drawn from docs/tables/: gates.jsonl's row for the host, the judge version
the run's gate.json names, the verdict and the rule that decided it; rejudge.jsonl's rows for the last line.
`+"`make docs`"+` writes this page and the tables with the renderer under _report,
`+"`go -C _report run ./cmd/report -docs`"+`, which adds rows and never changes or removes one, and a test in
`+"`make test-go`"+` fails when it differs from what the renderer writes now.

%s: %d ADVANCE, %d BLOCK.

| Gate run | Host | Judge | Verdict | Deciding rule |
| --- | --- | --- | --- | --- |
`, plural(len(gates), "gate run", "gate runs"), advance, block)
	for _, g := range gates {
		v := gateJudge(g).Name
		if v == "" {
			v = "not named"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", str(at(g, "id")), mdCell(hostLabel(gateHost(g))), mdCell(v),
			mdCell(orNot(str(at(g, "verdict")), "verdict")), mdCell(gateDeciding(g)))
	}
	fmt.Fprintf(&b, `
Each run is judged again in Go, under the judge version its gate.json records, as `+"`bin/laug gate rejudge -all`"+`
judges it. The judge's identity, as `+"`bin/laug judge -identity`"+` prints it: version %s for a new run, sources'
sha256 %s. The sources are internal/judge/versions.txt
and the .go files in internal/judge and internal/jsonx that are not tests. A run judged in Go records both in
gate.json, and `+"`bin/laug gate rejudge`"+` picks its judge by the version. The %d are the lab tree's, kept outside
this one; a clone holds the %s kept in `+"`gate/results/`"+` (`+"`gate/results/README.md`"+`).

`+"```text"+`
laug · rejudge, %s
`+"```"+`
`, id.Version, id.SHA256, judged, plural(kept, "run", "runs"), counts)
	return []byte(b.String()), nil
}

// gateHost is a gates row's host, as ReadRun reads gate.json's.
func gateHost(g jsonx.Value) Host {
	h := at(g, "host")
	return Host{Type: str(at(h, "type")), VCPU: num(at(h, "vcpu")), MemGiB: num(at(h, "mem_gib")),
		MemAvailGiB: num(at(h, "mem_available_gib"))}
}

// gateJudge is the registry's version for the judge a gates row records, zero when the registry names none.
func gateJudge(g jsonx.Value) judge.Version {
	v, _ := rejudge.Recorded{SHA256: str(at(g, "combine_sha256")), Entry: str(at(g, "judge_entry")),
		Sources: str(at(g, "judge_sources"))}.Version()
	return v
}

// gateDeciding is what Run.Deciding says for the run of a gates row: "refused: <why>", "stopped at <step>", the
// deciding rule's id, or "".
func gateDeciding(g jsonx.Value) string {
	if s := str(at(g, "refused")); s != "" {
		return "refused: " + s
	}
	if s := str(at(g, "stopped_at")); s != "" {
		return "stopped at " + s
	}
	return str(at(g, "deciding"))
}

// mdEscaper writes recorded text into a Markdown table cell: a backslash before each character that would end the
// cell or start a code span, a bracketed link, an HTML tag or emphasis with * (GitHub Flavored Markdown 0.29,
// sections 2.4 and 4.10). It leaves _, ~ and & as they are: they can still mark emphasis, a strikethrough or an
// entity. A bare www. or http address still links. A newline is a space.
var mdEscaper = strings.NewReplacer(`\`, `\\`, "|", `\|`, "`", "\\`", "*", `\*`, "[", `\[`, "]", `\]`, "<", `\<`,
	"\r\n", " ", "\n", " ", "\r", " ")

func mdCell(s string) string { return mdEscaper.Replace(s) }
