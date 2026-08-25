// Package pgtest provisions throwaway Postgres schemas for the build-tagged
// integration suites.
//
// The container plumbing (pgtest.go) is behind `//go:build store_pg`, so an
// ordinary `go build ./...` or `make validate` never compiles testcontainers.
// The pieces that need no container — the SPRAWL_STORE_PG_REQUIRED decision and
// NextID — live untagged in require.go, so `make validate` covers them via
// require_test.go. A test file beside pgtest.go would compile only under a tag
// validate never sets, which is a test nothing runs.
//
// This file carries no build tag and no code purely so the package is not empty
// in an untagged build; Go reports that as "build constraints exclude all Go
// files" rather than as the no-op it actually is.
//
// It deliberately does not import internal/store. The store's own in-package
// test files use this harness, and a pgtest that imported store would make that
// an import cycle — which is why NewSchema takes the migrator as a parameter
// instead of calling store.Migrate itself.
package pgtest
