package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/grpc"
)

// -otlp's tracing, with an in-memory exporter (sdk/trace/tracetest) in place of OTLP: the spans of the advance shape,
// the endpoint and environment checks, an error's message kept out, and the startup replay untraced.

// startRequest starts workflow Gate through a client built as StarterMain builds its own, with tr's interceptors, and
// returns the start request a server would get: its Header is what WorkflowExecutionStarted carries. A gRPC
// interceptor answers every call in this process, so nothing is dialed.
func startRequest(t *testing.T, tr *Tracing, id string) *workflowservice.StartWorkflowExecutionRequest {
	t.Helper()
	var got *workflowservice.StartWorkflowExecutionRequest
	answer := func(_ context.Context, method string, req, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
		switch r := req.(type) {
		case *workflowservice.StartWorkflowExecutionRequest:
			got = r
			reply.(*workflowservice.StartWorkflowExecutionResponse).RunId = "0199-run"
			return nil
		case *workflowservice.GetSystemInfoRequest:
			return nil
		}
		return fmt.Errorf("the test answers no %s", method)
	}
	c, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: "default", Identity: "laug-gate",
		Interceptors: tr.ClientInterceptors(),
		ConnectionOptions: client.ConnectionOptions{
			DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(answer)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: id, TaskQueue: "laug-gate-00000000"},
		Gate, gateInput()); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no StartWorkflowExecution reached the answer")
	}
	return got
}

// The advance shape, with the starter's tracing and the worker's on one in-memory exporter: 1 StartWorkflow:Gate
// (laug-gate), 1 RunWorkflow:Gate, 17 StartActivity and 17 RunActivity (laug-worker), one RunActivity per step id, in
// one trace, each span a child of the one before it in that chain; 0 query spans although the test queries progress;
// no attribute, event, status or resource value holding / or the host's name.
func TestTraceOfTheAdvanceShape(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	starter, err := newTracing("laug-gate", sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatal(err)
	}
	wtr, err := newTracing("laug-worker", sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatal(err)
	}
	req := startRequest(t, starter, "gate-g20261016-120000")
	if req.GetHeader().GetFields()[TraceHeader] == nil {
		t.Fatalf("the start request's header holds no %s: %v", TraceHeader, req.GetHeader())
	}
	f := &gateFakes{interceptors: []interceptor.WorkerInterceptor{wtr.ic}, header: req.GetHeader(), id: req.GetWorkflowId()}
	res, _ := runGate(t, f)
	if res.End != "done" || res.VerdictWord != "ADVANCE" {
		t.Fatalf("result %+v", res)
	}
	spans := exp.GetSpans()

	byName := map[string][]tracetest.SpanStub{}
	for _, s := range spans {
		byName[strings.SplitN(s.Name, ":", 2)[0]] = append(byName[strings.SplitN(s.Name, ":", 2)[0]], s)
	}
	counts := fmt.Sprintf("StartWorkflow %d, RunWorkflow %d, StartActivity %d, RunActivity %d, all %d",
		len(byName["StartWorkflow"]), len(byName["RunWorkflow"]), len(byName["StartActivity"]), len(byName["RunActivity"]), len(spans))
	if counts != "StartWorkflow 1, RunWorkflow 1, StartActivity 17, RunActivity 17, all 36" {
		t.Fatalf("spans: %s", counts)
	}
	t.Logf("spans: %s", counts)
	sw, rw := byName["StartWorkflow"][0], byName["RunWorkflow"][0]
	if sw.Name != "StartWorkflow:Gate" || rw.Name != "RunWorkflow:Gate" {
		t.Fatalf("names %q, %q", sw.Name, rw.Name)
	}

	// The step ids, from each RunActivity's temporalActivityID: the 17 of TestGateRunsEveryStepInOrder.
	want := strings.Fields(head + " lease-steady lease-pl barrier quiet-0 quiet-1 wave-time wave await-steady await-pl judge post release")
	var ids []string
	starts := map[trace.SpanID]bool{}
	for _, s := range byName["StartActivity"] {
		starts[s.SpanContext.SpanID()] = true
		if s.Parent.SpanID() != rw.SpanContext.SpanID() {
			t.Errorf("%s's parent is not RunWorkflow", s.Name)
		}
	}
	for _, s := range byName["RunActivity"] {
		ids = append(ids, attr(s.Attributes, "temporalActivityID"))
		if !starts[s.Parent.SpanID()] {
			t.Errorf("RunActivity %s's parent is no StartActivity span", attr(s.Attributes, "temporalActivityID"))
		}
		if attr(s.Attributes, "temporalWorkflowID") != "gate-g20261016-120000" {
			t.Errorf("RunActivity %s: temporalWorkflowID %q", attr(s.Attributes, "temporalActivityID"), attr(s.Attributes, "temporalWorkflowID"))
		}
	}
	sort.Strings(ids)
	sort.Strings(want)
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("RunActivity ids\n got %s\nwant %s", strings.Join(ids, " "), strings.Join(want, " "))
	}
	if rw.Parent.SpanID() != sw.SpanContext.SpanID() || sw.Parent.IsValid() {
		t.Fatal("RunWorkflow is not StartWorkflow's child, or StartWorkflow has a parent")
	}

	host, _ := os.Hostname()
	for _, s := range spans {
		if s.SpanContext.TraceID() != sw.SpanContext.TraceID() {
			t.Errorf("%s is in another trace", s.Name)
		}
		service := "laug-worker"
		if s.Name == "StartWorkflow:Gate" {
			service = "laug-gate"
		}
		if r := s.Resource.Attributes(); len(r) != 1 || r[0].Key != "service.name" || r[0].Value.AsString() != service {
			t.Errorf("%s's resource %v, want service.name %s alone", s.Name, r, service)
		}
		for _, v := range spanValues(s) {
			if strings.Contains(v, "/") || (host != "" && strings.Contains(v, host)) {
				t.Errorf("%s holds %q", s.Name, v)
			}
		}
		if strings.HasPrefix(s.Name, "HandleQuery") || strings.HasPrefix(s.Name, "QueryWorkflow") {
			t.Errorf("a query span: %s", s.Name)
		}
	}
}

