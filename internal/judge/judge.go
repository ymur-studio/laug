package judge

import (
	"io/fs"

	"github.com/ymur-studio/laug/internal/jsonx"
)

// A Result is what `laug judge <dir>` makes of a run: the two files -write writes, and what it prints.
type Result struct {
	Version Version
	JSON    []byte // verdict.json: Evaluate's object in jq's default form
	MD      []byte // verdict.md: Render over JSON read back
	Verdict string // ADVANCE or BLOCK, as `jq -r .verdict` reads it from JSON
	summary []byte // Summary over JSON read back
}

// Stdout is what laug judge -write prints for the run given dir as its argument: the summary, the line naming
// the two files and the verdict alone. dir is used as given, so "runs/g1/" prints "runs/g1//verdict.json",
// as the shell does.
func (r *Result) Stdout(dir string) []byte {
	b := append([]byte(nil), r.summary...)
	b = append(b, "written          "+dir+"/verdict.json, "+dir+"/verdict.md\n"...)
	return append(b, r.Verdict+"\n"...)
}

// Summary is what SUMMARY prints for the run: the lines of stdout before the files' line.
func (r *Result) Summary() []byte { return append([]byte(nil), r.summary...) }

// Exit is laug judge's status for a run it judged: 0 for ADVANCE, 1 for anything else.
func (r *Result) Exit() int {
	if r.Verdict == "ADVANCE" {
		return 0
	}
	return 1
}

// A Failure is a run whose files Gather read but that the judge cannot judge: its jq stops in JUDGE or in
// RENDER, so it exits 2 and writes neither file. A gathered input that does not parse, a *StreamError, stops
// JUDGE too. A failure in SUMMARY would come after both files are written, and exit 5; no verdict Evaluate
// builds makes one, so Judge reports it as a Failure too, and writes nothing.
type Failure struct {
	Step string // "judge", "render" or "summary"
	Err  error  // the jq error
}

func (f *Failure) Error() string {
	switch f.Step {
	case "render":
		return "verdict.md could not be rendered, so no verdict is written: " + f.Err.Error()
	case "summary":
		return "the summary could not be printed: " + f.Err.Error()
	}
	return "the judge failed, so no verdict is written: " + f.Err.Error()
}

func (f *Failure) Unwrap() error { return f.Err }

// Judge judges the run in fsys, its results directory, as `laug judge <dir>` does under version v: it gathers
// the run with v's Arm (opts.Arm is set from v), evaluates JUDGE, writes verdict.json in jq's default form, and
// reads those bytes back to render verdict.md and the summary, as the shell judge's three jq calls read the file.
// Nothing is written. The errors are laug judge's exit 2: ErrNoRunDir, a *StreamError and a *Failure.
func Judge(fsys fs.FS, v Version, opts Options) (*Result, error) {
	opts.Arm = v.Arm
	in, err := Gather(fsys, opts)
	if err != nil {
		return nil, err
	}
	out, err := Evaluate(v, in.Value)
	if err != nil {
		return nil, &Failure{Step: "judge", Err: err}
	}
	js := jsonx.Marshal(out)
	back := readBack(js)
	md, err := Render(v, back)
	if err != nil {
		return nil, &Failure{Step: "render", Err: err}
	}
	sum, err := Summary(back)
	if err != nil {
		return nil, &Failure{Step: "summary", Err: err}
	}
	verdict, _ := field(back, "verdict").Str() // a string: Evaluate's $verdict
	return &Result{Version: v, JSON: js, MD: md, Verdict: verdict, summary: sum}, nil
}
