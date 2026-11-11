package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// The gate workflow's traces (docs/DESIGN.md section 9): -otlp 127.0.0.1:<port> on the worker and the starter, which
// laug passes from its own -otlp. Empty, the default, builds no interceptor and no provider: the untraced code path,
// in a binary that links the OTel modules (3.4 MB more, a worker build of 31 132 098 B before them, 34 537 912 B with
// them), so the noise rule's down runs price this build. The orchestrator's noise runs measured a build without OTel
// (cells/results/2026-10-15-gate6-noise.txt). Set, the client gets the SDK's OpenTelemetry tracing interceptor
// (go.temporal.io/sdk/contrib/opentelemetry v0.8.1), and every worker built from that client gets it too: the SDK hands
// a worker each client interceptor that is also a worker interceptor (SDK v1.49.0 NewServiceClient,
// NewAggregatedWorker; on the dev server the worker made its spans with the interceptor on its client alone,
// docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt section 5c). The
// startup replay builds its replayer with no interceptor and from no client (NewGateReplayer, replay.go), so it is
// never traced: RunWorkflow has no replay check (SDK v1.49.0 tracingWorkflowInboundInterceptor.ExecuteWorkflow), and a
// traced replayer exports one RunWorkflow span per replay (TestReplayIsUntraced, its control).
//
// What a trace holds: the starter's StartWorkflow:Gate; the worker's RunWorkflow:Gate, StartActivity:<type> as each
// step is scheduled and RunActivity:<type> as each attempt runs. The interceptor's tags are the only attributes:
// temporalWorkflowID gate-<gate id>, temporalRunID, and temporalActivityID on RunActivity, the step's own id. laug adds
// none: the worker's -repo is the checkout's absolute path. Queries make no span (DisableQueryTracing), but a query the
// worker answers by a replay, with no copy of the workflow in its cache, starts a RunWorkflow span: the starter's last
// query after the gate closed, and every 2 s query after a worker restart until the next workflow task (on
// the dev server: 2 RunWorkflow spans in a gate, 33 in one whose worker was KILLed mid-wave and started 30 s later,
// docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt section 5). An error keeps its type and loses its
// message (quietSpan). History, not the trace, is the authority for steps and attempts: a KILLed worker exports nothing
// for its open spans.

// TraceHeader is the header the interceptor writes into WorkflowExecutionStarted and each ActivityTaskScheduled: the
// key every SDK uses (contrib v0.8.1 defaultHeaderKey; a traced gate's history holds 20 headers of 154 B for 19
// steps, docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt section 5e).
const TraceHeader = "_tracer-data"

// Tracing is what -otlp builds: the interceptor for client.Options and the provider behind it.
type Tracing struct {
	ic       interceptor.Interceptor
	provider *sdktrace.TracerProvider
	endpoint string
}

// otlpRE is the one endpoint form taken: 127.0.0.1 and a port, as ORCH_ADDRESS. The stack's Collector takes OTLP over
// HTTP on 127.0.0.1:7751 (_obs/collector.yaml; SECURITY.md, the listener table).
var otlpRE = regexp.MustCompile(`^127\.0\.0\.1:[1-9][0-9]{0,4}$`)

// stopWithin is how long Stop waits for the last spans: the exporter's own timeout, and the bound on Shutdown.
const stopWithin = 2 * time.Second

// NewTracing builds the tracing for -otlp endpoint, with service as the resource's service.name (laug-worker or
// laug-gate) and nothing else in it. endpoint "" is no tracing: a nil *Tracing, whose methods do nothing, and nothing
// is checked. With an endpoint it refuses one that is not 127.0.0.1:<port>, and any OTEL_* variable in the environment:
// the OpenTelemetry SDK reads them when the provider and the exporter are built: OTEL_RESOURCE_ATTRIBUTES and
// OTEL_SERVICE_NAME into the resource, since WithResource merges resource.Environment() (sdk v1.44.0
// WithResource), OTEL_BSP_* into the batch processor (NewBatchSpanProcessor) and
// OTEL_EXPORTER_OTLP_* into the exporter (otlptracehttp v1.44.0 otlpconfig.NewHTTPConfig). The worker
// and the starter run in laug's own process, so the variables refused are that process's: the environment laug builds
// for a role holds no OTEL_*, but the OpenTelemetry SDK reads the process's own. onError gets what the exporter could
// not send: with no Collector on endpoint, spans are dropped and onError says so. When the port refuses the connect,
// "connection refused", one line a batch; when the connect hangs, "processor export timeout", one line an export,
// each 2 s request retried until the batch processor's 30 s (both on the dev server,
// docs/tables/records.jsonl#cells/results/2026-10-17-obs-offline.txt sections 5b and 5g).
func NewTracing(endpoint, service string, onError func(error)) (*Tracing, error) {
	if endpoint == "" {
		return nil, nil
	}
	if err := checkOTLP(endpoint); err != nil {
		return nil, err
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "OTEL_") {
			return nil, fmt.Errorf("%s is set; the OpenTelemetry SDK would read it from laug's own environment: unset it", name)
		}
	}
	if onError != nil {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(onError))
	}
	// The endpoint and the path are set here, so nothing in the environment moves them. 2 s a request, not the
	// default 10 s. The exporter retries a batch answered 429, 502, 503 or 504, or lost to a temporary network error,
	// from 5 s on for 1 min at most, within the batch processor's 30 s an export; a refused connection drops the batch
	// at once (otlptracehttp v1.44.0 otlpconfig.DefaultTimeout, retry.DefaultConfig, client.UploadTraces and
	// evaluate; sdk v1.44.0 DefaultExportTimeout, batchSpanProcessor.exportSpans). Both cases ran on the dev server: the
	// refusal, logged as it happened; a hung connect, each request ending at its 2 s and the export at the processor's
	// 30 s, "processor export timeout". No proxy: the endpoint is 127.0.0.1.
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithURLPath("/v1/traces"),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithTimeout(stopWithin),
		otlptracehttp.WithProxy(func(*http.Request) (*url.URL, error) { return nil, nil }),
	)
	if err != nil {
		return nil, fmt.Errorf("the OTLP exporter for %s: %w", endpoint, err)
	}
	// The batch processor's defaults: a queue of 2048 spans, which drops a span when full, and an export every 5 s
	// (sdk v1.44.0 DefaultMaxQueueSize, DefaultScheduleDelay, batchSpanProcessor.enqueueDrop).
	t, err := newTracing(service, sdktrace.NewBatchSpanProcessor(exp))
	if err != nil {
		return nil, err
	}
	t.endpoint = endpoint
	return t, nil
}

