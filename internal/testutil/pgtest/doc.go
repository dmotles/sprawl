// Package pgtest provisions throwaway Postgres schemas for the build-tagged
// integration suites.
//
// The harness itself is behind `//go:build store_pg`, so an ordinary
// `go build ./...` or `make validate` never compiles testcontainers. This file
// carries no build tag purely so the package is not empty in an untagged build,
// which Go reports as "build constraints exclude all Go files" rather than as
// the no-op it actually is.
//
// It deliberately does not import internal/store. The store's own in-package
// test files use this harness, and a pgtest that imported store would make that
// an import cycle — which is why NewSchema takes the migrator as a parameter
// instead of calling store.Migrate itself.
package pgtest
