package config

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// loadErr loads a config from an inline body and requires an error.
func loadErr(t *testing.T, body string) error {
	_, err := loadErrIn(t, body)
	return err
}

// loadErrIn is loadErr plus the temp root, needed to normalize the tempdir path
// out of the message before comparing two renderings.
func loadErrIn(t *testing.T, body string) (string, error) {
	t.Helper()
	root := writeConfig(t, body)
	_, err := Load(root)
	if err == nil {
		t.Fatalf("Load must reject this config, got nil error. body:\n%s", body)
	}
	return root, err
}

// renderNormalized returns the error text with the (per-test, random) temp root
// replaced by a fixed token, so two renderings from different temp dirs are
// comparable. Nothing else is touched.
func renderNormalized(err error, root string) string {
	return strings.ReplaceAll(err.Error(), root, "<ROOT>")
}

// fieldKindFor returns the reflect.Kind of the Config field carrying the given
// yaml tag. Fails the test for an unknown key.
func fieldKindFor(t *testing.T, key string) reflect.Kind {
	t.Helper()
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		if strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0] == key {
			return rt.Field(i).Type.Kind()
		}
	}
	t.Fatalf("no Config field carries the yaml tag %q", key)
	return reflect.Invalid
}

// nonZeroValueFor returns a plausible non-zero value for a key, derived from the
// field's kind rather than from a hand-maintained list, so a newly added field
// is covered automatically.
func nonZeroValueFor(t *testing.T, key string) string {
	t.Helper()
	if fieldKindFor(t, key) == reflect.Int {
		return "7"
	}
	return "val-" + key
}

// stringKeys returns every known key backed by a string field, by reflection.
func stringKeys(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, k := range KnownKeys() {
		if fieldKindFor(t, k) == reflect.String {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		t.Fatal("reflection found no string-backed config keys; the test is not measuring anything")
	}
	return out
}

// assertCarriesFullReference is the shared check that an error is
// self-documenting: a user hitting it may have no AI assistance available to
// work out what the valid keys are.
func assertCarriesFullReference(t *testing.T, what string, msg string) {
	t.Helper()
	for _, k := range KnownKeys() {
		if !strings.Contains(msg, k) {
			t.Errorf("%s must carry the full reference (missing key %q); got:\n%s", what, k, msg)
		}
	}
}

// TestLoad_UnknownKeys_ReportsEveryKeyWithLineNumbers is the core
// self-documenting-error requirement: ALL unrecognized keys in one error, each
// with its line number.
func TestLoad_UnknownKeys_ReportsEveryKeyWithLineNumbers(t *testing.T) {
	body := "validate: make test\n" + // line 1 - known
		"vlaidate: oops\n" + // line 2 - typo
		"hub_url: http://x\n" + // line 3 - known
		"totally_unknown: 3\n" // line 4 - unknown

	err := loadErr(t, body)
	var uke *UnknownKeysError
	if !errors.As(err, &uke) {
		t.Fatalf("want *UnknownKeysError, got %T: %v", err, err)
	}
	// Both bad keys, with their real line numbers, and neither known key.
	want := map[string]int{"vlaidate": 2, "totally_unknown": 4}
	got := map[string]int{}
	for _, p := range uke.Problems {
		got[p.Key] = p.Line
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Problems = %v, want %v (every unrecognized key, no known ones)", got, want)
	}

	msg := err.Error()
	for _, s := range []string{"vlaidate", "totally_unknown", "line 2", "line 4"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error text must contain %q; got:\n%s", s, msg)
		}
	}
	if !strings.Contains(msg, `"validate"`) {
		t.Errorf("error must suggest \"validate\" for the typo \"vlaidate\"; got:\n%s", msg)
	}
	assertCarriesFullReference(t, "the unknown-key error", msg)
}

