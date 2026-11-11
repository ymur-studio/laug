// The renderer, laug's figures, summary tables and docs/EVIDENCE.md, as its own module: the go tool leaves a
// directory whose name starts with _ out of ./... at the root, so go build, vet and test there never touch it, and
// make docs, make test-go and make lint run it with go -C _report. Its path sits under the main module's, so Go's
// internal rule lets it import internal/jsonx, internal/judge, internal/rejudge and internal/tables, which the replace
// directive takes from the checkout beside it: the same code, so docs/tables' number text is the same bytes. Nothing
// else is required, and no go.sum is needed (go mod tidy writes none).
module github.com/ymur-studio/laug/_report

go 1.26.0

require github.com/ymur-studio/laug v0.0.0

replace github.com/ymur-studio/laug => ../
