package orchestrator

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The scrub's world: host myhost, user someuser, a home directory and a checkout made at run time. The home root is
// filepath.Dir of the home directory, a temp directory: no test spells a real one.
func scrubWorld(t *testing.T) Scrub {
	t.Helper()
	return Scrub{Host: "myhost", User: "someuser", Home: filepath.Join(t.TempDir(), "someuser"), Root: filepath.Join(t.TempDir(), "laug"),
		Queue: "laug-gate-1a2b3c4d", BootIDs: []string{"5e1d8a2c-0000-4000-8000-00000000b001"}}
}

func jsonPayload(s string) *commonpb.Payload {
	return &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(s)}
}

func completed(id int64, identity, result string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
			Identity: identity, Result: &commonpb.Payloads{Payloads: []*commonpb.Payload{jsonPayload(result)}}}}}
}

// synthetic is a history with what a real one names: the starter's identity and an input with a path under the
// checkout and one outside it, a sticky queue and the checkout's queue, a worker's identity and an answer with a boot
// id and a wall time, and the CLI's default identity on a cancel.
func synthetic(s Scrub) *historypb.History {
	input := fmt.Sprintf(`{"gate_results":%q,"omes_dir":"/tmp/x","gid":"g20261016-120000","dir":"gate/results/g20261016-120000"}`, s.Root+"/gate/results")
	return &historypb.History{Events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				WorkflowType: &commonpb.WorkflowType{Name: "Gate"}, Identity: "laug-gate",
				TaskQueue: &taskqueuepb.TaskQueue{Name: s.Queue, Kind: enumspb.TASK_QUEUE_KIND_NORMAL},
				Input:     &commonpb.Payloads{Payloads: []*commonpb.Payload{jsonPayload(input)}}}}},
		{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{
				TaskQueue: &taskqueuepb.TaskQueue{Name: "myhost:1234", Kind: enumspb.TASK_QUEUE_KIND_STICKY, NormalName: s.Queue}}}},
		completed(3, "laug-worker@abc1234", `{"name":"open","exit":0,"result":"next","answer":{"result":"next","boot_id":"b0","t0":{"wall":1792152000.120}}}`),
		{EventId: 4, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCEL_REQUESTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionCancelRequestedEventAttributes{WorkflowExecutionCancelRequestedEventAttributes: &historypb.WorkflowExecutionCancelRequestedEventAttributes{
				Identity: "temporal-cli:u@myhost"}}},
	}}
}

func TestScrubHistory(t *testing.T) {
	s := scrubWorld(t)
	h := synthetic(s)
	counts, err := ScrubHistory(h, s)
	if err != nil {
		t.Fatal(err)
	}
	if l := Leaks(h, s); len(l) != 0 {
		t.Fatalf("leaks after the scrub: %+v", l)
	}
	ev := h.GetEvents()
	if got := []string{ev[0].GetWorkflowExecutionStartedEventAttributes().GetIdentity(), ev[2].GetActivityTaskCompletedEventAttributes().GetIdentity(),
		ev[3].GetWorkflowExecutionCancelRequestedEventAttributes().GetIdentity()}; strings.Join(got, " ") != "laug-gate laug-worker scrubbed" {
		t.Fatalf("identities %v", got)
	}
	tq := ev[1].GetWorkflowTaskScheduledEventAttributes().GetTaskQueue()
	if tq.GetName() != "host:1234" || tq.GetNormalName() != "laug-gate-00000000" || ev[0].GetWorkflowExecutionStartedEventAttributes().GetTaskQueue().GetName() != "laug-gate-00000000" {
		t.Fatalf("queues %q, %q", tq.GetName(), tq.GetNormalName())
	}
	in := string(ev[0].GetWorkflowExecutionStartedEventAttributes().GetInput().GetPayloads()[0].GetData())
	if !strings.Contains(in, `"gate_results":"gate/results"`) || !strings.Contains(in, `"omes_dir":"(outside the repository)/x"`) {
		t.Fatalf("input after the scrub: %s", in)
	}
	res := string(ev[2].GetActivityTaskCompletedEventAttributes().GetResult().GetPayloads()[0].GetData())
	if !strings.Contains(res, `"boot_id":"00000000-0000-0000-0000-000000000000"`) || !strings.Contains(res, "1792152000.120") {
		t.Fatalf("result after the scrub: %s", res)
	}
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"myhost", "someuser", s.Root, s.Home} {
		if strings.Contains(string(b), bad) {
			t.Errorf("the marshalled history still holds %q", bad)
		}
	}
	if counts["identities"] != 2 || counts["sticky queues"] != 1 || counts["queue names"] != 2 || counts["boot ids"] != 1 || counts["paths"] != 2 {
		t.Errorf("counts %v", counts)
	}
}

