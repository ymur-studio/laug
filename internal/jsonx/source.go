package jsonx

import "embed"

// Source is this package's .go files, its tests among them as *.go takes them. The judge's identity is the sha256 of
// the ones that are not tests and of internal/judge's own (internal/judge, identity.go), so a gate run records the
// code that read and wrote its JSON.
//
//go:embed *.go
var Source embed.FS