// checkOTLP refuses an endpoint that is not 127.0.0.1:<port>, port 1 to 65535: the stack has no auth (SECURITY.md).
func checkOTLP(endpoint string) error {
	port, err := strconv.Atoi(strings.TrimPrefix(endpoint, "127.0.0.1:"))
	if !otlpRE.MatchString(endpoint) || err != nil || port > 65535 {
		return fmt.Errorf("-otlp %q: 127.0.0.1:<port>, the stack's Collector, 127.0.0.1:7751 (laug's -otlp)", endpoint)
	}
	return nil
}

// newTracing is the provider and the interceptor over the span processor sp: the resource is service.name alone,
// never resource.Default() or a detector, which add the host and the process (the OpenTelemetry Go SDK's
// resource docs). Every span is sampled: the default sampler is parent-based always-on (sdk v1.44.0
// NewTracerProvider's doc).
func newTracing(service string, sp sdktrace.SpanProcessor) (*Tracing, error) {
	if service == "" || strings.ContainsAny(service, "/ ") {
		return nil, fmt.Errorf("service %q: one word, laug-worker or laug-gate", service)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
		sdktrace.WithSpanProcessor(sp),
	)
	ic, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{
		// The name contrib takes from the global provider when no tracer is given (contrib v0.8.1
		// NewTracer); the global provider is left as it is, a no-op.
		Tracer:              tp.Tracer("temporal-sdk-go"),
		DisableQueryTracing: true,
		SpanStarter: func(ctx context.Context, t trace.Tracer, name string, opts ...trace.SpanStartOption) trace.Span {
			_, s := t.Start(ctx, name, opts...)
			return quietSpan{s}
		},
	})
	if err != nil {
		_ = tp.Shutdown(context.Background())
		return nil, err
	}
	return &Tracing{ic: ic, provider: tp}, nil
}

// ClientInterceptors is client.Options' Interceptors: the tracing interceptor, or nil with no tracing.
func (t *Tracing) ClientInterceptors() []interceptor.ClientInterceptor {
	if t == nil {
		return nil
	}
	return []interceptor.ClientInterceptor{t.ic}
}

// Endpoint is -otlp's endpoint, or "" with no tracing.
func (t *Tracing) Endpoint() string {
	if t == nil {
		return ""
	}
	return t.endpoint
}

// Stop sends what is queued and ends the provider, waiting stopWithin at most: a worker or a starter stops at most 2 s
// later than it would without -otlp. On the dev server the worker was gone 0.012 s after TERM when the port refused
// the connect and 2.008 s after it when the connect hung. Its error is what was not sent.
func (t *Tracing) Stop() error {
	if t == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopWithin)
	defer cancel()
	if err := t.provider.Shutdown(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("the spans still queued were not sent within %s: %w", stopWithin, err)
		}
		return err
	}
	return nil
}

// quietSpan keeps an error's type and status code and drops its message, from the span's status and its exception
// event: a step's error can quote a path or a line of step.sh's output (steps.go, stepErr), and no attribute of a
// span holds a path. History keeps every message.
type quietSpan struct{ trace.Span }

func (s quietSpan) RecordError(err error, _ ...trace.EventOption) {
	if err == nil {
		return
	}
	s.Span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.type", fmt.Sprintf("%T", err))))
}

func (s quietSpan) SetStatus(c codes.Code, _ string) { s.Span.SetStatus(c, "") }
