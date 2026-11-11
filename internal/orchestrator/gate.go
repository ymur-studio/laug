package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Workflow Gate gives laug gate run's verdict from gate/step.sh, one step per activity, in gate/run.sh's
// order. It owns the order, the waits between steps, the backstop and the stop path; step.sh owns what each step does,
// and every refusal (steps refuse themselves: gate/lib.sh, refuse). The starter runs gate/step.sh check first and
// passes the gate id and directory in, so the workflow id is gate-<gid> and history records both from event 1.
//
// The order, with each activity's id (ActivityID), activity, context and StartToClose:
//
//	open           Step open                   ctx      2 m    t0_ms; next
//	preflight      Step preflight              ctx      2 m    the plan's keys and timings; next, judge, refused
//	omes           Step omes                   ctx      6 m
//	laugd          Step laugd                  ctx      1 m
//	harness        Step harness                ctx      1 m
//	lease-<k>      Step lease <k>, plan order  ctx      6 m    next, stop-leasing (ends the loop), judge, refused
//	barrier        Step barrier                ctx      harness_timeout_s + 60 s
//	quiet-<i>      Step quiet <i>              ctx      1 m    quiet; next, refused
//	wave-time      Step wave-time              ctx      1 m
//	wave           Step wave                   waveCtx  deadline_s + kill_grace_s + 120 s   done, items
//	await-<k>      AwaitItem                   waveCtx  as wave                             ItemEnd
//	judge          Step combine                dctx     2 m    verdict, exit, trap; Verdict bytes
//	post           Step post                   dctx     1 m    exit
//	release        Step release                dctx     7 m
//
// and on the stop path, all on dctx: stop-<name> (Stop, kill_grace_s + 70 s), stopped (Step stopped <why>, kill_grace_s
// + 60 s), post-stopped (1 m) and release-stopped (7 m). Every activity has StepOptions(StartToClose, true) and its id:
// replay matches ScheduleActivityTask on activity id and type only (SDK v1.49.0
// isCommandMatchEvent, its ScheduleActivityTask case), and every step is type Step, so with the SDK's sequence-number
// ids Barrier moved before the last Lease would replay clean (ExecuteActivity, getStringID). Each id is used
// once a run: a step runs at most once, the judge path and the stop path never both run post or release, Stop runs
// once, and a retry keeps its id.
//
// Event budget. From the dev server's histories (docs/tables/records.jsonl#cells/results/2026-10-16-gate6-step3.txt
// lines 102-106, 137; the Sleep's 5 from an ItemProbe history of 106 events):
// WorkflowExecutionStarted and the first workflow task, 4 events; an activity alone, 6 (scheduled, started, completed,
// then a workflow task of 3); a Sleep, 5 (TimerStarted, TimerFired, a workflow task); the wave with 2 items, 20 (3
// ActivityTaskScheduled and a TimerStarted, 2 awaits x 5, the wave 5, TimerCanceled); a barrier pass that is not quiet,
// 11 (quiet-<i> 6, Sleep(1 s) 5); WorkflowExecutionCompleted, 1. The sums below are estimates from those pieces, not
// counts. Answers that arrive together share a workflow task, which takes 3 events off each time; an await that answers
// after the wave adds its ActivityTaskCancelRequested, 1. ADVANCE with 2 items and a quiet barrier (quiet, Sleep(10 s),
// quiet: 17) is about 4 + 8 x 6 + 17 + 6 + 20 + 18 + 1 = 114 events (scratch dev servers counted 110 on the
// fake gate, 108, 111 and 116 on fake activities); each more item adds about 11 (a lease and an await): 180 at the
// plan's 8 items. 60 passes not quiet before the passing one: about 774 for 2 items, 840 for 8. The stop path adds 6; a
// cancel adds its CancelRequested event and a workflow task (4) and a cancel request per pending activity (9 at most):
// 859 at worst. The ceiling, 64 passes, no wave, 8 items: about 4 + 14 x 6 + 704 + 18 + 1 = 811. Heartbeats, queries
// and retries add none. A worker restart adds none when the next workflow task comes 27 s or more after the worker
// died, and 2 when it comes within about 3 s: that task goes to the dead worker's sticky queue, times out there 5 s
// later (WorkflowTaskTimedOut) and is scheduled again on the gate's queue (on scratch dev servers, 5
// KILLs mid-wave added none, 108 to 115 events against 108 to 116 with no restart; a KILL 3 s before a Sleep fired
// added the 2; the cutoff between 3 s and 27 s is not measured). Under 900 at the shipped plan (gate/plan.yaml), with
// 40 or more to spare: no continue-as-new. At the plan's largest barrier_timeout_s, 600 (gate/lib.sh), about 6 800:
// past the server's 4 096-event continue-as-new suggestion, which Gate does not read, and under the 10 240-event
// warning (server v1.32.0 common/dynamicconfig/constants.go:483-488, limit.historyCount.suggestContinueAsNew,
// and :452-456, limit.historyCount.warn; SDK v1.49.0 WorkflowInfo.GetContinueAsNewSuggested).

