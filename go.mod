module github.com/ymur-studio/laug

go 1.26.0

// One SDK pin, v1.49.0, for internal/health and internal/orchestrator. TestGateKeepsOneCancelChild
// (internal/orchestrator/gate_test.go) reads the SDK's unexported cancelCtx by reflection: on a bump, re-read
// internal/context.go. go list -m all holds one SDK, v1.49.0, and one google.golang.org/protobuf, v1.36.12. The pool's
// packages use none of these modules.
require (
	github.com/uber-go/tally/v4 v4.1.17
	go.temporal.io/api v1.63.5
	go.temporal.io/sdk v1.49.0
	go.temporal.io/sdk/contrib/tally v0.2.0
	google.golang.org/protobuf v1.36.12
)

// Traces (internal/orchestrator/trace.go, -otlp): the SDK's OpenTelemetry interceptor, contrib/opentelemetry v0.8.1,
// the newest the module proxy listed on 2026-10-17 (go list -m -versions). It requires sdk v1.46.0 or later and otel,
// otel/sdk and otel/trace v1.43.0 (its go.mod, read in the module cache). grpc v1.83.2 requires those three at
// v1.44.0, so minimal version selection takes v1.44.0 (go mod graph, go list -m all), and the OTLP exporter, otlptrace
// and otlptracehttp, is pinned to the same v1.44.0. otlptracehttp raised grpc-gateway/v2 from v2.22.0 to v2.29.0 and
// testify from v1.10.0 to v1.11.1, both indirect. grpc is direct for trace_test.go's in-process answer.
require (
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0
	go.opentelemetry.io/otel/sdk v1.44.0
	go.opentelemetry.io/otel/trace v1.44.0
	go.temporal.io/sdk/contrib/opentelemetry v0.8.1
	google.golang.org/grpc v1.83.2
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/nexus-rpc/nexus-proto-annotations v0.1.0 // indirect
	github.com/nexus-rpc/sdk-go v0.7.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_golang v1.11.0 // indirect
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/common v0.26.0 // indirect
	github.com/prometheus/procfs v0.6.0 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/twmb/murmur3 v1.1.8 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