// spanValues is every string a span carries to a store: its name, status description and scope name, each attribute's
// key and value, each event's name and attributes, and its resource's.
func spanValues(s tracetest.SpanStub) []string {
	vals := []string{s.Name, s.Status.Description, s.InstrumentationScope.Name}
	for _, a := range s.Attributes {
		vals = append(vals, string(a.Key), a.Value.Emit())
	}
	for _, e := range s.Events {
		vals = append(vals, e.Name)
		for _, a := range e.Attributes {
			vals = append(vals, string(a.Key), a.Value.Emit())
		}
	}
	for _, a := range s.Resource.Attributes() {
		vals = append(vals, string(a.Key), a.Value.Emit())
	}
	return vals
}

// A step's error reaches its span as a status code and the error's type, never its message, through the interceptor
// newTracing builds: the wave fails with a StepFailed whose message quotes a path, as steps.go's can (stepErr), and the
// gate goes to the stop path and BLOCKs. The one error span is the wave's RunActivity:Step: status Error with no
// description, one exception event holding exception.type alone, and no value of any span holds /. This is the test of
// quietSpan's wiring: without the SpanStarter in newTracing the status and the event hold the message, and it fails
// (on a copy of this module without the SpanStarter, RunActivity:Step holds "step wave: exit 2: /tmp/x/...",
// docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt section 5g).
func TestTraceOfAFailedStepHoldsNoMessage(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	wtr, err := newTracing("laug-worker", sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatal(err)
	}
	why := "step wave: exit 2: /tmp/x/gate/results/g20261016-120000/wave.log"
	f := &gateFakes{errs: map[string]error{"wave": stepErr("StepFailed", why, StepFault{Name: "wave", Exit: 2, Why: why})},
		holdAwaits: true, interceptors: []interceptor.WorkerInterceptor{wtr.ic}}
	res, _ := runGate(t, f)
	if res.End != "stopped" || res.VerdictWord != "BLOCK" {
		t.Fatalf("result %+v", res)
	}
	spans := exp.GetSpans()
	var failed []string
	for _, s := range spans {
		if s.Status.Code == codes.Error {
			failed = append(failed, s.Name+" "+attr(s.Attributes, "temporalActivityID"))
			if s.Status.Description != "" {
				t.Errorf("%s's status description %q; want none", s.Name, s.Status.Description)
			}
			if len(s.Events) != 1 || s.Events[0].Name != "exception" || len(s.Events[0].Attributes) != 1 ||
				s.Events[0].Attributes[0].Key != "exception.type" {
				t.Errorf("%s's events %+v; want one exception event with exception.type alone", s.Name, s.Events)
			}
		}
		for _, v := range spanValues(s) {
			if strings.Contains(v, "/") {
				t.Errorf("%s holds %q", s.Name, v)
			}
		}
	}
	if strings.Join(failed, ", ") != "RunActivity:Step wave" {
		t.Fatalf("error spans %q; want the wave's RunActivity:Step alone", failed)
	}
	t.Logf("spans: %d, error spans: %s, exception.type %s", len(spans), strings.Join(failed, ", "), attrOfEvent(spans, "exception.type"))
}