// GateRunInput is laug gate run's inputs and the run check made: GID, the gate id, and Dir, check's answer, shown.
type GateRunInput struct {
	GateInput
	GID string `json:"gid"`
	Dir string `json:"dir"`
}

// GateResult is how Gate ended. End is done (judged, handed on, released), stopped (the stop path ran, or the judge
// wrote the trap verdict) or failed (a bad input, or post erred after the judge). Exit is the gate's code, as laug
// gate run's: 0 ADVANCE, 1 BLOCK, 2 no verdict or a failed hand-on. Verdict is verdict.json as the judge or the stopped
// step read it, byte for byte (base64 on the wire, exact), and SHA256 its hex digest. Steps holds every step's answer,
// in order, without its Verdict and SHA256, which the result carries once (answered); Items each item's end, from its
// await, or else from the wave's or stopped's answer (after the backstop, the wave's .done as Stop read it).
type GateResult struct {
	GateID      string             `json:"gate_id"`
	Dir         string             `json:"dir"`
	End         string             `json:"end"`
	Exit        int                `json:"exit"`
	VerdictWord string             `json:"verdict_word"`
	Verdict     []byte             `json:"verdict"`
	SHA256      string             `json:"sha256"`
	Why         string             `json:"why,omitempty"`
	Steps       []StepAnswer       `json:"steps"`
	Items       map[string]ItemEnd `json:"items"`
	Stop        *StopResult        `json:"stop,omitempty"`
}

// ItemEndAt is an item's end and when the workflow learned it (workflow.Now, UTC).
type ItemEndAt struct {
	ItemEnd
	At time.Time `json:"at"`
}

// GateProgress answers the query "progress". A query writes nothing to history, and neither does SetCurrentDetails,
// which holds Text (SDK v1.49.0 SetCurrentDetails, getWorkflowMetadata). Phase is main, wave, judge,
// stop or ended. Step is the live activity's id, "sleep" in a Sleep, "" once ended; Since is when it began, as
// workflow.Now gives it. Began holds every step and Stop as it began, in order, Sleeps left out, so the starter prints
// each one however short. Done holds the ids that answered, in order, awaits and Stop included. QuietPasses counts the
// quiet steps.
type GateProgress struct {
	GateID          string               `json:"gate_id"`
	Dir             string               `json:"dir"`
	Phase           string               `json:"phase"`
	Step            string               `json:"step"`
	Verb            string               `json:"verb"`
	Key             string               `json:"key,omitempty"`
	Since           time.Time            `json:"since"`
	Began           []GateMark           `json:"began"`
	Done            []string             `json:"done"`
	QuietPasses     int                  `json:"quiet_passes"`
	Items           map[string]ItemEndAt `json:"items"`
	CancelRequested bool                 `json:"cancel_requested"`
	End             string               `json:"end,omitempty"`
	Text            string               `json:"text"`
}

// GateMark is an activity's id and when the workflow scheduled it (workflow.Now, UTC).
type GateMark struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// answerData is what the workflow reads from an answer.
type answerData struct {
	Result          string                 `json:"result"`
	Exit            *int                   `json:"exit"`
	Verdict         *string                `json:"verdict"`
	Trap            bool                   `json:"trap"`
	Quiet           *bool                  `json:"quiet"`
	Keys            []string               `json:"keys"`
	DeadlineS       *int                   `json:"deadline_s"`
	KillGraceS      *int                   `json:"kill_grace_s"`
	QuietS          *int                   `json:"quiet_s"`
	BarrierTimeoutS *int                   `json:"barrier_timeout_s"`
	HarnessTimeoutS *int                   `json:"harness_timeout_s"`
	T0Ms            *int64                 `json:"t0_ms"`
	Items           map[string]*itemAnswer `json:"items"`
}

// itemAnswer is one item in the wave's and stopped's answers (gate/step.sh, item_answer): null when the item left no
// exit.json.
type itemAnswer struct {
	Exit      *int    `json:"exit"`
	StoppedBy *string `json:"stopped_by"`
}

func dataOf(ans StepAnswer) answerData {
	var d answerData
	_ = json.Unmarshal(ans.Answer, &d)
	return d
}

