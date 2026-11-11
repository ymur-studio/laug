package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/ymur-studio/laug/internal/rejudge"
)

// The parity matrix: workflow Gate on the real gate/step.sh against gate/run.sh, case by case, on the fake gate (make
// harness-test's gate cases, harness-test.sh's numbered gate/run.sh cases). Each case runs its two legs at once, each
// in a world of its own (realGate: results, FAKE_GATE_DIR, both locks and a laugd port), and 4 cases run at once, the
// ones that sweep too (below). Both legs must end as the table says, judge the same signature (S2, sameSignature), and,
// where the judge wrote the verdict, rejudge to it byte for byte (S1, rejudged); Gate's result must carry
// verdict.json's bytes and digest. S1 and S2 are internal/rejudge's, as laug gate rejudge and gate compare run them, in
// this process, with no git and no jq. S1 runs on every pass with this test's own judge, the working tree's
// internal/judge, so it tests an uncommitted edit to the judge too. laug gate rejudge -all on the commit holds the
// judge to HEAD's.
// hang, stuck and late are wider than harness-test's gp-deadline (5 s) and gp-late (12 s, a 6 s harness):
// one fake gate alone took 2.2 s on step.sh and 1.4 s on run.sh (TestStepsMatchRunSh on the
// dev host), and up to 8 run at once. At deadline_s 5 a wave that starts after about t0 + 4.8 s meets the watchdog with
// steady and pl still running, and S2 differs on stopped_by. Each leg logs t0 to its wave mark, so a shrinking margin
// shows.  The cases that sweep, cancel (gate/step.sh stopped) and stuck (the watchdog's KILL, gate/lib.sh
// sweep_orphans), run beside the others. They once ran alone, and so did cancel-lease: a sweep sent TERM to
// every process whose command line named <gate id>-<key>, and the gate id is g<UTC second>, one per results directory,
// so worlds that start in the same second share it. The sweep TERMed another case's lease request (curl's argument
// names gate-<gate id>-<key>, step.sh's verb_lease) and items. On the dev host, before that rule, items-1's
// Gate leg refused at lease steady once in 2 full runs. With all-pass, items-1, cancel and cancel-lease run at once 6
// times over, all-pass failed S2 6 times and cancel 2. It is safe now because the sweep goes by session (gate/lib.sh,
// sweep_item; verb_stopped's loop in step.sh). It TERMs the item's own session, which launch_item's setsid gives that
// item alone, and the session of a process that names the run id and holds the item's LEASE_FILE in its environment.
// Another world's LEASE_FILE is a path under its own results directory, a t.TempDir() outside the checkout that rel
// (gate/lib.sh) leaves absolute, so no process of another world matches; the sweeping step's own session is skipped. A
// key with no record once the wave began (a wave step stopped between an item's launch and its record, verb_wave) is
// swept by its LEASE_FILE alone, which no other world's process holds either. cancel-lease stops before the wave and
// sweeps nothing. Measured on the dev host with the rule gone: one run of 5 passes, 80 of 80 cases, 17.5 to
// 24.2 s a pass (its longest case), against 40.8 and 46.0 s in one run of 2 with it; all-pass, items-1, cancel and
// cancel-lease at once 6 times, 24 of 24, the 8 worlds of each pass on one gate id.
type parityCase struct {
	name    string
	plan    string   // a plan under the repository in place of load/testdata/gate/plan.yaml, with realGate's times
	edit    string   // a yq edit of the plan
	knobs   []string // FAKE_GATE_* and ITEMS
	cancel  string   // Gate cancelled, run.sh sent TERM: "wave", once all 3 items started; "lease", once a create began
	end     string   // Gate's End
	exit    int      // both legs
	word    string   // both legs' verdict
	class   string   // both legs' verdict.json class, when set
	refused string   // a phrase both legs' refused holds, when set
	tag     string   // SERVER_TAG for both legs, when set; else realGate's 1.31.0
	byMode  bool     // the expectation follows load/steady-state.yaml: ADVANCE while no compare row has a tolerance, else BLOCK class regression
	leases  []string // each item's lease server_tag in plan order, in both legs, when set; R6 holds too
}

