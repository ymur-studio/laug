package orchestrator

// RecordMain writes a completed Gate history as a replay fixture: laug orchestrator record <gid> <name>
// -overwrite-fixture runs it in laug's own process against -orchestrator, read only, as identity laug-record.
//
// It fetches workflow gate-<gid>'s whole history and refuses one whose last event is not WorkflowExecutionCompleted:
// replay checks the completion command against it (SDK v1.49.0 replayWorkflowHistoryRoot). Then
// it scrubs the history (ScrubHistory: identities, the sticky queue's host, this checkout's queue, boot ids, the
// checkout's root and the home directory, inside decoded payloads too) and looks for anything left (ScanLeaks: the
// host, the user, the home directory and the home root, the root, the queue, the boot ids and each rule of -denylist,
// the list check/denylist.sh reads, by the rule it reads it by: internal/denylist). Any leak exits 2 with one line per
// leak, its kind, event id and JSON path, never the text it matched, and nothing is written. Else it writes the
// history as protojson with a two-space indent, the form ReplayWorkflowHistoryFromJSONFile reads, as health's fixture
// is written (internal/health, CheckMain's -export).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ymur-studio/laug/internal/denylist"
)

// recordIdentity is the recorder's identity: it starts nothing and writes nothing to the server.
const recordIdentity = "laug-record"

// RecordMain parses args with its own flags, the ones the record binary took, and records on ctx. cmd is the laug
// command it runs for, orchestrator record, which names its refusal: "laug · <cmd>: <why>". environ is the environment
// laug built for it, which it reads in place of the process's: TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE, TEMPORAL_API_KEY
// and HOME, the path the scrub takes out. Exit 0 written; 2 refused, a leak, or a flag it does not take.
func RecordMain(ctx context.Context, cmd string, args, environ []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(stderr)
	repo := f.String("repo", "", "the checkout's root; laug passes its own")
	gid := f.String("gate", "", "the gate id, g<date>-<time>; the workflow is gate-<gid>")
	out := f.String("out", "", "the fixture to write, internal/orchestrator/testdata/histories/<name>.json; a "+
		"relative path is from -repo")
	deny := f.String("denylist", "", "the deny list, read as check/denylist.sh reads it (internal/denylist); a "+
		"relative path is from -repo; a missing file is no error")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	code, err := record(ctx, *repo, *gid, *out, *deny, environ, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "laug · %s: %v\n", cmd, err)
	}
	return code
}