// resultError is an answer whose result the workflow does not take at that step.
type resultError struct{ name, result string }

func (e resultError) Error() string { return fmt.Sprintf("step %s answered %q", e.name, e.result) }

// answerError is an answer the workflow cannot go on from: why names what it lacks.
type answerError struct{ name, why string }

func (e answerError) Error() string { return fmt.Sprintf("step %s: %s", e.name, e.why) }

// ceilingError ends a barrier loop that never ended by its own rules (1.5 of the spec): a host wall clock that steps
// back keeps quiet-<i> answering not quiet without ever refusing.
type ceilingError struct{ passes, barrierTimeout int }

func (e ceilingError) Error() string {
	return fmt.Sprintf("the barrier ran %d passes; barrier_timeout_s is %d s and no quiet-<i> refused", e.passes, e.barrierTimeout)
}

// backstopError: the wave had not answered by t0 + deadline_s + kill_grace_s + 120 s.
type backstopError struct{}

func (backstopError) Error() string {
	return "step wave: no answer by t0 + deadline_s + kill_grace_s + 120 s (the backstop)"
}

type gateRun struct {
	ctx, dctx workflow.Context
	in        GateRunInput
	gid       string
	res       GateResult
	prog      GateProgress
	live      *StepInput // the step in flight; nil during a Sleep and once it answered
	// The plan's timings in seconds, from preflight's answer; until then the plan's defaults (gate/lib.sh,
	// PLAN_DEFAULTS), and deadline_s at its 3600 s upper bound.
	keys                                                    []string
	deadline, killGrace, quiet, harnessTime, barrierTimeout int
	t0                                                      time.Time // open's t0_ms
	// noWorker: no worker took open within its ScheduleToStartTimeout. The stop path's steps then get one too, so a
	// gate that no worker serves ends in about 4 m, not in the 71 m of their ScheduleToCloseTimeouts.
	noWorker    bool
	cancelNoted bool
}

// IsGateID reports whether s is a gate run's id, g<date>-<time>[-<n>], as gate/step.sh check names it.
func IsGateID(s string) bool { return gidRE.MatchString(s) }

// scheduleToStart is open's ScheduleToStartTimeout, and each later activity's once no worker took open.
const scheduleToStart = time.Minute

// barrierPasses is the ceiling on the barrier loop: max(barrier_timeout_s, 60) + 4 passes. While the host wall clock
// moves forward each pass that does not end the loop spends 1 s or more of it, so quiet-<i> refuses within
// barrier_timeout_s + 2 passes.
func barrierPasses(barrierTimeout int) int { return max(barrierTimeout, 60) + 4 }

// stepID is a step's activity id: StepName, but post-stopped and release-stopped for the stop path's post and release.
func stepID(verb, key string) string {
	if (verb == "post" || verb == "release") && key == "stopped" {
		return verb + "-stopped"
	}
	return StepName(verb, key)
}

// options are an activity's options: StepOptions with WaitForCancellation, its id, and open's ScheduleToStartTimeout,
// which every later activity gets too once open's ran out.
func (p *gateRun) options(id string, stc time.Duration) workflow.ActivityOptions {
	o := StepOptions(stc, true)
	o.ActivityID = id
	if id == "open" || p.noWorker {
		o.ScheduleToStartTimeout = scheduleToStart
	}
	return o
}

// Gate runs a gate step by step. The steps before the wave run on the workflow's context, the wave, its awaits and
// the backstop timer on waveCtx, its one WithCancel child, and judge, post, release and the stop path on a
// disconnected context, so a cancel cannot cut them short. It always returns a nil error: the workflow completes, and
// a cancel shows in the result, not as status Canceled.
//
// Replay runs Gate on the recorded workflow input, which laug orchestrator record scrubs: paths in Results, OmesDir,
// Lock, HostLock and Extra become shown paths there. Gate therefore reads GID from its input and every timing from
// answers; the rest of GateRunInput only passes into the activities' inputs and the result. A branch on any input
// field but GID would make every recorded history fail replay.
func Gate(ctx workflow.Context, in GateRunInput) (GateResult, error) {
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	p := &gateRun{ctx: ctx, dctx: dctx, in: in, gid: in.GID, deadline: 3600, killGrace: 30, quiet: 10, harnessTime: 300, barrierTimeout: 60}
	p.res = GateResult{GateID: in.GID, Dir: in.Dir, Steps: []StepAnswer{}, Items: map[string]ItemEnd{}}
	p.prog = GateProgress{GateID: in.GID, Dir: in.Dir, Phase: "main", Began: []GateMark{}, Done: []string{}, Items: map[string]ItemEndAt{}}
	if err := workflow.SetQueryHandler(ctx, "progress", func() (GateProgress, error) {
		c := p.prog
		c.CancelRequested = ctx.Err() != nil
		return c, nil
	}); err != nil {
		p.res.End, p.res.Exit, p.res.Why = "failed", 2, "query progress: "+err.Error()
		p.end()
		return p.res, nil
	}
	if !gidRE.MatchString(in.GID) {
		p.res.End, p.res.Exit, p.res.Why = "failed", 2, fmt.Sprintf("input: gate id %q is not g<date>-<time>", in.GID)
		p.end()
		return p.res, nil
	}
	if err := p.main(); err != nil {
		p.stopPath(err)
	} else if ctx.Err() != nil {
		// A cancel that landed after the wave answered and before the judge was scheduled: the stop path, whose stopped
		// writes the trap verdict at the step .state.json names, as gate/run.sh's EXIT trap would.
		p.stopPath(ctx.Err())
	} else {
		p.judgePath()
	}
	p.end()
	return p.res, nil
}