// armPlan is gate/plan.yaml's incumbent arm as a test plan: incumbent on the plan's incumbent, leased first, then
// steady and pl on SERVER_TAG.
const armPlan = "load/testdata/gate/plan-arm.yaml"

// deadlineEdit is hang's and stuck's plan edit. Both legs must reach the wave mark before the watchdog at t0 +
// deadline_s, with time for the items the knob leaves alone to end. A leg whose wave meets the watchdog with one of
// them still running differs from the other on stopped_by. A leg at the barrier 1 s or more past t0 + deadline_s is
// refused, too late for the wave (gate/lib.sh, wave_time_why). Gate's leg is the slower to its wave mark. On
// the dev host, this test ran pinned to 2 CPUs (taskset -c 0,1) with 6 busy loops on the same 2. At
// deadline_s 8 both cases failed: Gate's wave mark at 9.0 and 8.0 s, run.sh's at 4.0 and 4.4 s, Gate's 3 items stopped
// by TERM. At 16 both passed: Gate's wave mark at 9.0 and 9.7 s, 7.0 and 6.3 s inside the deadline. Each case waits
// the deadline out, so 16 costs it about 8 s.
const deadlineEdit = ".deadline_s = 16"

// gplanTimes is realGate's yq edit, the short times of make harness-test's gate cases (stepsh_test.go, realGate).
const gplanTimes = `.deadline_s = 30 | .kill_grace_s = 1 | .lease_ttl_s = 120 | .quiet_s = 0 | .poll_s = 1 | .harness_timeout_s = 20
    | .barrier_timeout_s = 2 | .wave_min_s = 0`

