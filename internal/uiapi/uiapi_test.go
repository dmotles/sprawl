package uiapi

import (
	"reflect"
	"sort"
	"testing"
)

// TestPool_ExposesNoWritingMethod pins the read-only seam as a structural
// property rather than a convention.
//
// Pool is the only route this package has to Postgres, so if it never names
// Exec, Begin, CopyFrom or SendBatch then no handler can write through it and
// no future handler can start to without editing this interface — a diff that
// is visible in review and that trips this test. Stated as an EXACT set, not as
// a denylist: a denylist of four verbs is silent about the fifth.
func TestPool_ExposesNoWritingMethod(t *testing.T) {
	typ := reflect.TypeOf((*Pool)(nil)).Elem()

	got := make([]string, 0, typ.NumMethod())
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)

	want := []string{"Ping", "Query", "QueryRow"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uiapi.Pool exposes %v, want exactly %v — the read API's only route to Postgres must name no writing verb, so that adding one is a reviewable edit to this interface rather than a line in a handler", got, want)
	}
}