func (p *gateRun) input(verb, key string) StepInput {
	return StepInput{GateInput: p.in.GateInput, Dir: path.Join(p.in.Results, p.gid), GID: p.gid, Name: StepName(verb, key), Verb: verb, Key: key}
}

// mark is what comes before each activity or Sleep: the progress's step and the current details.
func (p *gateRun) mark(id, verb, key string) {
	p.prog.Step, p.prog.Verb, p.prog.Key = id, verb, key
	p.prog.Since = workflow.Now(p.ctx).UTC()
	if id != "sleep" {
		p.prog.Began = append(p.prog.Began, GateMark{ID: id, At: p.prog.Since})
	}
	p.details()
}

// details sets Text and the workflow's current details from the progress.
func (p *gateRun) details() {
	var b strings.Builder
	if p.prog.Phase == "ended" {
		fmt.Fprintf(&b, "gate %s: ended %s, exit %d", p.gid, p.prog.End, p.res.Exit)
	} else {
		fmt.Fprintf(&b, "gate %s: %s since %s UTC", p.gid, p.prog.Step, p.prog.Since.Format("15:04:05"))
		if ends := p.itemTexts(); len(ends) > 0 {
			b.WriteString(", items ended: " + strings.Join(ends, ", "))
		}
		if p.ctx.Err() != nil {
			b.WriteString(", cancel requested")
		}
	}
	p.prog.Text = b.String()
	workflow.SetCurrentDetails(p.ctx, p.prog.Text)
}