var parityCases = []parityCase{
	{name: "all-pass", knobs: []string{"FAKE_GATE_CREATE_S=0.3"}, end: "done", exit: 0, word: "ADVANCE"},
	{name: "dnr", knobs: []string{"FAKE_GATE_DNR=pl"}, end: "done", exit: 1, word: "BLOCK"},
	{name: "exit2", knobs: []string{"FAKE_GATE_EXIT2=kh"}, end: "done", exit: 1, word: "BLOCK"},
	{name: "hang", edit: deadlineEdit, knobs: []string{"FAKE_GATE_HANG=kh"}, end: "done", exit: 1, word: "BLOCK", class: "deadline"},
	{name: "stuck", edit: deadlineEdit, knobs: []string{"FAKE_GATE_STUCK=pl"}, end: "done", exit: 1, word: "BLOCK", class: "deadline"},
	// make harness-test exits 1 at 2 s, while the first lease's 3 s create is under way in both legs: lease steady is
	// taken, lease pl answers stop-leasing. With the harness exiting at once (harness-test.sh's case 6) whether the
	// first lease starts first is a race, which run.sh, one process, wins and step.sh, a process a step, loses.
	{name: "harness-exit", knobs: []string{"FAKE_GATE_HARNESS_EXIT=1", "FAKE_GATE_HARNESS_S=2", "FAKE_GATE_CREATE_S=3"}, end: "done", exit: 1, word: "BLOCK", class: "harness"},
	{name: "harness-timeout", edit: ".harness_timeout_s = 1", knobs: []string{"FAKE_GATE_HARNESS_S=30"}, end: "done", exit: 1, word: "BLOCK", class: "harness"},
	{name: "create-fail", knobs: []string{"FAKE_GATE_CREATE_FAIL=2", "FAKE_GATE_CREATE_S=0.5"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "lease pl: laugd refused it"},
	{name: "backend-tag", knobs: []string{"FAKE_GATE_BACKEND_TAG=1.32.1"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "is on server_tag 1.32.1"},
	{name: "not-quiet", knobs: []string{"FAKE_GATE_PROJECTS=laug-0000aaaa", "FAKE_GATE_EXTRA_FROM=2"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "the host was not quiet 2 s after make harness-test passed"},
	{name: "late", edit: ".deadline_s = 20 | .wave_min_s = 7", knobs: []string{"FAKE_GATE_HARNESS_S=14"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "too late for the wave at the barrier"},
	{name: "items-1", knobs: []string{"ITEMS=1"}, end: "done", exit: 1, word: "BLOCK"},
	{name: "cancel", knobs: []string{"FAKE_GATE_HANG=steady pl kh"}, cancel: "wave", end: "stopped", exit: 1, word: "BLOCK"},
	// A cancel while lease steady's 2 s create is under way: Stop sends the lease TERM, and release waits the busy
	// create out (harness-test.sh's case 12, TERM during a cold create).
	{name: "cancel-lease", knobs: []string{"FAKE_GATE_CREATE_S=2"}, cancel: "lease", end: "stopped", exit: 1, word: "BLOCK"},
	{name: "refused-preflight", knobs: []string{"FAKE_GATE_VCPU=8"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "under the 16 a gate needs"},
	{name: "host-type", knobs: []string{"FAKE_GATE_HOST_TYPE=m7i.8xlarge"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "this host's declared type is not m8i.8xlarge"},
	// The harness still runs at the refusal in both legs: with the fake's 0 s harness, run.sh refuses before harness.json
	// exists and step.sh, a process later, after, and R1 differs.
	{name: "later", edit: ".wave_min_s = 30", knobs: []string{"FAKE_GATE_HARNESS_S=3"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "too late for the wave at lease steady"},
	// The incumbent arm at SERVER_TAG 1.31.0, the plan's incumbent: an A/A gate, every lease on 1.31.0.
	{name: "arm-aa", plan: armPlan, knobs: []string{"FAKE_GATE_CREATE_S=0.3"}, end: "done", exit: 0, word: "ADVANCE"},
	// The first create, incumbent's, says 1.32.1: refused at lease incumbent, named against the plan's incumbent.
	{name: "arm-wrong-tag", plan: armPlan, knobs: []string{"FAKE_GATE_BACKEND_TAG_AT=1 1.32.1"}, end: "done", exit: 1, word: "BLOCK", class: "refused",
		refused: "is on server_tag 1.32.1, and the plan's incumbent is 1.31.0"},
	// steady's latency readings and shares 1.5 times the incumbent's (load/testdata/gate/fake-item.sh): R7 records it
	// while load/steady-state.yaml has no tolerance, and BLOCKs on it, class regression, once it has.
	{name: "arm-scale", plan: armPlan, knobs: []string{"FAKE_GATE_SCALE=steady", "FAKE_GATE_SCALE_BY=1.5"}, byMode: true},
	// A/B: the incumbent on the plan's 1.31.0, steady and pl on SERVER_TAG 1.32.1, each judged by its own tag's set. It
	// runs step.sh's lease body, its tag check and R6 per item at an A/B tag, which no harness section can: make
	// harness-test's gate/step.sh section runs no laugd.
	{name: "arm-ab", plan: armPlan, tag: "1.32.1", end: "done", exit: 0, word: "ADVANCE", leases: []string{"1.31.0", "1.32.1", "1.32.1"}},
}

// parityWorld is realGate's world with a case's plan, plan edit and knobs.
func parityWorld(t *testing.T, c parityCase) (*GateActivities, GateInput) {
	t.Helper()
	a, in := realGate(t)
	if c.tag != "" {
		in.ServerTag = c.tag
	}
	if c.plan != "" {
		plan, err := exec.Command("yq", gplanTimes, filepath.Join(a.Repo, c.plan)).Output()
		if err != nil {
			t.Fatalf("yq on %s: %v", c.plan, err)
		}
		if err := os.WriteFile(in.Plan, plan, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if c.edit != "" {
		if out, err := exec.Command("yq", "-i", c.edit, in.Plan).CombinedOutput(); err != nil {
			t.Fatalf("yq -i %q: %v: %s", c.edit, err, out)
		}
	}
	for _, kv := range c.knobs {
		k, v, _ := strings.Cut(kv, "=")
		if k == "ITEMS" {
			in.Items = v
		} else {
			in.Extra[k] = v
		}
	}
	return a, in
}

// cancelReady waits, 60 s at most, until a leg is where its case cancels it: for "wave", all 3 items wrote
// started.<key> in its FAKE_GATE_DIR; for "lease", the fake backend counted a create (creates.n).
func cancelReady(fake, at string) bool {
	for until := time.Now().Add(time.Minute); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
		switch at {
		case "wave":
			if m, _ := filepath.Glob(filepath.Join(fake, "started.*")); len(m) >= 3 {
				return true
			}
		case "lease":
			if exists(filepath.Join(fake, "creates.n")) {
				return true
			}
		}
	}
	return false
}

// cancelOn is a cancel case's Gate leg: once the leg is ready (cancelReady), a cancel of the workflow, asked for from
// the step the case cancels in. The env takes it from any goroutine, since it posts to the env's channel (SDK
// v1.49.0 registerDelayedCallback of the test env). The env resolves the cancelled step at once
// (RequestCancelActivity); the old Step goroutine ends at its next heartbeat's NotFound (mockHeartbeatFn) and sends
// nothing, while the stop path's Stop sends TERM.
func cancelOn(fake, at string) stepWrap {
	return func(env *testsuite.TestWorkflowEnvironment, step func(context.Context, StepInput) (StepAnswer, error)) func(context.Context, StepInput) (StepAnswer, error) {
		var once sync.Once
		return func(ctx context.Context, si StepInput) (StepAnswer, error) {
			if si.Verb == at {
				once.Do(func() {
					go func() {
						if cancelReady(fake, at) {
							env.RegisterDelayedCallback(env.CancelWorkflow, 0)
						}
					}()
				})
			}
			return step(ctx, si)
		}
	}
}

// runShLeg runs gate/run.sh in its world, in a session of its own, as stepsh_test.go's TestStepsMatchRunSh does;
// with cancel, TERM to run.sh once cancelReady (harness-test.sh's case 3, TERM to the gate itself).
func runShLeg(a *GateActivities, in GateInput, cancel string) (int, string, []byte, error) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stop()
	cmd := exec.CommandContext(ctx, "env", "--default-signal=INT,QUIT", "gate/run.sh")
	cmd.Dir, cmd.Env = a.Repo, in.Env(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Minute
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return 0, "", nil, err
	}
	if cancel != "" {
		go func() {
			if cancelReady(in.Extra["FAKE_GATE_DIR"], cancel) {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}()
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return 0, "", out.Bytes(), err
	}
	runs, _ := filepath.Glob(filepath.Join(in.Results, "g*"))
	if len(runs) != 1 {
		return 0, "", out.Bytes(), fmt.Errorf("run.sh's results hold %d runs", len(runs))
	}
	return cmd.ProcessState.ExitCode(), runs[0], out.Bytes(), nil
}

// verdictFields reads what the table checks from a run's verdict.json.
type verdictFields struct {
	Verdict   string  `json:"verdict"`
	Class     *string `json:"class"`
	StoppedAt *string `json:"stopped_at"`
	Refused   *string `json:"refused"`
	Rules     map[string]struct {
		Pass bool `json:"pass"`
	} `json:"rules"`
}

// orNull is a verdict field for a message: the string, quoted, or null.
func orNull(p *string) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *p)
}

func readVerdict(dir string) (verdictFields, []byte, error) {
	var v verdictFields
	b, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		return v, nil, err
	}
	return v, b, json.Unmarshal(b, &v)
}

// toWave is t0 to the wave mark, from clock.json and timeline.jsonl, by the wall clock and by the monotonic one
// (/proc/uptime: stamp writes both, gate/lib.sh); -1, -1 when no wave ran. The watchdog and the plan's limits run on
// the wall clock, so a wall step shows as the two parting, not as a lost margin (on the dev host, a wall
// interval came out about 3.5 s longer than the run around it).
func toWave(dir string) (wall, mono float64) {
	type clk struct {
		Wall float64 `json:"wall"`
		Mono float64 `json:"mono"`
	}
	var clock struct {
		T0 clk `json:"t0"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "clock.json"))
	if err != nil || json.Unmarshal(b, &clock) != nil {
		return -1, -1
	}
	tl, _ := os.ReadFile(filepath.Join(dir, "timeline.jsonl"))
	for _, l := range strings.Split(string(tl), "\n") {
		var m struct {
			Step string `json:"step"`
			clk
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.Step == "wave" {
			return m.Wall - clock.T0.Wall, m.Mono - clock.T0.Mono
		}
	}
	return -1, -1
}

// startSpread is max - min of the items' starts over <key>/exit.json, by the monotonic clock and by the wall clock:
// the wave's one fork loop starts every item within 1 s (gate/step.sh, wave). The check reads mono: a wall step inside
// the loop fails it on wall alone. A date shim that stepped wall 3.5 s there gave a wall spread of 3.554 s and a mono
// one of 0.050 s. On the dev host, laug host clock measured wall steps of +3.1 s every 32 s. wall is logged beside
// it, as toWave does. ok is false when no item wrote one.
func startSpread(dir string) (mono, wall float64, ok bool) {
	files, _ := filepath.Glob(filepath.Join(dir, "*", "exit.json"))
	mlo, mhi, wlo, whi := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, f := range files {
		var e struct {
			Start struct {
				Wall float64 `json:"wall"`
				Mono float64 `json:"mono"`
			} `json:"start"`
		}
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &e) == nil {
			mlo, mhi = math.Min(mlo, e.Start.Mono), math.Max(mhi, e.Start.Mono)
			wlo, whi = math.Min(wlo, e.Start.Wall), math.Max(whi, e.Start.Wall)
		}
	}
	if len(files) == 0 {
		return 0, 0, false
	}
	return mhi - mlo, whi - wlo, true
}

// sameSignature is S2 of two runs, as laug gate compare gives it: each run's signature from its verdict.json and
// timeline.jsonl (rejudge.Signature), then each of a's fields against b's (rejudge.Compare). nil when every field is
// equal; else S2's line and one line per field that differs, with a's value and b's. repo is the root a path in a
// signature's error is shown against.
func sameSignature(repo, a, b string) error {
	sa, err := rejudge.Signature(a, repo)
	if err != nil {
		return err
	}
	sb, err := rejudge.Signature(b, repo)
	if err != nil {
		return err
	}
	diffs := rejudge.Compare(sa, sb)
	if len(diffs) == 0 {
		return nil
	}
	o, _ := sa.Obj()
	line := rejudge.CompareLine(filepath.Base(a), filepath.Base(b), diffs, o.Len())
	return errors.New(strings.Join(append([]string{line}, diffs...), "\n"))
}

// rejudged is S1 of the run in dir, as laug gate rejudge -brief gives it (rejudge.Rejudge). The judge version its
// gate.json records runs again in memory on the run as it stands, against its verdict.json and verdict.md. The judge is
// this test's own, the working tree's internal/judge. NotHeld is empty, so no condition holds it to HEAD's; laug gate
// rejudge sets NotHeld when its binary's judge is not HEAD's. repo is the root the line shows a path against.
func rejudged(repo, dir string) rejudge.Answer {
	return rejudge.Rejudge(dir, rejudge.Checkout{Root: repo})
}

// armJudging: load/steady-state.yaml names a tolerance on a compare row, so R7 judges (internal/judge, R7).
func armJudging(t *testing.T, repo string) bool {
	t.Helper()
	cmd := exec.Command("yq", `[.compare.rows[] | select(.tolerance != null)] | length`, "load/steady-state.yaml")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("yq on load/steady-state.yaml: %v", err)
	}
	return strings.TrimSpace(string(out)) != "0"
}

// leaseTags is each item's lease.json server_tag, in plan.json's order; "" for an item with none.
func leaseTags(t *testing.T, dir string) []string {
	t.Helper()
	var plan struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err == nil {
		err = json.Unmarshal(b, &plan)
	}
	if err != nil {
		t.Errorf("plan.json in %s: %v", dir, err)
		return nil
	}
	var tags []string
	for _, it := range plan.Items {
		var l struct {
			ServerTag string `json:"server_tag"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, it.Key, "lease.json")); err == nil {
			_ = json.Unmarshal(b, &l)
		}
		tags = append(tags, l.ServerTag)
	}
	return tags
}

func TestGateMatchesRunSh(t *testing.T) {
	sem := make(chan struct{}, 4) // 4 cases at once, sweeping ones too (parityCase)
	for _, c := range parityCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sem <- struct{}{}
			defer func() { <-sem }()
			ga, gin := parityWorld(t, c)
			sa, sin := parityWorld(t, c)
			if c.byMode {
				if armJudging(t, ga.Repo) {
					c.end, c.exit, c.word, c.class = "done", 1, "BLOCK", "regression"
				} else {
					c.end, c.exit, c.word, c.class = "done", 0, "ADVANCE", ""
				}
			}
			t.Cleanup(func() {
				killResults(gin.Results)
				killResults(sin.Results)
				stopPool(sin.PoolListen)
			})
			var wrap stepWrap
			if c.cancel != "" {
				wrap = cancelOn(gin.Extra["FAKE_GATE_DIR"], c.cancel)
			}
			var wg sync.WaitGroup
			var res GateResult
			var gdir, sdir string
			var shExit int
			var shOut []byte
			var gerr, serr error
			start := time.Now()
			wg.Add(2)
			go func() { defer wg.Done(); res, gdir, gerr = runGateLeg(ga, gin, wrap) }()
			go func() { defer wg.Done(); shExit, sdir, shOut, serr = runShLeg(sa, sin, c.cancel) }()
			wg.Wait()
			if gerr != nil {
				t.Fatalf("Gate leg: %v", gerr)
			}
			if serr != nil {
				t.Fatalf("run.sh leg: %v\n%s", serr, shOut)
			}
			gw, gm := toWave(gdir)
			sw, sm := toWave(sdir)
			t.Logf("both legs in %s; t0 to the wave mark, wall and mono (-1: no wave): Gate %.1f s, %.1f s; run.sh %.1f s, %.1f s",
				time.Since(start).Round(100*time.Millisecond), gw, gm, sw, sm)

			if res.End != c.end || res.Exit != c.exit || res.VerdictWord != c.word {
				t.Errorf("Gate: end %s, exit %d, %s; want %s, %d, %s (why: %s)", res.End, res.Exit, res.VerdictWord, c.end, c.exit, c.word, res.Why)
			}
			if shExit != c.exit {
				t.Errorf("run.sh: exit %d, want %d\n%s", shExit, c.exit, shOut)
			}
			gv, gb, err := readVerdict(gdir)
			if err != nil {
				t.Fatalf("Gate's verdict.json: %v", err)
			}
			sv, _, err := readVerdict(sdir)
			if err != nil {
				t.Fatalf("run.sh's verdict.json: %v", err)
			}
			sum := sha256.Sum256(gb)
			if !bytes.Equal(res.Verdict, gb) || res.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("Gate's result carries %d bytes, sha256 %.12s; verdict.json is %d bytes, sha256 %.12s", len(res.Verdict), res.SHA256, len(gb), hex.EncodeToString(sum[:]))
			}
			for leg, v := range map[string]verdictFields{"Gate": gv, "run.sh": sv} {
				if v.Verdict != c.word {
					t.Errorf("%s: verdict.json says %s, want %s (class %s, refused %s)", leg, v.Verdict, c.word, orNull(v.Class), orNull(v.Refused))
				}
				if c.class != "" && (v.Class == nil || *v.Class != c.class) {
					t.Errorf("%s: class %s, want %s (refused %s)", leg, orNull(v.Class), c.class, orNull(v.Refused))
				}
				if c.refused != "" && (v.Refused == nil || !strings.Contains(*v.Refused, c.refused)) {
					t.Errorf("%s: refused %s, want it to hold %q", leg, orNull(v.Refused), c.refused)
				}
				if at := map[string]string{"wave": "wave", "lease": "lease steady"}[c.cancel]; at != "" && (v.StoppedAt == nil || *v.StoppedAt != at) {
					t.Errorf("%s: stopped_at %s, want %s", leg, orNull(v.StoppedAt), at)
				}
				if c.leases != nil && !v.Rules["R6"].Pass {
					t.Errorf("%s: R6 failed, want it to hold", leg)
				}
			}
			if c.leases != nil {
				for leg, d := range map[string]string{"Gate": gdir, "run.sh": sdir} {
					if got := leaseTags(t, d); fmt.Sprint(got) != fmt.Sprint(c.leases) {
						t.Errorf("%s: the leases' server_tag %v, want %v", leg, got, c.leases)
					}
				}
			}
			if err := sameSignature(ga.Repo, gdir, sdir); err != nil {
				t.Errorf("S2, Gate's run against run.sh's: %v", err)
			}
			for leg, d := range map[string]string{"Gate": gdir, "run.sh": sdir} {
				v := map[string]verdictFields{"Gate": gv, "run.sh": sv}[leg]
				if v.StoppedAt == nil {
					if ans := rejudged(ga.Repo, d); ans.Outcome != rejudge.Identical {
						t.Errorf("%s: S1: %s", leg, ans.Line())
					}
				}
				if mono, wall, ok := startSpread(d); ok {
					t.Logf("%s: the items' starts spread %.3f s mono, %.3f s wall", leg, mono, wall)
					if mono >= 1 {
						t.Errorf("%s: the items started %.3f s apart (mono; wall %.3f s)", leg, mono, wall)
					}
				}
			}
		})
	}
}