// TestLoad_UnknownKeys_TypedError pins that callers can detect this class
// programmatically rather than by string matching.
func TestLoad_UnknownKeys_TypedError(t *testing.T) {
	err := loadErr(t, "bogus_key: 1\n")
	var uke *UnknownKeysError
	if !errors.As(err, &uke) {
		t.Fatalf("Load error must be a *UnknownKeysError, got %T: %v", err, err)
	}
	if len(uke.Problems) != 1 {
		t.Fatalf("Problems = %d, want 1: %+v", len(uke.Problems), uke.Problems)
	}
	if uke.Problems[0].Key != "bogus_key" || uke.Problems[0].Line != 1 {
		t.Errorf("Problems[0] = {Key:%q Line:%d}, want {bogus_key 1}", uke.Problems[0].Key, uke.Problems[0].Line)
	}
}

// TestLoad_UnknownShapesAreIdentical is the QUM-1086 AC that a nested block, a
// sequence value, and an unknown flat scalar all hard-error IDENTICALLY. They
// are the same node-level event (an unrecognized top-level KEY), so the
// implementation must not special-case the value shape.
//
// It compares the renderings TO EACH OTHER, not to a golden string, so it
// cannot churn on wording changes -- it can only fail when someone
// special-cases a value shape.
func TestLoad_UnknownShapesAreIdentical(t *testing.T) {
	shapes := []struct{ name, body string }{
		{"nested block", "validate: x\nsome_key:\n  a: 1\n  b: 2\n"},
		{"block sequence", "validate: x\nsome_key:\n  - a\n  - b\n"},
		{"flow sequence", "validate: x\nsome_key: [a, b]\n"},
		{"flat scalar", "validate: x\nsome_key: hello\n"},
		{"deeply nested", "validate: x\nsome_key:\n  a:\n    b:\n      c: 1\n"},
		{"null value", "validate: x\nsome_key:\n"},
		{"empty map", "validate: x\nsome_key: {}\n"},
		// A nested block whose BODY contains another unrecognized name: the
		// top-level key must be reported once and the walk must not recurse.
		{"nested with inner unknown", "validate: x\nsome_key:\n  also_bogus: 1\n"},
	}

	rendered := make(map[string]string, len(shapes))
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			root, err := loadErrIn(t, s.body)
			var uke *UnknownKeysError
			if !errors.As(err, &uke) {
				t.Fatalf("want *UnknownKeysError, got %T: %v", err, err)
			}
			if len(uke.Problems) != 1 {
				t.Fatalf("Problems = %+v, want exactly 1 (the walk must not recurse into a nested value)", uke.Problems)
			}
			if uke.Problems[0].Key != "some_key" {
				t.Errorf("Key = %q, want %q", uke.Problems[0].Key, "some_key")
			}
			// The KEY is always on line 2 regardless of where the value sits.
			if uke.Problems[0].Line != 2 {
				t.Errorf("Line = %d, want 2 (the key's line, not the value's)", uke.Problems[0].Line)
			}
			rendered[s.name] = renderNormalized(err, root)
		})
	}

	base := shapes[0].name
	for _, s := range shapes[1:] {
		if rendered[s.name] != "" && rendered[base] != "" && rendered[s.name] != rendered[base] {
			t.Errorf("value shape must not change the error text.\n%s:\n%s\n%s:\n%s",
				base, rendered[base], s.name, rendered[s.name])
		}
	}
}

// TestLoad_MultipleUnknownShapes_StillIdentical extends the identity property
// to the multi-problem case: two unknown keys of different value shapes must
// render the same as two unknown keys of the same shape.
func TestLoad_MultipleUnknownShapes_StillIdentical(t *testing.T) {
	// Both bodies must put the two bad keys on the SAME lines (1 and 3) — the
	// nested value in the mixed variant occupies line 2, so the flat variant
	// needs a filler there. Differing line numbers would be correct behaviour,
	// not the drift this test is looking for.
	mixedRoot, mixedErr := loadErrIn(t, "bogus_a:\n  x: 1\nbogus_b: [1, 2]\n")
	flatRoot, flatErr := loadErrIn(t, "bogus_a: 1\nvalidate: x\nbogus_b: 2\n")
	mixed, flat := renderNormalized(mixedErr, mixedRoot), renderNormalized(flatErr, flatRoot)
	if mixed != flat {
		t.Errorf("value shapes must not change a multi-key error.\nmixed:\n%s\nflat:\n%s", mixed, flat)
	}
}

