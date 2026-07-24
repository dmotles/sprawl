// Package hube2e holds the QUM-911 Hub Phase 1 capstone end-to-end test: a
// local hubd process, the real host wire-log tailer, and a Connect subscriber
// (the browser stand-in) exercised together to prove live-tail plus zero-gap /
// zero-dupe reconnect across a subscriber network blip and a hubd restart.
//
// The heavy end-to-end test lives behind the `hub_e2e` build tag so
// `make validate`'s plain `go test ./...` never spawns a hubd child. The pure
// wire-seq contiguity checker and its negative-control test are untagged, so
// the assertion that underpins every zero-gap claim runs on every validate.
package hube2e