// Gate and run.sh on one GATE_LOCK and GATE_HOST_LOCK, each order in turn: the one that starts first holds both locks
// and ADVANCEs, the other is refused at preflight on the lock, BLOCK class refused; both locks are free after. Gate
// first: run.sh starts once step.sh's lock holder wrote "held held" (.steps/holder). run.sh first: Gate starts once
// run.sh's timeline has its plan mark, which comes after its flock (gate/run.sh's step 1).
func TestGateAndRunShExcludeEachOther(t *testing.T) {
	// seen is the first file under results/g*/ that holds want, 60 s at most.
	seen := func(results, name, want string) bool {
		for until := time.Now().Add(time.Minute); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
			fs, _ := filepath.Glob(filepath.Join(results, "g*", name))
			for _, f := range fs {
				if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), want) {
					return true
				}
			}
		}
		return false
	}
	refusedOnLock := func(v verdictFields) bool {
		return v.Verdict == "BLOCK" && v.Class != nil && *v.Class == "refused" && v.Refused != nil && strings.Contains(*v.Refused, "another gate run holds")
	}
	for _, gateFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "gate-first", false: "runsh-first"}[gateFirst], func(t *testing.T) {
			ga, gin := realGate(t)
			sa, sin := realGate(t)
			sin.Lock, sin.HostLock = gin.Lock, gin.HostLock
			gin.Extra["FAKE_GATE_ITEM_S"], sin.Extra["FAKE_GATE_ITEM_S"] = "2", "2"
			t.Cleanup(func() {
				killResults(gin.Results)
				killResults(sin.Results)
				stopPool(sin.PoolListen)
			})
			var wg sync.WaitGroup
			var res GateResult
			var gdir, sdir string
			var shExit int
			var shOut []byte
			var gerr, serr error
			gate := func() { defer wg.Done(); res, gdir, gerr = runGateLeg(ga, gin, nil) }
			runSh := func() { defer wg.Done(); shExit, sdir, shOut, serr = runShLeg(sa, sin, "") }
			wg.Add(2)
			if gateFirst {
				go gate()
				if !seen(gin.Results, ".steps/holder", "held held") {
					t.Error("Gate's lock holder never wrote held held")
				}
				go runSh()
			} else {
				go runSh()
				if !seen(sin.Results, "timeline.jsonl", `"step":"plan"`) {
					t.Error("run.sh's timeline never marked plan")
				}
				go gate()
			}
			wg.Wait()
			if gerr != nil || serr != nil {
				t.Fatalf("Gate leg: %v; run.sh leg: %v\n%s", gerr, serr, shOut)
			}
			gv, _, gerr := readVerdict(gdir)
			sv, _, serr := readVerdict(sdir)
			if gerr != nil || serr != nil {
				t.Fatalf("verdict.json: %v, %v", gerr, serr)
			}
			if gateFirst && !(res.VerdictWord == "ADVANCE" && res.Exit == 0 && refusedOnLock(sv) && shExit == 1) ||
				!gateFirst && !(shExit == 0 && sv.Verdict == "ADVANCE" && refusedOnLock(gv) && res.Exit == 1) {
				t.Fatalf("Gate %s exit %d (%+v), run.sh exit %d (%+v)\n%s", res.VerdictWord, res.Exit, gv, shExit, sv, shOut)
			}
			if !lockFree(gin.Lock) || !lockFree(gin.HostLock) {
				t.Fatal("a lock is still held after both runs")
			}
		})
	}
}