// TestLoad_RetiredKey_LivenessMigrationMessage covers the migration case from
// the QUM-1086 comment: `liveness` was removed by QUM-1071 and has no
// near-match in the recognized set, so did-you-mean produces nothing useful.
// The remedy must be stated explicitly.
func TestLoad_RetiredKey_LivenessMigrationMessage(t *testing.T) {
	err := loadErr(t, "validate: x\nliveness:\n  interval: 15m\n")
	msg := err.Error()
	for _, want := range []string{"liveness", "QUM-1071", "delete this block"} {
		if !strings.Contains(msg, want) {
			t.Errorf("retired-key error must contain %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "did you mean") {
		t.Errorf("a retired key must not get a did-you-mean suggestion; got:\n%s", msg)
	}
}

// TestLoad_RetiredKey_NearMissStillGetsSuggestion is the negative control for
// the test above: a key that merely LOOKS like the retired one takes the
// ordinary did-you-mean path.
func TestLoad_RetiredKey_NearMissStillGetsSuggestion(t *testing.T) {
	err := loadErr(t, "vlaidate: x\n")
	msg := err.Error()
	if !strings.Contains(msg, "did you mean") {
		t.Errorf("a non-retired typo must get a did-you-mean; got:\n%s", msg)
	}
	if strings.Contains(msg, "QUM-1071") {
		t.Errorf("a non-retired key must not get the retired-key message; got:\n%s", msg)
	}
}

// TestRetiredKeys_DisjointFromRecognized: a key cannot be both recognized and
// retired. Derived from the exported surfaces, not from a package var, so it
// pins behaviour rather than an implementation detail.
func TestRetiredKeys_DisjointFromRecognized(t *testing.T) {
	retired := RetiredKeys()
	if len(retired) == 0 {
		t.Fatal("RetiredKeys() must contain at least `liveness` (QUM-1071)")
	}
	if _, ok := retired["liveness"]; !ok {
		t.Errorf("RetiredKeys() must contain `liveness`; got %v", retired)
	}
	known := map[string]bool{}
	for _, k := range KnownKeys() {
		known[k] = true
	}
	for k, remedy := range retired {
		if known[k] {
			t.Errorf("%q is both a recognized key and a retired key", k)
		}
		if remedy == "" {
			t.Errorf("retired key %q has no remedy text; the remedy is the whole point", k)
		}
		// A retired key must be rejected by Load, and its remedy surfaced.
		err := loadErr(t, k+": x\n")
		if !strings.Contains(err.Error(), remedy) {
			t.Errorf("Load error for retired key %q must carry its remedy %q; got:\n%s", k, remedy, err)
		}
	}
}

// TestLoad_DottedPrefixHint: `worktree:` as a nested block is the real-world
// mistake QUM-1078 was filed over. A bare edit-distance suggestion is useless
// there; the message must say the keys are FLAT and dotted.
func TestLoad_DottedPrefixHint(t *testing.T) {
	err := loadErr(t, "worktree:\n  setup: echo hi\n")
	msg := err.Error()
	for _, want := range []string{"worktree.setup", "worktree.teardown", "flat"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error for a `worktree:` block must contain %q; got:\n%s", want, msg)
		}
	}
}

// TestLoad_EmptyInputs_ZeroValueNoError pins the Load contract for every shape
// of "nothing configured". This is also the panic guard: a yaml.Node walk that
// indexes Content[0] without checking Kind panics on an empty document.
//
// Note this deliberately requires a ZERO-value struct: defaults live in the
// accessors (PauseTimeout, ValidatePopupAfter), never prefilled into fields.
// Prefilling would let Save freeze today's default into a user's file.
func TestLoad_EmptyInputs_ZeroValueNoError(t *testing.T) {
	cases := map[string]string{
		"empty string":  "",
		"comment only":  "# nothing here\n",
		"bare document": "---\n",
		"blank lines":   "\n\n\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, body))
			if err != nil {
				t.Fatalf("Load returned an error, want nil: %v", err)
			}
			if cfg == nil {
				t.Fatal("Load returned a nil config")
			}
			if !reflect.DeepEqual(*cfg, Config{sprawlRoot: cfg.sprawlRoot}) {
				t.Errorf("Load must return a zero-value config, got %+v", *cfg)
			}
		})
	}

	t.Run("absent file", func(t *testing.T) {
		cfg, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load returned an error, want nil: %v", err)
		}
		if !reflect.DeepEqual(*cfg, Config{sprawlRoot: cfg.sprawlRoot}) {
			t.Errorf("want zero-value config, got %+v", *cfg)
		}
		// The defaults must still be reachable through the accessors.
		if cfg.PauseTimeout() != DefaultPauseTimeoutSeconds*1e9 {
			t.Errorf("PauseTimeout() = %v, want the %ds default", cfg.PauseTimeout(), DefaultPauseTimeoutSeconds)
		}
	})
}