// A sticky queue is scrubbed by its form: the worker's host may not be the recorder's (another host, or a rename
// between the gate and the record), and Leaks reports a sticky name the scrub missed.
func TestScrubStickyOfAnotherHost(t *testing.T) {
	s := scrubWorld(t)
	h := synthetic(s)
	tq := h.GetEvents()[1].GetWorkflowTaskScheduledEventAttributes().GetTaskQueue()
	tq.Name = "ip-10-9-8-7:5f2c-uuid"
	sticky := func() []Leak {
		var ls []Leak
		for _, l := range Leaks(h, s) {
			if l.Kind == "sticky-host" {
				ls = append(ls, l)
			}
		}
		return ls
	}
	if l := sticky(); len(l) != 1 || l[0].Path != "events[1].workflowTaskScheduledEventAttributes.taskQueue.name" {
		t.Fatalf("before the scrub: sticky-host leaks %+v, want one", l)
	}
	counts, err := ScrubHistory(h, s)
	if err != nil {
		t.Fatal(err)
	}
	if tq.GetName() != "host:5f2c-uuid" || counts["sticky queues"] != 1 {
		t.Fatalf("sticky name %q, counts %v", tq.GetName(), counts)
	}
	if l := Leaks(h, s); len(l) != 0 {
		t.Fatalf("leaks after the scrub: %+v", l)
	}
	// A normal queue's name is no sticky one, whatever it holds.
	h.GetEvents()[0].GetWorkflowExecutionStartedEventAttributes().GetTaskQueue().Name = "a:b"
	if l := sticky(); len(l) != 0 {
		t.Fatalf("a normal queue reported: %+v", l)
	}
}

func TestLeaksReadsPayloads(t *testing.T) {
	type leakCase struct {
		ev   *historypb.HistoryEvent
		deny string
		kind string
		path string
	}
	why := `{"name":"lease-pl","exit":0,"result":"refused","answer":{"result":"refused","why":"cannot write ~someuser/out"}}`
	verdict := base64.StdEncoding.EncodeToString([]byte(`{"note":"myhost"}`))
	for name, c := range map[string]leakCase{
		"a path inside a longer string, under neither the root nor home": {completed(5, "laug-worker@abc1234", why), "", "user",
			"events[4].activityTaskCompletedEventAttributes.result.payloads[0].data:$.answer.why"},
		"a verdict's base64": {completed(5, "laug-worker@abc1234", `{"name":"judge","verdict":"`+verdict+`"}`), "", "host",
			"events[4].activityTaskCompletedEventAttributes.result.payloads[0].data:$.verdict"},
		"a denylist line": {completed(5, "laug-worker@abc1234", `{"name":"x","answer":{"note":"a tiger"}}`), "tiger\n", "denylist line 1",
			"events[4].activityTaskCompletedEventAttributes.result.payloads[0].data:$.answer.note"},
		"an encoding Leaks cannot read": {&historypb.HistoryEvent{EventId: 5, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
				Result: &commonpb.Payloads{Payloads: []*commonpb.Payload{{Metadata: map[string][]byte{"encoding": []byte("binary/foo")}, Data: []byte("x")}}}}}},
			"", "encoding binary/foo", "events[4].activityTaskCompletedEventAttributes.result.payloads[0]"},
	} {
		s := scrubWorld(t)
		s.Deny = denyRules(t, c.deny)
		h := synthetic(s)
		h.Events = append(h.Events, c.ev)
		if _, err := ScrubHistory(h, s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		l := Leaks(h, s)
		if len(l) != 1 || l[0].Kind != c.kind || l[0].EventID != 5 || l[0].Path != c.path {
			t.Errorf("%s: leaks %+v, want one %s at event 5, %s", name, l, c.kind, c.path)
		}
	}

	// A string holding the home directory: a leak before the scrub, none after.
	s := scrubWorld(t)
	h := synthetic(s)
	h.Events = append(h.Events, completed(5, "laug-worker@abc1234", fmt.Sprintf(`{"name":"x","answer":{"log":%q}}`, "see "+s.Home+"/go/x.log")))
	home := func() bool {
		for _, l := range Leaks(h, s) {
			if l.Kind == "home" && l.EventID == 5 {
				return true
			}
		}
		return false
	}
	if !home() {
		t.Fatal("no home leak before the scrub")
	}
	if _, err := ScrubHistory(h, s); err != nil {
		t.Fatal(err)
	}
	if home() || len(Leaks(h, s)) != 0 {
		t.Fatalf("leaks after the scrub: %+v", Leaks(h, s))
	}
	if d := string(h.Events[4].GetActivityTaskCompletedEventAttributes().GetResult().GetPayloads()[0].GetData()); !strings.Contains(d, `"see ~/go/x.log"`) {
		t.Fatalf("home after the scrub: %s", d)
	}
}

