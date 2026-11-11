// The stack's verbs, bin/obs, as their own module. _obs receives what laug emits. laug sends traces with
// -otlp and serves metrics on Prometheus endpoints; this is one place on the host to receive, keep and read them. The
// gate never needs it, and no Go file of the main module or _report imports a path under _obs. Three read a file here:
// _report reads the A/A lists under _obs/aa and, for the staleness figure, _obs/prometheus.yml and
// _obs/follow/fakecell_test.go (lessonworkflow.go), internal/orchestrator's deny-list test sources _obs/obs.sh for
// deny_check, and check's tests read the tree's pages and paths, _obs's among them. The go tool leaves a directory
// whose name starts with _ out of ./... at the root, so go build, vet and test there never touch it, and make build,
// make test-go and make lint run it with go -C _obs. It holds follow and cellstat, and stdlib_test.go holds them and
// bin/obs to the standard library and laug's own code. Its path sits under the main module's, so Go's internal rule
// lets it import internal/script and internal/gateopen, which the replace directive takes from the checkout beside
// it: the same code, nothing copied. Nothing else is required, and no go.sum is needed (go mod tidy writes none).
module github.com/ymur-studio/laug/_obs

go 1.26.0

require github.com/ymur-studio/laug v0.0.0

replace github.com/ymur-studio/laug => ../