// TestLoad_TopLevelNotAMapping: yaml.Node unmarshal does NOT error on a
// top-level sequence or scalar (verified against yaml.v3 v3.0.1), so Load must
// reject it itself rather than walking a non-mapping.
func TestLoad_TopLevelNotAMapping(t *testing.T) {
	for name, body := range map[string]string{
		"sequence": "- a\n- b\n",
		"scalar":   "hello\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := loadErr(t, body)
			if !strings.Contains(err.Error(), "mapping") {
				t.Errorf("error should explain a mapping is required; got: %v", err)
			}
		})
	}
}

// TestLoad_DuplicateKey: yaml.v3 reports duplicate mapping keys as an unmarshal
// error. Load must surface it rather than only walking the node (which is
// populated with both pairs). Asserted on the facts Load owns -- the key name
// and the line -- not on yaml.v3's phrasing, which can change across upgrades.
func TestLoad_DuplicateKey(t *testing.T) {
	err := loadErr(t, "validate: a\nvalidate: b\n")
	msg := err.Error()
	// "line 2" and not a bare "2": every unknown-key error embeds the full
	// reference table, so a bare digit is satisfied by table text alone.
	for _, want := range []string{"validate", "line 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("duplicate-key error must name the key and the offending line; missing %q in: %v", want, err)
		}
	}
}

// TestLoad_KnownKeyWrongType: an int field given a string is a decode error,
// not an unknown-key error. Must still be loud, and must carry the line.
func TestLoad_KnownKeyWrongType(t *testing.T) {
	err := loadErr(t, "validate: x\npause_timeout_seconds: sixty\n")
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("wrong-type error must carry the offending line; got: %v", err)
	}
	if !strings.Contains(err.Error(), "pause_timeout_seconds") {
		t.Errorf("wrong-type error must name the key; got: %v", err)
	}
	var uke *UnknownKeysError
	if errors.As(err, &uke) {
		t.Errorf("a recognized key with a bad value must not be reported as unknown: %v", err)
	}
}

// TestLoad_NonStringScalarInStringField: yaml.v3 coerces this, and changing
// that is explicitly out of scope.
func TestLoad_NonStringScalarInStringField(t *testing.T) {
	cfg, err := Load(writeConfig(t, "validate: 60\n"))
	if err != nil {
		t.Fatalf("validate: 60 must still load (yaml.v3 coerces): %v", err)
	}
	if cfg.Validate != "60" {
		t.Errorf("Validate = %q, want %q", cfg.Validate, "60")
	}
}