// The host name matches as a word, and the user name as a word that names the user (TestLeaksUser), so a 3-letter
// user name inside an ordinary word is no leak: "lab" inside mem_available_gib, in a real gate's verdict bytes, refused
// its history once (cells/results/2026-10-16-gate6-runs.txt, section 3). The home directory, the queue and a deny line
// still match anywhere. The home directory is made at run time: no test spells a real home root.
func TestLeaksWordBoundaries(t *testing.T) {
	s := Scrub{Host: "cellbox", User: "lab", Home: filepath.Join(t.TempDir(), "lab"), Root: filepath.Join(t.TempDir(), "laug"),
		Queue: "laug-gate-1a2b3c4d", Deny: denyRules(t, "tig\n")}
	kinds := func(result string) string {
		h := &historypb.History{Events: []*historypb.HistoryEvent{completed(1, "laug-worker", result)}}
		var ks []string
		for _, l := range Leaks(h, s) {
			ks = append(ks, l.Kind)
		}
		return strings.Join(ks, " ")
	}
	note := func(v string) string { return fmt.Sprintf(`{"name":"x","answer":{"note":%q}}`, v) }
	for _, c := range []struct{ in, want string }{
		{"mem_available_gib", ""},
		{s.Home + "/x", "home home-root"},
		{"lab@host", "user"},
		{"user=lab", ""},
		{"lab", ""},
		{"-lab-", ""},
		{"lab_1", ""},
		{"label", ""},
		{"collab9", ""},
		{s.Home + "/x for lab", "home home-root"},
		{"subcellboxes", ""},
		{"cellbox_old", ""},
		{"on cellbox:7233", "host"},
		{"cellbox", "host"},
		{"x" + s.Queue + "y", "queue"},
		{"a tiger", "denylist line 1"},
	} {
		if got := kinds(note(c.in)); got != c.want {
			t.Errorf("%q: leaks %q, want %q", c.in, got, c.want)
		}
	}
	verdict := base64.StdEncoding.EncodeToString([]byte(`{"verdict":"ADVANCE","host":{"mem_available_gib":41.5,"cpus":32}}`))
	if got := kinds(`{"name":"judge","verdict":"` + verdict + `"}`); got != "" {
		t.Errorf("a verdict naming mem_available_gib: leaks %q, want none", got)
	}
	if got := kinds(`{"name":"judge","verdict":"` + base64.StdEncoding.EncodeToString([]byte(`{"note":"ran as lab@cellbox"}`)) + `"}`); got != "host user" {
		t.Errorf("a verdict naming the user: leaks %q, want host user", got)
	}
}