// attrOfEvent is the first value of key in any span's events, or "".
func attrOfEvent(spans []tracetest.SpanStub, key string) string {
	for _, s := range spans {
		for _, e := range s.Events {
			if v := attr(e.Attributes, key); v != "" {
				return v
			}
		}
	}
	return ""
}

func attr(kvs []attribute.KeyValue, key string) string {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}

// The control for the query count above: the same run with query tracing on gives HandleQuery spans, so the 0 above
// is DisableQueryTracing's, not a query the test env never traces.
func TestTraceQueriesWithoutTheOption(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)))
	ic, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{Tracer: tp.Tracer("temporal-sdk-go")})
	if err != nil {
		t.Fatal(err)
	}
	runGate(t, &gateFakes{interceptors: []interceptor.WorkerInterceptor{ic}})
	n := 0
	for _, s := range exp.GetSpans() {
		if strings.HasPrefix(s.Name, "HandleQuery:") {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no HandleQuery span with query tracing on: the count of 0 above would prove nothing")
	}
}

func TestTracingOffAndRefused(t *testing.T) {
	tr, err := NewTracing("", "laug-worker", nil)
	if tr != nil || err != nil || tr.ClientInterceptors() != nil || tr.Endpoint() != "" || tr.Stop() != nil {
		t.Fatalf("no -otlp: %v, %v; want no tracing, no interceptor", tr, err)
	}
	for _, e := range []string{"0.0.0.0:7751", "localhost:7751", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536",
		"127.0.0.2:7751", "[::1]:7751", "http://127.0.0.1:7751", "127.0.0.1:7751/v1/traces", " 127.0.0.1:7751"} {
		if tr, err := NewTracing(e, "laug-worker", nil); err == nil || tr != nil {
			t.Errorf("-otlp %q taken", e)
		}
	}
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=x")
	if _, err := NewTracing("127.0.0.1:7751", "laug-worker", nil); err == nil || !strings.Contains(err.Error(), "OTEL_RESOURCE_ATTRIBUTES is set") {
		t.Fatalf("with OTEL_RESOURCE_ATTRIBUTES set: %v", err)
	}
	if _, err := newTracing("a/b", sdktrace.NewSimpleSpanProcessor(tracetest.NewInMemoryExporter())); err == nil {
		t.Fatal("a service name with / taken")
	}
}