// TestLoad_TrimsEveryStringField: trimming is applied at the Load boundary to
// EVERY string field, discovered by reflection rather than from a
// hand-maintained list -- a per-key trim list would be exactly the kind of
// hand-synced list QUM-1086 deletes.
func TestLoad_TrimsEveryStringField(t *testing.T) {
	keys := stringKeys(t)
	var body strings.Builder
	for _, k := range keys {
		body.WriteString(k + ": \"  v-" + k + "  \"\n")
	}
	cfg, err := Load(writeConfig(t, body.String()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, k := range keys {
		want := "v-" + k
		if got, _ := cfg.Get(k); got != want {
			t.Errorf("Get(%q) = %q, want %q (Load must trim every string field)", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry / reference: ONE source of truth.
// ---------------------------------------------------------------------------

// TestReference_IsExactlyTheStructsYAMLTags derives the expected key set by
// reflecting over Config INLINE IN THE TEST, so a hand-written key list that
// drifts from the struct fails here.
func TestReference_IsExactlyTheStructsYAMLTags(t *testing.T) {
	rt := reflect.TypeOf(Config{})
	wantKeys := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported: not a config key
			continue
		}
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("exported field %s has no usable yaml tag; every config field needs one", f.Name)
		}
		wantKeys[tag] = true
	}
	if len(wantKeys) == 0 {
		t.Fatal("reflection found no config fields; the test is not measuring anything")
	}

	gotKeys := map[string]bool{}
	for _, k := range KnownKeys() {
		gotKeys[k] = true
	}
	if !reflect.DeepEqual(wantKeys, gotKeys) {
		t.Errorf("KnownKeys() = %v, but Config's yaml tags are %v; the registry and the struct have drifted", gotKeys, wantKeys)
	}
}

// TestReference_EveryKeyHasAWellFormedRow inspects the table's CELLS, not just
// the presence of key names -- which is where drift actually lives, and which a
// reflective implementation cannot satisfy by accident. Exactly one row per
// key; the type cell must match the field's real reflect.Kind; the purpose cell
// must be non-empty.
func TestReference_EveryKeyHasAWellFormedRow(t *testing.T) {
	ref := Reference()
	lines := strings.Split(ref, "\n")
	// NB this requires the key to be the FIRST whitespace-separated field on its
	// row, i.e. a plain aligned-columns table, not a markdown one with a leading
	// "|". That is deliberate: the table is printed to a terminal.

	for _, key := range KnownKeys() {
		var rows []string
		for _, ln := range lines {
			if fields := strings.Fields(ln); len(fields) > 0 && fields[0] == key {
				rows = append(rows, ln)
			}
		}
		if len(rows) != 1 {
			t.Errorf("key %q has %d rows in the reference, want exactly 1:\n%s", key, len(rows), ref)
			continue
		}
		row := rows[0]

		wantType := "string"
		if fieldKindFor(t, key) == reflect.Int {
			wantType = "int"
		}
		if !strings.Contains(row, wantType) {
			t.Errorf("row for %q must show type %q; got: %q", key, wantType, row)
		}
		// key + type + nested + default + at least two words of purpose.
		if n := len(strings.Fields(row)); n < 6 {
			t.Errorf("row for %q has only %d columns/words, so it carries no meaningful purpose text; got: %q", key, n, row)
		}
	}
}

// TestReference_HasAllFiveColumns: key, type, nested, default, purpose.
func TestReference_HasAllFiveColumns(t *testing.T) {
	ref := Reference()
	for _, want := range []string{"key", "type", "nested", "default", "purpose"} {
		if !strings.Contains(ref, want) {
			t.Errorf("Reference() must have a %q column; got:\n%s", want, ref)
		}
	}
	// The documented default must match the constant the accessor actually
	// applies -- a reference stating a default the code does not use is worse
	// than no reference.
	var row string
	for _, ln := range strings.Split(ref, "\n") {
		if fields := strings.Fields(ln); len(fields) > 0 && fields[0] == "pause_timeout_seconds" {
			row = ln
		}
	}
	if row == "" {
		t.Fatalf("no pause_timeout_seconds row in:\n%s", ref)
	}
	if !strings.Contains(row, strconv.Itoa(DefaultPauseTimeoutSeconds)) {
		t.Errorf("pause_timeout_seconds row must state the real default %d; got: %q", DefaultPauseTimeoutSeconds, row)
	}
	if !strings.Contains(ref, "flat") {
		t.Errorf("Reference() must state that all keys are flat scalars; got:\n%s", ref)
	}
}

// TestLoad_AcceptSetEqualsReferenceSet pins parser-acceptance == reference.
// Together with TestReference_IsExactlyTheStructsYAMLTags this proves
// parser == struct fields.
func TestLoad_AcceptSetEqualsReferenceSet(t *testing.T) {
	for _, key := range KnownKeys() {
		if _, err := Load(writeConfig(t, key+": "+nonZeroValueFor(t, key)+"\n")); err != nil {
			t.Errorf("key %q appears in the reference but Load rejects it: %v", key, err)
		}
	}
	if _, err := Load(writeConfig(t, "not_in_the_reference: x\n")); err == nil {
		t.Error("a key absent from the reference must be rejected by Load")
	}
}

// ---------------------------------------------------------------------------
// Get / Set / Keys over the typed struct.
// ---------------------------------------------------------------------------

// TestGet_KnownButUnsetKey pins the `ok` semantics, which are load-bearing for
// Keys(), Save(), and every consumer that uses the `v, ok := cfg.Get(...)`
// form and treats !ok as "not configured".
func TestGet_KnownButUnsetKey(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, k := range KnownKeys() {
		v, ok := cfg.Get(k)
		if ok || v != "" {
			t.Errorf("Get(%q) on a zero-value config = (%q, %v), want (\"\", false): "+
				"`ok` means the key is known AND set", k, v, ok)
		}
	}
}

// TestGet_UnknownKey: an unrecognized key reads as not-set, never as an error
// or a panic -- the consumer form is `v, ok := cfg.Get(...)`.
func TestGet_UnknownKey(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	if v, ok := cfg.Get("no_such_key"); ok || v != "" {
		t.Errorf("Get(no_such_key) = (%q, %v), want (\"\", false)", v, ok)
	}
}

// TestSet_UnknownKey_Errors: Set must refuse a typo instead of silently
// persisting it (current behaviour).
func TestSet_UnknownKey_Errors(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	serr := cfg.Set("vlaidate", "bar")
	if serr == nil {
		t.Fatal("Set on an unknown key must return an error")
	}
	for _, want := range []string{"vlaidate", "did you mean", "validate"} {
		if !strings.Contains(serr.Error(), want) {
			t.Errorf("Set error must contain %q; got:\n%s", want, serr)
		}
	}
	assertCarriesFullReference(t, "the Set error", serr.Error())
	if v, ok := cfg.Get("vlaidate"); ok || v != "" {
		t.Errorf("a rejected Set must not store anything: Get = (%q, %v)", v, ok)
	}
}

// TestSet_KnownKeys_AllSucceed is the negative control for the test above,
// over every key rather than a hand-picked one.
func TestSet_KnownKeys_AllSucceed(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	for _, k := range KnownKeys() {
		v := nonZeroValueFor(t, k)
		if err := cfg.Set(k, v); err != nil {
			t.Errorf("Set(%q, %q) must succeed for a recognized key: %v", k, v, err)
			continue
		}
		if got, ok := cfg.Get(k); !ok || got != v {
			t.Errorf("after Set(%q, %q), Get = (%q, %v)", k, v, got, ok)
		}
	}
}

// TestSet_IntField_ParsesAndValidates covers the string->int seam Set now owns.
func TestSet_IntField_ParsesAndValidates(t *testing.T) {
	cfg, _ := Load(t.TempDir())

	if err := cfg.Set("pause_timeout_seconds", "  60  "); err != nil {
		t.Fatalf("Set with surrounding whitespace must succeed (strconv.Atoi alone would reject it): %v", err)
	}
	if cfg.PauseTimeoutSeconds != 60 {
		t.Errorf("PauseTimeoutSeconds = %d, want 60", cfg.PauseTimeoutSeconds)
	}

	for _, bad := range []string{"sixty", "1.5", "9999999999999999999999", "60s"} {
		err := cfg.Set("pause_timeout_seconds", bad)
		if err == nil {
			t.Errorf("Set(pause_timeout_seconds, %q) must fail", bad)
			continue
		}
		if !strings.Contains(err.Error(), "integer") {
			t.Errorf("bad-int error for %q should say an integer is expected; got: %v", bad, err)
		}
		if cfg.PauseTimeoutSeconds != 60 {
			t.Errorf("a rejected Set must not mutate the field: got %d, want 60", cfg.PauseTimeoutSeconds)
		}
	}

	// A negative value parses (it is a valid integer); the accessor's <= 0
	// guard is what turns it into the default. Not an error, deliberately:
	// changing the meaning of existing keys is out of scope.
	if err := cfg.Set("pause_timeout_seconds", "-5"); err != nil {
		t.Fatalf("a negative integer must parse: %v", err)
	}
	if cfg.PauseTimeout() != DefaultPauseTimeoutSeconds*1e9 {
		t.Errorf("a non-positive value must fall back to the default in the accessor, got %v", cfg.PauseTimeout())
	}

	if err := cfg.Set("pause_timeout_seconds", ""); err != nil {
		t.Fatalf("Set to empty must clear an int field to 0 (\"use the default\"): %v", err)
	}
	if cfg.PauseTimeoutSeconds != 0 {
		t.Errorf("PauseTimeoutSeconds = %d after clearing, want 0", cfg.PauseTimeoutSeconds)
	}
}

// TestSet_TrimsEveryStringField: trimming happens at both write boundaries
// (Load and Set), over every string field.
func TestSet_TrimsEveryStringField(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	for _, k := range stringKeys(t) {
		if err := cfg.Set(k, "  spaced  "); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
		if got, _ := cfg.Get(k); got != "spaced" {
			t.Errorf("Get(%q) = %q, want %q (Set must trim)", k, got, "spaced")
		}
	}
}

// TestKeys_OnlySetKeysSorted: Keys() reports keys that are actually SET, in
// sorted order -- it is the `sprawl config show` surface, not the reference.
func TestKeys_OnlySetKeysSorted(t *testing.T) {
	cfg, _ := Load(t.TempDir())
	if got := cfg.Keys(); len(got) != 0 {
		t.Errorf("a zero-value config must have no set keys, got %v", got)
	}
	for _, kv := range [][2]string{{"validate", "v"}, {"memory_model", "m"}, {"hub_url", "h"}} {
		setKey(t, cfg, kv[0], kv[1])
	}
	got := cfg.Keys()
	want := []string{"hub_url", "memory_model", "validate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Keys() = %v, want %v (sorted, set-only)", got, want)
	}
}

// TestRegistry_OnlySupportedKinds pins the guard that keeps the Config doc's
// promise ("adding a field here is the whole job of adding a key") honest.
//
// Only string and int are handled by Get, Set, Reference and Load's per-key
// decode. A bool or time.Duration field added without touching those would
// panic inside Set's reflect.SetString, make Get return the literal
// "<bool Value>", and make Reference() mislabel the row as `string` — and the
// suite would stay green, because the reference test derives the expected type
// with the same "not int ⇒ string" rule the code uses. registry() therefore
// panics on any other kind, and this test is what surfaces that at CI time
// rather than from a user's CLI invocation.
func TestRegistry_OnlySupportedKinds(t *testing.T) {
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if tag := strings.Split(f.Tag.Get("yaml"), ",")[0]; tag == "" || tag == "-" {
			continue
		}
		switch f.Type.Kind() {
		case reflect.String, reflect.Int:
		default:
			t.Errorf("field %s has kind %s; only string and int are supported. "+
				"Add explicit handling in Get, Set, Reference and Load's per-key decode first.",
				f.Name, f.Type.Kind())
		}
	}
	// And the registry must actually be buildable (it panics on a bad kind).
	if len(KnownKeys()) == 0 {
		t.Fatal("registry built no keys")
	}
}

// TestSet_UnknownKey_BlamesTheArgumentNotTheFile: `sprawl config set validat x`
// is a wrong ARGUMENT, not a broken file. Reporting it as
// ".sprawl/config.yaml: unrecognized key" sends the user to edit a file that is
// perfectly fine.
func TestSet_UnknownKey_BlamesTheArgumentNotTheFile(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	serr := cfg.Set("validat", "x")
	if serr == nil {
		t.Fatal("Set on an unknown key must fail")
	}
	msg := serr.Error()
	if strings.Contains(msg, ".sprawl/config.yaml") {
		t.Errorf("a rejected Set must not blame the config file; got:\n%s", msg)
	}
	if !strings.Contains(msg, "unrecognized config key") || !strings.Contains(msg, `"validat"`) {
		t.Errorf("Set error must name the offending key argument; got:\n%s", msg)
	}
	// Load's error, by contrast, SHOULD name the file — the negative control.
	lerr := loadErr(t, "validat: x\n")
	if !strings.Contains(lerr.Error(), "config.yaml") {
		t.Errorf("Load's error must still name the file; got:\n%s", lerr)
	}
}

// TestLoad_MergeKey_HasAnIntelligibleHint: yaml.v3 EXPANDS `<<: *anchor` on a
// whole-mapping decode, but Load walks keys individually so it sees the literal
// `<<`. Rejecting it is correct (every key is a flat scalar, so there is no
// section to merge) but edit distance says nothing useful about `<<`, so the
// remedy has to be spelled out.
func TestLoad_MergeKey_HasAnIntelligibleHint(t *testing.T) {
	err := loadErr(t, "validate: x\n<<: {hub_url: h}\n")
	msg := err.Error()
	for _, want := range []string{"merge key", "flat"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the `<<` error must explain merge keys are unsupported (missing %q); got:\n%s", want, msg)
		}
	}
}