// The user name is a leak where it names the user: a home-path component (after the home root, or ~<user>) or
// <user>@. The home root is made at run time: no test spells a real one. A bare word is none: on the lab host a 3-letter
// user name matched a verdict citing cells/results/2026-10-17-lab-arm-calibration.txt and refused two histories (G2r
// and F9, cells/results/2026-10-24-boot4.txt).
func TestLeaksUser(t *testing.T) {
	root := t.TempDir()
	s := Scrub{Host: "cellbox", User: "lab", Home: filepath.Join(root, "lab"), Root: filepath.Join(t.TempDir(), "laug")}
	kinds := func(result string) string {
		h := &historypb.History{Events: []*historypb.HistoryEvent{completed(1, "laug-worker", result)}}
		var ks []string
		for _, l := range Leaks(h, s) {
			ks = append(ks, l.Kind)
		}
		return strings.Join(ks, " ")
	}
	verdict := func(v string) string {
		return `{"name":"judge","verdict":"` + base64.StdEncoding.EncodeToString([]byte(v)) + `"}`
	}
	const calibration = "cells/results/2026-10-17-lab-arm-calibration.txt"
	if got := kinds(verdict(`{"verdict":"ADVANCE","comparison":{"evidence":"` + calibration + `"}}`)); got != "" {
		t.Errorf("a verdict citing %s: leaks %q, want none", calibration, got)
	}
	for _, c := range []struct{ in, want string }{
		{calibration, ""},
		{"./" + calibration, ""},
		{root + "/lab/laug/gate/results", "home home-root"},
		{root + "/lab", "home home-root"},
		{"~lab/laug", "user"},
		{"~lab", "user"},
		{"lab@cellbox", "host user"},
		{"ssh lab@db", "user"},
		{"/srv/lab/x", ""},
		{"collab@x", ""},
		{"~labs", ""},
		{"ran as lab", ""},
	} {
		if got := kinds(fmt.Sprintf(`{"name":"x","answer":{"note":%q}}`, c.in)); got != c.want {
			t.Errorf("%q: leaks %q, want %q", c.in, got, c.want)
		}
		if got := kinds(verdict(fmt.Sprintf(`{"note":%q}`, c.in))); got != c.want {
			t.Errorf("%q in a verdict: leaks %q, want %q", c.in, got, c.want)
		}
	}
	// After the home root the user name names the user, whatever home the scrub names. hasWord holds the word's
	// edges ("~labs" above).
	hr := s.homeRoot()
	for _, c := range []struct {
		in   string
		want bool
	}{
		{hr + "lab/laug", true},
		{hr + "lab", true},
		{hr + "x/lab/y", false},
		{"/srv/lab/x", false},
	} {
		i := strings.LastIndex(c.in, "lab")
		if got := namesUser(c.in, i, i+len("lab"), hr); got != c.want {
			t.Errorf("namesUser(%q): %v, want %v", c.in, got, c.want)
		}
	}
}

// A key is checked as a string, a JSON object's and a proto map's such as a memo field's name, and a key that matches
// shows in a leak's path by its place among the keys, so no path holds what leaked; a clean key shows as written.
func TestLeakKeys(t *testing.T) {
	s := Scrub{Host: "myhost", Deny: denyRules(t, "# planted\nzorblax\nre:qx7[0-9]\n")}
	memo := func(fields map[string]string) *historypb.History {
		m := &commonpb.Memo{Fields: map[string]*commonpb.Payload{}}
		for k, v := range fields {
			m.Fields[k] = jsonPayload(v)
		}
		return &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 2,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
				WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Memo: m}}}}}
	}
	result := func(s string) *historypb.History {
		return &historypb.History{Events: []*historypb.HistoryEvent{completed(1, "laug-worker", s)}}
	}
	const res, mem = "events[0].activityTaskCompletedEventAttributes.result.payloads[0].data:$.answer",
		"events[0].workflowExecutionStartedEventAttributes.memo.fields"
	for _, c := range []struct {
		name string
		h    *historypb.History
		want []string
	}{
		{"a JSON key", result(`{"answer":{"ZorBlax":"fine","note":"fine"}}`), []string{"denylist line 2 at " + res + "[key 0]"}},
		{"a JSON key a re: line matches", result(`{"answer":{"a":"fine","qx79 note":"fine"}}`),
			[]string{"denylist line 3 at " + res + "[key 1]"}},
		{"a memo field's name, its value clean", memo(map[string]string{"zorblax": `"fine"`}),
			[]string{"denylist line 2 at " + mem + "[key 0]"}},
		{"a memo field's name, its value the host", memo(map[string]string{"note": `"fine"`, "zorblax": `"myhost"`}),
			[]string{"denylist line 2 at " + mem + "[key 1]", "host at " + mem + "[key 1].data:$"}},
		{"the host as a memo field's name", memo(map[string]string{"myhost": `"fine"`}), []string{"host at " + mem + "[key 0]"}},
		{"a clean memo field's name, as written", memo(map[string]string{"note": `"myhost"`}),
			[]string{"host at " + mem + `["note"].data:$`}},
	} {
		var got []string
		for _, l := range Leaks(c.h, s) {
			got = append(got, l.Kind+" at "+l.Path)
			if low := strings.ToLower(l.Path); strings.Contains(low, "zorblax") || strings.Contains(low, "qx7") ||
				strings.Contains(low, "myhost") {
				t.Errorf("%s: a leak's path holds what leaked: %s", c.name, l.Path)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: leaks %q, want %q", c.name, got, c.want)
		}
	}
}
