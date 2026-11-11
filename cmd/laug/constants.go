package main

// Defaults internal/script's knob table keeps, each comment naming its knob, and two go.mod go lines.
// TestConstantsMatchKnobs (version_test.go) reads every one but omesGo and fails when one differs.
const (
	// serverTagDefault mirrors the Makefile's SERVER_TAG_DEFAULT, which its fc-* targets read, and the SERVER_TAG
	// knob: the temporalio/server tag a compose cell runs when none is named.
	serverTagDefault = "1.31.0"
	// omesRef mirrors the OMES_REF knob: the Omes commit behind every committed result.
	omesRef = "f0de20635c8204b0c9b8277ce00493204a3c335a"
	// omesURL mirrors the OMES_URL knob: where laug upstream clones Omes from.
	omesURL = "https://github.com/temporalio/omes.git"
	// omesGo is the go line of Omes's go.mod at omesRef, which deps checks before laug upstream has cloned Omes.
	omesGo = "1.26.5"
	// goModGo is the go line of this module's go.mod: the Go that builds bin/laug.
	goModGo = "1.26.0"

	// The daily path's defaults. cellAddressDefault and promDefault are port block 0's: the CELL_ADDRESS and
	// PROM_URL knobs, from FRONTEND_PORT and PROMETHEUS_PORT.
	cellAddressDefault = "127.0.0.1:7233"
	promDefault        = "http://127.0.0.1:9090"
	projectDefault     = "laug"                               // PROJECT
	namespaceDefault   = "default"                            // NAMESPACE
	scenarioDefault    = "workflow_with_single_noop_activity" // SCENARIO
	omesDir            = ".upstream/omes"                     // OMES_DIR: where laug upstream clones Omes
)

// The gate's defaults, and host clock's.
const (
	planDefault         = "gate/plan.yaml" // PLAN
	baseDefault         = "main"           // BASE
	gatePostDefault     = "local"          // GATE_POST
	gateResultsDefault  = "gate/results"   // GATE_RESULTS
	clockSecondsDefault = 60               // SECONDS
)

// The HealthCheck workflow's and the orchestrator's defaults. TestConstantsMatchKnobs and TestOrchestratorMatchesKnobs
// hold those the knob table keeps; healthEveryDefault and orchRunDir have no knob.
const (
	healthEveryDefault     = "10s"              // health loop's -every
	timerDefault           = "60s"              // TIMER
	orchAddressDefault     = "127.0.0.1:7400"   // ORCH_ADDRESS
	orchHTTPPortDefault    = 7401               // ORCH_HTTP_PORT
	orchMetricsPortDefault = 7402               // ORCH_METRICS_PORT
	orchUIPortDefault      = 8400               // ORCH_UI_PORT
	orchRunDir             = "orchestrator/run" // the dev server's state, gitignored
	orchNamespace          = "default"          // ORCH_NAMESPACE, passed as the TEMPORAL_NAMESPACE knob; no flag sets it
	probeIdleDefault       = 60                 // SECONDS: orchestrator probe's -idle, in seconds
)