// TestLoad_AliasValuesStillResolve is the other half of the merge-key finding:
// an anchor/alias used as a VALUE must keep working, because the per-key decode
// resolves aliases. Verified against yaml.v3 v3.0.1.
func TestLoad_AliasValuesStillResolve(t *testing.T) {
	cfg, err := Load(writeConfig(t, "validate: &v make test\nhub_url: *v\n"))
	if err != nil {
		t.Fatalf("an alias VALUE must still load: %v", err)
	}
	if cfg.Validate != "make test" || cfg.HubURL != "make test" {
		t.Errorf("alias not resolved: Validate=%q HubURL=%q", cfg.Validate, cfg.HubURL)
	}
}

// TestReference_DefaultsRenderRegardlessOfTagOrder pins that the `sprawl` tag
// parser is ORDER-INDEPENDENT. `purpose=` consumes the rest of the tag, so a
// naive parser that only looks for `default=` before it silently drops the
// default and swallows it into the purpose text — an unstated, unenforced
// invariant of exactly the kind this issue exists to delete.
func TestReference_DefaultsRenderRegardlessOfTagOrder(t *testing.T) {
	cases := map[string]string{
		"default before purpose": `default=10,purpose=some text, with a comma`,
		"default after purpose":  `purpose=some text, with a comma,default=10`,
	}
	for name, tag := range cases {
		if got := defaultFromTag(tag, "-"); got != "10" {
			t.Errorf("%s: defaultFromTag(%q) = %q, want %q", name, tag, got, "10")
		}
	}
	if got := defaultFromTag("purpose=no default here", "-"); got != "-" {
		t.Errorf("a tag with no default must keep the fallback, got %q", got)
	}

	// And every field that declares a default must render it in the table —
	// otherwise only pause_timeout_seconds' default is ever checked.
	ref := Reference()
	rt := reflect.TypeOf(Config{})
	checked := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		want := defaultFromTag(f.Tag.Get("sprawl"), "")
		if key == "" || want == "" {
			continue
		}
		checked++
		var row string
		for _, ln := range strings.Split(ref, "\n") {
			if fields := strings.Fields(ln); len(fields) > 0 && fields[0] == key {
				row = ln
			}
		}
		if !strings.Contains(row, want) {
			t.Errorf("key %q declares default %q but its reference row omits it: %q", key, want, row)
		}
	}
	if checked == 0 {
		t.Fatal("no field declares a default; this test is not measuring anything")
	}
}