// itemTexts are the ended items, in plan order, then any other key by name: "<k> exit <n>[ after TERM|KILL]".
func (p *gateRun) itemTexts() []string {
	var ks []string
	seen := map[string]bool{}
	for _, k := range p.keys {
		if _, ok := p.prog.Items[k]; ok {
			ks = append(ks, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range p.prog.Items {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	var out []string
	for _, k := range append(ks, rest...) {
		out = append(out, itemText(p.prog.Items[k].ItemEnd))
	}
	return out
}

// itemText is an item's end in one phrase: "<k> exit <n>[ after TERM|KILL]", "<k> exit null[ after ...]" when it was
// still there 10 s after KILL, or "<k> not found: <why>".
func itemText(e ItemEnd) string {
	if !e.Found {
		return fmt.Sprintf("%s not found: %s", e.Key, e.Why)
	}
	x := "null"
	if e.Exit != nil {
		x = strconv.Itoa(*e.Exit)
	}
	s := fmt.Sprintf("%s exit %s", e.Key, x)
	if e.StoppedBy != nil && *e.StoppedBy != "" {
		s += " after " + *e.StoppedBy
	}
	return s
}

func (p *gateRun) end() {
	p.prog.Phase, p.prog.End, p.prog.Step, p.prog.Verb, p.prog.Key = "ended", p.res.End, "", "", ""
	p.details()
}

// answered records a step's answer. The copy in Steps leaves out Verdict and SHA256: judgePath and stopped put them in
// the result once, as Verdict and SHA256. With two copies a 1 MiB verdict.json made a result of 2 796 491 bytes, over
// the server's 2 MiB limit on the completion's result (limit.blobSize.error, server v1.32.0
// common/dynamicconfig/constants.go:387-391): the SDK fails that workflow task on every try, or the server terminates
// the run (SDK v1.49.0 payloadLimitsVisitorImpl.checkPayloadSize), after judge, post and release ran. One copy
// is 1 400 216 bytes (TestGateResultCarriesTheVerdictOnce).
func (p *gateRun) answered(id string, ans StepAnswer) {
	ans.Verdict, ans.SHA256 = nil, ""
	p.res.Steps = append(p.res.Steps, ans)
	p.prog.Done = append(p.prog.Done, id)
}

// itemEnded records an item's end, from its await or an answer.
func (p *gateRun) itemEnded(e ItemEnd) {
	p.res.Items[e.Key] = e
	p.prog.Items[e.Key] = ItemEndAt{ItemEnd: e, At: workflow.Now(p.ctx).UTC()}
	p.details()
}

// fillItems takes the items an answer lists that no await answered: null is an item with no exit.json.
func (p *gateRun) fillItems(items map[string]*itemAnswer, step string) {
	ks := make([]string, 0, len(items))
	for k := range items {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		if _, ok := p.res.Items[k]; ok {
			continue
		}
		e := ItemEnd{Key: k}
		if ia := items[k]; ia != nil {
			e.Found, e.Exit, e.StoppedBy = true, ia.Exit, ia.StoppedBy
		} else {
			e.Why = fmt.Sprintf("%s answered with no %s/exit.json", step, k)
		}
		p.itemEnded(e)
	}
}

// step runs one step on the workflow's context.
func (p *gateRun) step(verb, key string, stc time.Duration) (StepAnswer, error) {
	var a *GateActivities
	si := p.input(verb, key)
	id := stepID(verb, key)
	p.live = &si
	p.mark(id, verb, key)
	var ans StepAnswer
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(p.ctx, p.options(id, stc)), a.Step, si).Get(p.ctx, &ans); err != nil {
		return ans, err
	}
	p.live = nil
	p.answered(id, ans)
	return ans, nil
}

// final runs one of the steps a cancel cannot cut short, on the disconnected context: judge, post, release, stopped.
func (p *gateRun) final(verb, key string, stc time.Duration) (StepAnswer, error) {
	var a *GateActivities
	id := stepID(verb, key)
	p.mark(id, verb, key)
	var ans StepAnswer
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(p.dctx, p.options(id, stc)), a.Step, p.input(verb, key)).Get(p.dctx, &ans)
	if err == nil {
		p.answered(id, ans)
	}
	return ans, err
}

func (p *gateRun) sleep(d time.Duration) error {
	p.live = nil
	p.mark("sleep", "sleep", d.String())
	return workflow.Sleep(p.ctx, d)
}

// jump says whether an answer sends the gate to the judge (judge, refused), and fails on a result the step does not
// give.
func jump(ans StepAnswer, want ...string) (bool, error) {
	switch ans.Result {
	case "judge", "refused":
		return true, nil
	}
	for _, w := range want {
		if ans.Result == w {
			return false, nil
		}
	}
	return false, resultError{ans.Name, ans.Result}
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

// main runs open to the wave, gate/run.sh's order, steps 1 to 9. nil means the judge is next: the wave ended, or a step
// refused or sent the gate to the judge. An error goes to the stop path.
func (p *gateRun) main() error {
	run := func(verb, key string, stc time.Duration, want ...string) (StepAnswer, bool, error) {
		ans, err := p.step(verb, key, stc)
		if err != nil {
			return ans, false, err
		}
		j, err := jump(ans, want...)
		return ans, j, err
	}
	ans, j, err := run("open", "", 2*time.Minute, "next")
	if err != nil || j {
		return err
	}
	// Every timing after this counts from t0, never from the epoch.
	t0 := dataOf(ans).T0Ms
	if t0 == nil {
		return answerError{"open", "answer without t0_ms"}
	}
	p.t0 = time.UnixMilli(*t0)
	ans, j, err = run("preflight", "", 2*time.Minute, "next")
	if err != nil || j {
		return err
	}
	d := dataOf(ans)
	p.keys = d.Keys
	for _, t := range []struct {
		v   *int
		dst *int
	}{{d.DeadlineS, &p.deadline}, {d.KillGraceS, &p.killGrace}, {d.QuietS, &p.quiet}, {d.BarrierTimeoutS, &p.barrierTimeout},
		{d.HarnessTimeoutS, &p.harnessTime}} {
		if t.v != nil {
			*t.dst = *t.v
		}
	}
	for _, s := range []struct {
		verb string
		stc  time.Duration
	}{{"omes", 6 * time.Minute}, {"laugd", time.Minute}, {"harness", time.Minute}} {
		if _, j, err := run(s.verb, "", s.stc, "next"); err != nil || j {
			return err
		}
	}
	for _, k := range p.keys {
		ans, j, err := run("lease", k, 6*time.Minute, "next", "stop-leasing")
		if err != nil || j {
			return err
		}
		if ans.Result == "stop-leasing" {
			break
		}
	}
	if _, j, err := run("barrier", "", secs(p.harnessTime+60), "next"); err != nil || j {
		return err
	}
	// The barrier loop, gate/run.sh's step 6, one pass each time round: quiet-<i> (step.sh refuses on its own laugd's
	// why, and by host wall once barrier_timeout_s has passed since the barrier); quiet, then quiet_s later quiet
	// again, passes; anything else sleeps 1 s and goes round. The ceiling ends a loop no quiet step ever refuses.
	i := 0
	quiet := func() (bool, bool, error) {
		ans, j, err := run("quiet", strconv.Itoa(i), time.Minute, "next")
		i++
		p.prog.QuietPasses = i
		if err != nil || j {
			return false, j, err
		}
		q := dataOf(ans).Quiet
		return q != nil && *q, false, nil
	}
	for passes := 0; ; passes++ {
		if passes >= barrierPasses(p.barrierTimeout) {
			return ceilingError{passes, p.barrierTimeout}
		}
		q, j, err := quiet()
		if err != nil || j {
			return err
		}
		if q {
			if p.quiet > 0 {
				if err := p.sleep(secs(p.quiet)); err != nil {
					return err
				}
			}
			q, j, err = quiet()
			if err != nil || j {
				return err
			}
			if q {
				break
			}
		}
		if err := p.sleep(time.Second); err != nil {
			return err
		}
	}
	if _, j, err := run("wave-time", "", time.Minute, "next"); err != nil || j {
		return err
	}
	return p.wave()
}

// wave runs the wave step, one AwaitItem per item and the backstop timer, all on waveCtx, Gate's only WithCancel
// context. A Selector takes whichever answers first: an await changes progress only; the wave's answer, or the
// backstop, ends the wave. Then cancelWave cancels what still pends, in the order it was scheduled, on waveCtx's one
// Done channel (SDK v1.49.0 channelImpl.Close).
//
// One WithCancel child, and only one: a workflow cancel closes ctx's Done channel and then cancels ctx's children, in
// map order unless SDK flag 9 is on (cancelCtx.cancel, SDKFlagOrderedChildCancel in sdkFlagsAllowed), and the
// worker refuses TEMPORAL_SDK_FLAG_*. With one child there is no order to differ on replay. dctx is no child:
// NewDisconnectedContext skips propagateCancel (NewDisconnectedContext against WithCancel). No
// AwaitWithTimeout either: under flag 6 it adds a WithCancel of its own (awaitWithOptions).
//
// The backstop is t0 + deadline_s + kill_grace_s + 120 s, from workflow.Now, at least 1 s: a timer of 0 or less makes
// no command (NewTimerWithOptions). The deadline_s watchdog stays in the wave step, on the host wall
// clock (gate/lib.sh, watch_items), since a server timer armed before a wall clock step fired 123.5 s late by the
// host's clock (docs/tables/records.jsonl#cells/results/2026-10-14-firecracker-lab.txt). The timer
// backs it up for a wave step that writes no .done while a worker serves the gate. With no worker, no workflow task
// runs, not even the fired timer's, and the worker that comes back adopts the wave or loses it (steps.go, runStep).
func (p *gateRun) wave() error {
	var a *GateActivities
	waveIn := p.input("wave", "")
	stc := secs(p.deadline + p.killGrace + 120)
	waveCtx, cancelWave := workflow.WithCancel(p.ctx)
	p.live = &waveIn
	p.prog.Phase = "wave"
	p.mark("wave", "wave", "")
	waveF := workflow.ExecuteActivity(workflow.WithActivityOptions(waveCtx, p.options("wave", stc)), a.Step, waveIn)
	awaitFs := make([]workflow.Future, len(p.keys))
	for i, k := range p.keys {
		ai := AwaitInput{GateInput: p.in.GateInput, Dir: waveIn.Dir, GID: p.gid, Key: k}
		awaitFs[i] = workflow.ExecuteActivity(workflow.WithActivityOptions(waveCtx, p.options("await-"+k, stc)), a.AwaitItem, ai)
	}
	backstop := p.t0.Add(stc).Sub(workflow.Now(p.ctx))
	if backstop < time.Second {
		backstop = time.Second
	}
	timerF := workflow.NewTimer(waveCtx, backstop)

	var waveAns StepAnswer
	var waveErr error
	waveDone, fired := false, false
	sel := workflow.NewSelector(p.ctx)
	sel.AddFuture(waveF, func(f workflow.Future) {
		waveErr = f.Get(p.ctx, &waveAns)
		waveDone = true
	})
	for i, k := range p.keys {
		sel.AddFuture(awaitFs[i], func(f workflow.Future) {
			var e ItemEnd
			if f.Get(p.ctx, &e) != nil {
				return
			}
			e.Key = k
			p.prog.Done = append(p.prog.Done, "await-"+k)
			p.itemEnded(e)
		})
	}
	sel.AddFuture(timerF, func(f workflow.Future) { fired = f.Get(p.ctx, nil) == nil })
	// waveF pends until waveDone, so Select always has a case left.
	for !waveDone && !fired {
		sel.Select(p.ctx)
	}
	cancelWave()

	if fired && !waveDone {
		// Not waiting for the wave's cancel: Stop is what stops the step, by the wave's record (TERM). The cancel's
		// answer takes up to the worker's 2 s heartbeat throttle on a live worker, and the 10 s heartbeat timeout on a dead one,
		// whose attempt the server then ends with no retry (server v1.32.0
		// service/history/workflow/mutable_state_impl.go:6773-6775).
		return backstopError{}
	}
	if waveErr != nil {
		return waveErr
	}
	p.live = nil
	p.answered("wave", waveAns)
	if _, err := jump(waveAns, "done"); err != nil {
		return err
	}
	p.fillItems(dataOf(waveAns).Items, "the wave")
	return nil
}

// judgePath is gate/run.sh's finish: combine, post, release. A judge that answers trap true (bin/laug judge wrote no
// verdict) or errs ends as run.sh's EXIT trap does: post with no mark, release with no done event. A cancel that
// arrives on this path changes nothing but Why.
func (p *gateRun) judgePath() {
	p.prog.Phase = "judge"
	j, err := p.final("combine", "", 2*time.Minute)
	p.noteCancel("judge")
	if err != nil {
		p.stopped(fmt.Sprintf("step judge: %s", errType(err)))
		return
	}
	jd := dataOf(j)
	p.res.VerdictWord, p.res.Verdict, p.res.SHA256 = verdictOf(jd), j.Verdict, j.SHA256
	if jd.Trap {
		p.res.End, p.res.Exit = "stopped", j.Exit
		p.addNote("the judge wrote no verdict: the judge step wrote a trap verdict")
		post, err := p.final("post", "stopped", time.Minute)
		p.noteCancel("post-stopped")
		if err != nil || post.Exit != 0 {
			p.res.Exit = 2
			p.addWhy(err, "post")
		}
		p.release(true)
		p.noteCancel("release-stopped")
		return
	}
	post, err := p.final("post", "", time.Minute)
	p.noteCancel("post")
	if err != nil {
		p.res.End, p.res.Exit = "failed", 2
		p.addWhy(err, "post")
		p.release(true)
		p.noteCancel("release-stopped")
		return
	}
	p.res.End, p.res.Exit = "done", j.Exit
	if post.Exit != 0 {
		p.res.Exit = 2
	}
	p.release(false)
	p.noteCancel("release")
}

// noteCancel adds to Why, once, that a cancel arrived while the judge path ran.
func (p *gateRun) noteCancel(id string) {
	if p.ctx.Err() == nil || p.cancelNoted {
		return
	}
	p.cancelNoted = true
	p.addNote(fmt.Sprintf("a cancel arrived during %s; the judge path ran on", id))
	p.details()
}

// stopPath runs after an error before the judge: a step's error or an answer the workflow does not take, the
// barrier's ceiling, the backstop, or a cancel. It stops the live step by its record, then runs stopped, post stopped
// and release stopped. After the backstop, a wave whose .done Stop finds with exit 0 answered in that instant: the
// judge path runs on it instead, unless a cancel arrived while Stop ran. A terminate, a lost db or a stuck workflow
// task runs none of this: laug gate stop does it by hand (release.go).
func (p *gateRun) stopPath(cause error) {
	var a *GateActivities
	p.prog.Phase = "stop"
	cancelled := p.ctx.Err() != nil
	var te *temporal.TimeoutError
	if errors.As(cause, &te) && te.TimeoutType() == enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START {
		p.noWorker = true
	}
	var ce ceilingError
	var be backstopError
	backstop := !cancelled && errors.As(cause, &be)
	why := "the workflow was cancelled"
	switch {
	case cancelled:
	case errors.As(cause, &ce):
		why = ce.Error()
	case backstop:
		why = be.Error()
	default:
		// An unexpected result or answer comes from a step that answered, so nothing is live; it names its step.
		name := "gate"
		var re resultError
		var ae answerError
		if errors.As(cause, &re) {
			name = re.name
		} else if errors.As(cause, &ae) {
			name = ae.name
		} else if p.live != nil {
			name = p.live.Name
		}
		why = fmt.Sprintf("step %s: %s", name, errType(cause))
	}
	why = oneLine(why, 300)
	if p.live != nil {
		id := "stop-" + p.live.Name
		p.mark(id, "stop", p.live.Name)
		var sr StopResult
		err := workflow.ExecuteActivity(workflow.WithActivityOptions(p.dctx, p.options(id, secs(p.killGrace+70))), a.Stop, *p.live).Get(p.dctx, &sr)
		if err != nil {
			sr = StopResult{Name: p.live.Name, Why: "stop: " + errText(err)}
		} else {
			p.prog.Done = append(p.prog.Done, id)
		}
		p.res.Stop = &sr
		if backstop && err == nil && sr.Done && sr.Exit != nil && *sr.Exit == 0 {
			p.live = nil
			// The wave's answer is its .done's, which Stop read: it joins the steps, and gives the items no await
			// answered, as on the wave's own answer.
			if ans, err := answerFrom(sr.Name, sr.Answer); err == nil {
				p.answered("wave", ans)
				p.fillItems(dataOf(ans).Items, "the wave")
			}
			// A cancel that arrived while Stop ran came after the wave answered and before the judge was scheduled: the
			// stop path, as Gate takes it for any such cancel.
			if p.ctx.Err() != nil {
				p.addNote("the backstop fired as the wave answered: its .done says exit 0; a cancel arrived during " + id)
				p.stopped("the workflow was cancelled")
				return
			}
			p.addNote("the backstop fired as the wave answered: its .done says exit 0, so the judge ran")
			p.judgePath()
			return
		}
	}
	p.stopped(why)
}

// stopped is the stop path's end: stopped (the trap verdict, by records, unless the judge wrote one), post stopped,
// release stopped.
func (p *gateRun) stopped(why string) {
	p.prog.Phase = "stop"
	p.res.End, p.res.Exit = "stopped", 2
	p.addNote(why)
	if st, err := p.final("stopped", why, secs(p.killGrace+60)); err != nil {
		p.addWhy(err, "stopped")
	} else {
		d := dataOf(st)
		p.res.Exit = st.Exit
		p.res.VerdictWord, p.res.Verdict, p.res.SHA256 = verdictOf(d), st.Verdict, st.SHA256
		p.fillItems(d.Items, "stopped")
	}
	if post, err := p.final("post", "stopped", time.Minute); err != nil || post.Exit != 0 {
		p.res.Exit = 2
		p.addWhy(err, "post")
	}
	p.release(true)
}

func (p *gateRun) release(stopped bool) {
	key := ""
	if stopped {
		key = "stopped"
	}
	// A release that errs changes nothing but Why: the gate's exit comes from the verdict and post, and its cleanup,
	// which records what it left, changes no exit (gate/run.sh, finish).
	if _, err := p.final("release", key, 7*time.Minute); err != nil {
		p.addWhy(err, "release")
	}
}

func (p *gateRun) addNote(s string) {
	if p.res.Why != "" {
		s = p.res.Why + "; " + s
	}
	p.res.Why = s
}

// addWhy adds a step's error to Why as "step <name>: <text>". The runner's own errors begin so already (steps.go,
// stepErr), and keep their one prefix.
func (p *gateRun) addWhy(err error, name string) {
	if err == nil {
		return
	}
	text, prefix := errText(err), "step "+name+": "
	if !strings.HasPrefix(text, prefix) {
		text = prefix + text
	}
	p.addNote(text)
}

func verdictOf(d answerData) string {
	if d.Verdict == nil {
		return ""
	}
	return *d.Verdict
}

// oneLine makes s one line of at most n bytes, as stopped takes its why (steps.go, stepKey), cut at a rune.
func oneLine(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == 0 }), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// errType names an error for stopped's why: the application error's type (StepLost, StepFailed), the timeout, the
// cancel, or what an answer lacked.
func errType(err error) string {
	var ae *temporal.ApplicationError
	var te *temporal.TimeoutError
	var ce *temporal.CanceledError
	var re resultError
	var we answerError
	switch {
	case errors.As(err, &re):
		return "result " + re.result
	case errors.As(err, &we):
		return we.why
	case errors.As(err, &ae):
		if ae.Type() != "" {
			return ae.Type()
		}
		return "ApplicationError"
	case errors.As(err, &te):
		return "timeout " + te.TimeoutType().String()
	case errors.As(err, &ce):
		return "canceled"
	}
	return "error"
}

func errText(err error) string {
	var ae *temporal.ApplicationError
	if errors.As(err, &ae) {
		return ae.Message()
	}
	return err.Error()
}