// With a Collector that takes the connection and never answers, Stop gives up at its 2 s bound and says what it left:
// the starter and the worker stop that much later at most. The listener, on 127.0.0.1 at a port the kernel picks, holds
// the export on any host. A closed port would not: on the dev host a connect to one was refused at once in one run
// and hung in another (cells/results/2026-10-17-obs-offline.txt, section 5).
func TestTracingStopsWithNoCollector(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	defer func() {
		l.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	}()
	var handled atomic.Int32 // the exporter's goroutine may call it after Stop gave up
	tr, err := NewTracing(l.Addr().String(), "laug-worker", func(error) { handled.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	_, s := tr.provider.Tracer("temporal-sdk-go").Start(context.Background(), "RunActivity:Step")
	s.End()
	start := time.Now()
	err = tr.Stop()
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || took < stopWithin || took > stopWithin+500*time.Millisecond {
		t.Fatalf("Stop took %s and returned %v; want context.DeadlineExceeded after %s to %s", took, err, stopWithin,
			stopWithin+500*time.Millisecond)
	}
	mu.Lock()
	n := len(conns)
	mu.Unlock()
	t.Logf("Stop with a Collector that never answers: %s, %v; %d connection(s) taken; the error handler had %d",
		took.Round(10*time.Millisecond), err, n, handled.Load())
}

// An error keeps its type and its status code, never its message.
func TestQuietSpanDropsTheMessage(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tr, err := newTracing("laug-worker", sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatal(err)
	}
	_, s := tr.provider.Tracer("temporal-sdk-go").Start(context.Background(), "RunActivity:Step")
	q := quietSpan{s}
	q.RecordError(errors.New("gate/step.sh wave: exit 2: /tmp/x/gate/results/g20261016-120000/wave.log"))
	q.RecordError(nil)
	q.SetStatus(codes.Error, "gate/step.sh wave: exit 2")
	q.End()
	got := exp.GetSpans()
	if len(got) != 1 {
		t.Fatalf("%d spans", len(got))
	}
	sp := got[0]
	if sp.Status.Code != codes.Error || sp.Status.Description != "" {
		t.Fatalf("status %+v", sp.Status)
	}
	if len(sp.Events) != 1 || sp.Events[0].Name != "exception" || len(sp.Events[0].Attributes) != 1 ||
		sp.Events[0].Attributes[0].Key != "exception.type" || sp.Events[0].Attributes[0].Value.AsString() != "*errors.errorString" {
		t.Fatalf("events %+v", sp.Events)
	}
}

// The startup replay is untraced: its replayer, NewGateReplayer() with no interceptor as ReplayOpenGates builds it,
// replays a Gate history whose first event and every ActivityTaskScheduled carry the trace header, as a traced gate's
// do. The control: the same replay with the tracing interceptor exports one RunWorkflow span, since RunWorkflow has no
// replay check, and no StartActivity or RunActivity, which have one or never run in a replay.
func TestReplayIsUntraced(t *testing.T) {
	p := fixture(t, "advance")
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	headed := withTraceHeader(t, h)
	if headed < 18 {
		t.Fatalf("the trace header went into %d events; want the first and 17 ActivityTaskScheduled", headed)
	}
	r, err := NewGateReplayer()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReplayWorkflowHistory(nil, h); err != nil {
		t.Fatalf("a history with the trace header does not replay untraced: %v", err)
	}

	exp := tracetest.NewInMemoryExporter()
	tr, err := newTracing("laug-worker", sdktrace.NewSimpleSpanProcessor(exp))
	if err != nil {
		t.Fatal(err)
	}
	traced, err := NewGateReplayer(tr.ic)
	if err != nil {
		t.Fatal(err)
	}
	if err := traced.ReplayWorkflowHistory(nil, h); err != nil {
		t.Fatalf("traced replay: %v", err)
	}
	var names []string
	for _, s := range exp.GetSpans() {
		names = append(names, s.Name)
	}
	if strings.Join(names, " ") != "RunWorkflow:Gate" {
		t.Fatalf("a traced replay exported %q; want one RunWorkflow:Gate", names)
	}
}

// withTraceHeader puts a trace header, as the interceptor writes it, into h's first event and every
// ActivityTaskScheduled, and says how many events it changed.
func withTraceHeader(t *testing.T, h *historypb.History) int {
	t.Helper()
	pl, err := converter.GetDefaultDataConverter().ToPayload(map[string]string{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range h.GetEvents() {
		var hd **commonpb.Header
		switch e.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			hd = &e.GetWorkflowExecutionStartedEventAttributes().Header
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			hd = &e.GetActivityTaskScheduledEventAttributes().Header
		default:
			continue
		}
		if *hd == nil {
			*hd = &commonpb.Header{}
		}
		if (*hd).Fields == nil {
			(*hd).Fields = map[string]*commonpb.Payload{}
		}
		(*hd).Fields[TraceHeader] = pl
		n++
	}
	return n
}