// readDeny reads -denylist by internal/denylist's rule, a relative path from the root, as -out is: laug passes
// LAUG_DENYLIST's value from wherever it runs, and a list missed for the working directory would be no error. None
// named, or none there, gives no rules. An error names the list as given and the line it refuses, never its text.
func readDeny(root, p string) (*denylist.List, error) {
	if p == "" {
		return nil, nil
	}
	path := p
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	l, err := denylist.Read(path)
	if err != nil {
		var le *denylist.LineError
		if errors.As(err, &le) {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		return nil, fmt.Errorf("%s does not read", p)
	}
	return l, nil
}

// recordPaths are the checkout's root, absolute, and the file to write: a relative -out is from the root, as laug
// passes it, wherever laug runs from. The output names -out as given, so a line pasted into a result holds no home
// path.
func recordPaths(repo, out string) (root, dst string, err error) {
	if root, err = filepath.Abs(repo); err != nil {
		return "", "", err
	}
	dst = out
	if !filepath.IsAbs(dst) {
		dst = filepath.Join(root, dst)
	}
	return root, dst, nil
}

func record(ctx context.Context, repo, gid, out, denyFile string, environ []string, stdout, stderr io.Writer) (int,
	error) {
	laug := laugWord(environ)
	switch {
	case repo == "" || out == "":
		return 2, fmt.Errorf("-repo and -out are required: run %s orchestrator record <gate id> <name> "+
			"-overwrite-fixture", laug)
	case !IsGateID(gid):
		return 2, fmt.Errorf("-gate %q: a gate id, g<date>-<time>, as %s gate run -workflow printed it", gid, laug)
	}
	root, dst, err := recordPaths(repo, out)
	if err != nil {
		return 2, err
	}
	addr := getenv(environ, "TEMPORAL_ADDRESS")
	if addr == "" {
		return 2, fmt.Errorf("TEMPORAL_ADDRESS is empty: run %s orchestrator record, which sets it from "+
			"-orchestrator", laug)
	}
	ns := getenv(environ, "TEMPORAL_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	queue, err := Queue(filepath.Join(root, RunDir), false, laug)
	if err != nil {
		return 2, err
	}
	deny, err := readDeny(root, denyFile)
	if err != nil {
		return 2, err
	}
	var rules []denylist.Rule
	if deny != nil {
		rules = deny.Rules
	}
	host, _ := os.Hostname()
	var uname string
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	// HOME is environ's, as the recorder read it when it ran on its own; this process's when environ has none, so
	// the scrub always has a home to take out.
	home := getenv(environ, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	sc := Scrub{Host: host, User: uname, Home: home, Root: root, Queue: queue,
		BootIDs: []string{strings.TrimSpace(string(boot))}, Deny: rules}

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	opts := client.Options{HostPort: addr, Namespace: ns, Identity: recordIdentity}
	if key := getenv(environ, "TEMPORAL_API_KEY"); key != "" {
		opts.Credentials = client.NewAPIKeyStaticCredentials(key)
	}
	c, err := client.DialContext(ctx, opts)
	if err != nil {
		return 2, fmt.Errorf("dial %s: %w (is %s orchestrator server running?)", addr, err, laug)
	}
	defer c.Close()
	id := "gate-" + gid
	iter := c.GetWorkflowHistory(ctx, id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var h historypb.History
	for iter.HasNext() {
		e, err := iter.Next()
		if err != nil {
			return 2, fmt.Errorf("history of %s: %w", id, err)
		}
		h.Events = append(h.Events, e)
	}
	n := len(h.Events)
	if n == 0 {
		return 2, fmt.Errorf("%s has no history on %s, namespace %s", id, addr, ns)
	}
	if last := h.Events[n-1].GetEventType(); last != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED {
		return 2, fmt.Errorf("%s's last event is %s, not WorkflowExecutionCompleted: record a gate that has ended", id, last)
	}
	end := "unknown"
	if ps := h.Events[n-1].GetWorkflowExecutionCompletedEventAttributes().GetResult().GetPayloads(); len(ps) > 0 {
		var res GateResult
		if json.Unmarshal(ps[0].GetData(), &res) == nil && res.End != "" {
			end = res.End
		}
	}

	counts, err := ScrubHistory(&h, sc)
	if err != nil {
		return 2, fmt.Errorf("scrub %s: %w", id, err)
	}
	rep := ScanLeaks(&h, sc)
	if len(rep.Leaks) > 0 {
		for _, l := range rep.Leaks {
			fmt.Fprintf(stderr, "leak %s: event %d, %s\n", l.Kind, l.EventID, l.Path)
		}
		return 2, fmt.Errorf("%s: %d leak(s) after the scrub; nothing written", id, len(rep.Leaks))
	}
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(&h)
	if err != nil {
		return 2, err
	}
	// The first fixture makes testdata/histories/, which git does not keep empty. A leak, above, makes nothing.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 2, err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return 2, err
	}
	fmt.Fprintf(stdout, "laug · recorded %s: %d events, %d bytes, end %s -> %s\n", id, n, len(b), end, out)
	fmt.Fprintf(stdout, "scrubbed: %d identities, %d sticky queues, %d queue names, %d boot ids, %d paths\n",
		counts["identities"], counts["sticky queues"], counts["queue names"], counts["boot ids"], counts["paths"])
	fmt.Fprintf(stdout, "checked: %d strings, %d payloads, %s of the denylist, no leak\n", rep.Strings, rep.Payloads,
		deny.Summary())
	var order []enumspb.EventType
	byType := map[enumspb.EventType]int{}
	for _, e := range h.Events {
		t := e.GetEventType()
		if byType[t] == 0 {
			order = append(order, t)
		}
		byType[t]++
	}
	for _, t := range order {
		fmt.Fprintf(stdout, "%s %d\n", t, byType[t])
	}
	return 0, nil
}
