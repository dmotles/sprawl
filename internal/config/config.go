package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents .sprawl/config.yaml project-level settings.
//
// This struct is the SINGLE in-memory representation of the file (QUM-1086).
// Before that it carried both these typed fields and a parallel
// `values map[string]string`, hand-synced between the two — which silently
// discarded every key whenever the file contained a nested block (QUM-1078),
// taking the QUM-808 and QUM-837 commit guards with it.
//
// Consequences of being the single representation, all load-bearing:
//   - Every exported field is a config key, and its `yaml` tag IS the key name.
//     The reference table, the accepted-key set, Get/Set/Keys and Save are all
//     derived from these tags by reflection, so adding a field here is the
//     whole job of adding a key — PROVIDED its type is one registry() supports,
//     today string and int only. Any other kind PANICS at registry build; there
//     is no graceful degradation and no compile-time check. What catches it is
//     TestRegistry_OnlySupportedKinds, so if you add a field of a new kind,
//     that test is the gate you have to satisfy, and registry()/Get/Set/Save
//     all need the new kind taught to them first.
//   - Keys are FLAT dotted strings, not nested blocks. `worktree.setup` is a
//     literal YAML key containing a dot. Nesting is now possible (the parser
//     would just need a struct-typed field) but nothing uses it.
//   - `omitempty` on every field: Save writes only keys that are actually set,
//     so a `sprawl config set` cannot litter the file with empty values. Empty
//     and absent are equivalent to every consumer.
//   - The `sprawl` tag carries the reference table's `default` and `purpose`
//     cells. It is documentation only — the runtime defaults live in the
//     accessors below, and Load deliberately does NOT prefill them, so Save can
//     never freeze today's default into a user's file.
type Config struct {
	// QUM-1087 moved WHEN this runs: the merge engine now rebases the agent's
	// branch, runs this on the REBASED tree in the agent's own worktree, and
	// only then fast-forwards the parent. It is therefore no longer "post-merge"
	// — it is the gate the merge passes through, and the parent is never touched
	// on a failure. The wording lives here rather than in cmd/config.go because
	// QUM-1086 made this tag the single source for `sprawl config --help`, the
	// unrecognized-key error, and `Reference()`.
	Validate                  string `yaml:"validate,omitempty" sprawl:"purpose=Shell command run on the rebased tree to validate a merge before the parent branch is touched"`
	ValidateTimeout           string `yaml:"validate_timeout,omitempty" sprawl:"purpose=Max wall-clock for validate as a Go duration (e.g. 20m); empty means the caller default"`
	ValidatePopupAfterSeconds int    `yaml:"validate_popup_after_seconds,omitempty" sprawl:"default=10,purpose=Seconds before the TUI auto-opens the validate-output popup"`
	// PauseTimeoutSeconds is the default escalation budget (in seconds) for
	// the `pause` MCP tool. QUM-722.
	PauseTimeoutSeconds int `yaml:"pause_timeout_seconds,omitempty" sprawl:"default=30,purpose=Escalation budget for the pause MCP tool in seconds"`
	// HubURL is the lowest-precedence source for the hub endpoint resolver
	// (flag > env > user > this). Default empty: there is NO baked-in hub
	// endpoint (public-repo hygiene). QUM-875.
	HubURL string `yaml:"hub_url,omitempty" sprawl:"purpose=Hub endpoint; lowest-precedence source after flag, env and user config"`
	// HubTokenFile is the lowest-precedence source for the host bearer token
	// (env SPRAWL_HUB_TOKEN wins). Path to a 0600 file holding the token; the
	// token is NEVER placed on a CLI flag or in a URL. QUM-877.
	HubTokenFile string `yaml:"hub_token_file,omitempty" sprawl:"purpose=Path to a 0600 file holding the host bearer token"`
	// MemoryModel overrides the Claude model used for memory consolidation.
	// Read by internal/rootinit. Empty falls back to memory.DefaultMemoryModel.
	MemoryModel string `yaml:"memory_model,omitempty" sprawl:"purpose=Claude model used for memory consolidation; empty uses the built-in default"`
	// WorktreeSetup is bash run in each new agent worktree before the agent
	// starts. In this repo it is what installs the QUM-808 pre-commit guard and
	// the QUM-837 reference-transaction guard, and copies CLAUDE.local.md and
	// .env — so losing it silently disables the main-protection controls.
	WorktreeSetup string `yaml:"worktree.setup,omitempty" sprawl:"purpose=Bash run in each new agent worktree before the agent starts"`
	// WorktreeTeardown is bash run in an agent worktree before removal on
	// retire.
	WorktreeTeardown string `yaml:"worktree.teardown,omitempty" sprawl:"purpose=Bash run in an agent worktree before removal on retire"`
	// IdleReclaimAfter / IdleReclaimSweep are the QUM-1186 idle reaper's two
	// knobs, read once by NewReal.
	//
	// They are duration STRINGS rather than int seconds, and that is deliberate.
	// Load never prefills, so an absent int key decodes to 0 — and 0 here means
	// DISABLED. An int knob would therefore ship the reaper switched off for
	// every user who has never edited their config, silently. With a string,
	// absent ("") and an explicit "0" are distinguishable, so "absent → default"
	// and "0 → disabled" are both true at once.
	IdleReclaimAfter string `yaml:"idle_reclaim.after,omitempty" sprawl:"default=0 (DISABLED),purpose=Idle time before an agent's subprocess is reclaimed as a Go duration. DEFAULT 0 = OFF: enabling is gated on QUM-1213 (LastActivityAt can go stale during a long tool call and the quiescent term reads it). A wedged background task also has no auto-expiry by design, so it pins its agent until an operator notices"`
	IdleReclaimSweep string `yaml:"idle_reclaim.sweep,omitempty" sprawl:"default=1m,purpose=How often the idle reaper sweeps the runtime registry as a Go duration"`

	// sprawlRoot is not a config key. Unexported, so yaml ignores it on both
	// unmarshal and marshal.
	sprawlRoot string
}

// DefaultPauseTimeoutSeconds is the fallback pause-escalation budget. QUM-722.
const DefaultPauseTimeoutSeconds = 30

// DefaultValidatePopupAfterSeconds is the default threshold after which the
// TUI validate-output popup auto-opens for a running merge validate (QUM-588).
const DefaultValidatePopupAfterSeconds = 10

// PauseTimeout returns the configured pause timeout, or the default when
// unset/non-positive.
func (c *Config) PauseTimeout() time.Duration {
	if c.PauseTimeoutSeconds <= 0 {
		return time.Duration(DefaultPauseTimeoutSeconds) * time.Second
	}
	return time.Duration(c.PauseTimeoutSeconds) * time.Second
}

// ValidatePopupAfter returns the configured popup-open threshold or the
// default when unset (zero or negative).
func (c *Config) ValidatePopupAfter() time.Duration {
	if c.ValidatePopupAfterSeconds <= 0 {
		return time.Duration(DefaultValidatePopupAfterSeconds) * time.Second
	}
	return time.Duration(c.ValidatePopupAfterSeconds) * time.Second
}

// ValidateTimeoutDuration returns the parsed validate_timeout, or 0 if unset
// or unparseable. Callers should layer their own default on top. QUM-496.
func (c *Config) ValidateTimeoutDuration() time.Duration {
	// The TrimSpace is defensive and expected to be a no-op: Load and Set both
	// trim on the way in. Kept so this function's correctness does not depend on
	// an unstated caller invariant.
	v := strings.TrimSpace(c.ValidateTimeout)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// DefaultIdleReclaimAfter is 0, meaning the idle reaper is OFF unless a project
// opts in. A deliberate reversal of the original 15-minute default, made on
// evidence: a child was torn down twice on a clean host while work it had
// started was still live.
//
// The MECHANISM originally recorded here — "the predicate cannot see a child
// that is mid-tool-call, the in_turn authority reads idle" — was WITHDRAWN by
// the QUM-1197 ruling of 2026-08-10 after five runs failed to support it. The
// real defect was a MISSING TERM: an agent that backgrounds a tool call or
// spawns a sidechain ENDS ITS TURN, so every term read idle honestly while the
// work ran. That term (work_outstanding) now exists.
//
// So the switch stays off for DIFFERENT reasons, and they are the ones to state:
//   - QUM-1213: LastActivityAt can go stale during a long tool call, and the
//     quiescent term reads it.
//   - A wedged background task has no auto-expiry by design (any cap short
//     enough to clear a two-hour wedge also clears a legitimate build), so it
//     pins its agent until an operator reads the refusal record.
//
// The machinery ships and is covered; only the switch is off. QUM-1186/QUM-1197.
const DefaultIdleReclaimAfter = 0

// SuggestedIdleReclaimAfter is what the threshold should be once QUM-1197 makes
// the predicate safe to enable: long enough that an agent between operator
// messages is not churned, short enough to bound a parked agent's ~280MB RSS.
// Kept as a named constant so the intended value is not lost with the default.
const SuggestedIdleReclaimAfter = 15 * time.Minute

// DefaultIdleReclaimSweep is how often the registry is swept. One minute: the
// sweep is a handful of cheap local observations per agent, and a cadence much
// coarser than this would make the threshold's effective resolution the sweep
// interval rather than the threshold. QUM-1186.
const DefaultIdleReclaimSweep = time.Minute

// IdleReclaimAfterDuration returns the idle-reclaim threshold. Unset returns
// the default; an explicit "0"/"0s" returns 0, which disables the reaper.
//
// Unlike ValidateTimeoutDuration, an UNPARSEABLE value does not return 0: it
// returns the default alongside a non-nil error. A typo ("15min") silently
// switching off a memory reclaimer is the failure mode this repo keeps paying
// for — the caller must be able to tell "the user turned it off" from "the
// user mistyped it".
func (c *Config) IdleReclaimAfterDuration() (time.Duration, error) {
	return parseDurationKey("idle_reclaim.after", c.IdleReclaimAfter, DefaultIdleReclaimAfter)
}

// IdleReclaimSweepDuration returns the reaper's sweep interval, with the same
// unset/zero/unparseable contract as IdleReclaimAfterDuration.
func (c *Config) IdleReclaimSweepDuration() (time.Duration, error) {
	return parseDurationKey("idle_reclaim.sweep", c.IdleReclaimSweep, DefaultIdleReclaimSweep)
}

// parseDurationKey resolves a duration-string key: empty → def, parseable →
// the parsed value (including an explicit zero), unparseable → (def, error).
func parseDurationKey(key, raw string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("config: %s = %q is not a Go duration (e.g. 15m, 90s, or 0 to disable): %w", key, raw, err)
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Registry: derived from Config's struct tags, so it cannot drift from the
// parser or from the reference table.
// ---------------------------------------------------------------------------

// keyInfo describes one recognized config key.
type keyInfo struct {
	Name       string
	FieldIndex int
	Kind       reflect.Kind
	Nested     bool
	Default    string
	Purpose    string
}

var registry = sync.OnceValue(func() map[string]keyInfo {
	out := map[string]keyInfo{}
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		// Only string and int are supported, and an unsupported kind fails LOUDLY
		// rather than degrading. Without this, adding e.g. a bool field would
		// panic inside Set's reflect.SetString, make Get return the literal
		// "<bool Value>", and make Reference() mislabel the row as `string` — all
		// with a green test suite, because both the code and the test derive the
		// type the same "not int ⇒ string" way. The struct doc promises "adding a
		// field here is the whole job of adding a key"; this is what keeps that
		// promise honest. Pinned by TestRegistry_OnlySupportedKinds.
		if f.Type.Kind() != reflect.String && f.Type.Kind() != reflect.Int {
			panic(fmt.Sprintf("config: field %s (key %q) has unsupported kind %s; "+
				"only string and int are supported — add explicit handling in Get, Set, "+
				"Reference and Load's per-key decode before using another type",
				f.Name, name, f.Type.Kind()))
		}

		ki := keyInfo{
			Name:       name,
			FieldIndex: i,
			Kind:       f.Type.Kind(),
			// No key is nested today. The column exists because the reference must
			// state it per QUM-1086, and because a future struct-typed field would
			// make it meaningful.
			Nested:  false,
			Default: "-",
		}
		// The `sprawl` tag is parsed ORDER-INDEPENDENTLY. `purpose=` consumes the
		// rest of the tag verbatim so purpose text may contain commas, which means
		// `default=` has to be found across the whole tag rather than only in the
		// part before `purpose=` — otherwise `sprawl:"purpose=x,default=5"` would
		// silently drop the default and swallow it into the purpose text. That
		// would be exactly the kind of unstated, unenforced invariant this issue
		// exists to delete. Pinned by TestReference_DefaultsRenderRegardlessOfTagOrder.
		tag := f.Tag.Get("sprawl")
		ki.Default = defaultFromTag(tag, ki.Default)
		if _, purpose, ok := strings.Cut(tag, "purpose="); ok {
			ki.Purpose = purpose
		}
		out[name] = ki
	}
	return out
})

// defaultFromTag extracts `default=<v>` from a sprawl struct tag, wherever it
// appears relative to `purpose=`. It scans the comma-separated parts BEFORE any
// `purpose=` (which consumes the rest of the tag), and also accepts
// `default=` appearing after a purpose by scanning the whole tag as a fallback.
// Returns fallback when no default is declared.
func defaultFromTag(tag, fallback string) string {
	for _, part := range strings.Split(tag, ",") {
		k, v, found := strings.Cut(part, "=")
		if !found || strings.TrimSpace(k) != "default" {
			continue
		}
		// A default declared after `purpose=` would otherwise be swallowed into
		// the purpose text; trim any trailing purpose fragment.
		if idx := strings.Index(v, "purpose="); idx >= 0 {
			v = v[:idx]
		}
		return strings.TrimSpace(v)
	}
	return fallback
}

// KnownKeys returns every recognized config key, sorted.
func KnownKeys() []string {
	reg := registry()
	keys := make([]string, 0, len(reg))
	for k := range reg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// retiredKeys maps a key that USED to exist to the remedy for it. A retired key
// has no near-match in the recognized set, so did-you-mean produces nothing
// useful and the migration has to be stated outright.
var retiredKeys = map[string]string{
	"liveness": "removed in QUM-1071 — delete this block",
}

// RetiredKeys returns the retired-key table (key -> remedy). Exported so tests
// can assert disjointness from the recognized set without reaching into package
// internals.
func RetiredKeys() map[string]string {
	out := make(map[string]string, len(retiredKeys))
	for k, v := range retiredKeys {
		out[k] = v
	}
	return out
}

// Reference renders the recognized-key table. It is generated from Config's
// struct tags, so a newly added field appears here with no hand-written list to
// update — and the same text backs both `sprawl config --help` and every
// unrecognized-key error.
//
// The layout is plain aligned columns (not markdown): it is printed to a
// terminal, and tests key on the row's first whitespace-separated field being
// the key name.
//
// It deliberately ends at the table and the flat-keys fact, with NO "what to do
// next" line, for two reasons now. The right next action differs by caller (edit
// the file, vs re-run the command with a valid key), so that line belongs to
// whoever is rendering the error; and since the ordering fix this is no longer
// the tail of the unrecognized-key message anyway — the offending keys and the
// next step are rendered after it, so a next-step line here would land in the
// middle. `sprawl config --help` embeds this verbatim.
func Reference() string {
	reg := registry()
	keys := KnownKeys()

	keyW := len("key")
	for _, k := range keys {
		if len(k) > keyW {
			keyW = len(k)
		}
	}

	var b strings.Builder
	b.WriteString("recognized keys:\n\n")
	fmt.Fprintf(&b, "  %-*s  %-8s  %-6s  %-8s  %s\n", keyW, "key", "type", "nested", "default", "purpose")
	fmt.Fprintf(&b, "  %s  %s  %s  %s  %s\n",
		strings.Repeat("-", keyW), strings.Repeat("-", 8), strings.Repeat("-", 6),
		strings.Repeat("-", 8), strings.Repeat("-", 32))
	for _, k := range keys {
		ki := reg[k]
		typ := "string"
		if ki.Kind == reflect.Int {
			typ = "int"
		}
		nested := "no"
		if ki.Nested {
			nested = "yes"
		}
		fmt.Fprintf(&b, "  %-*s  %-8s  %-6s  %-8s  %s\n", keyW, k, typ, nested, ki.Default, ki.Purpose)
	}
	b.WriteString("\nall keys are flat scalars; there are no nested sections.\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Load / Save
// ---------------------------------------------------------------------------

// configPath returns the config file path under a sprawl root.
func configPath(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "config.yaml")
}

// Load reads .sprawl/config.yaml from the given sprawl root directory.
//
// Returns a zero-value Config (no error) when the file does not exist, is
// empty, or contains only comments. Defaults are NOT prefilled into fields —
// they live in the accessors — so Save can never write today's default into a
// user's file as if they had chosen it.
//
// Any unrecognized key is a HARD ERROR (QUM-1086). This file carries two
// main-protection guards and the defect being fixed was entirely one of
// silence, so the error names every offending key with its line number, offers
// a did-you-mean, states the remedy for retired keys, and prints the full
// recognized-key reference — a user hitting it may have no AI assistance
// available to work out what the valid keys are.
func Load(sprawlRoot string) (*Config, error) {
	cfg := &Config{sprawlRoot: sprawlRoot}

	path := configPath(sprawlRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// Parse ONCE into a node tree: the node is walked for unrecognized keys and
	// then decoded into the struct. This unmarshal is also what reports genuine
	// syntax errors and duplicate mapping keys.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	// Empty file / comments only: yaml leaves a zero Node with no content.
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return cfg, nil
	}
	root := doc.Content[0]
	// A bare `---` document decodes to an explicit null scalar; that is "nothing
	// configured", not a malformed file.
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return cfg, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parsing %s: line %d: the config must be a mapping of keys to values, "+
			"got a %s\n\n%s", path, root.Line, nodeKindName(root.Kind), Reference())
	}

	// First pass: collect EVERY unrecognized key before failing, so one run
	// reports all of them. Duplicate keys are caught here too.
	//
	// Duplicates need an explicit check: yaml.v3 reports them when a MAPPING is
	// decoded, and the per-key decode below never decodes the mapping as a whole,
	// so without this a duplicate would silently take the last value. Verified
	// against yaml.v3 v3.0.1 — the node unmarshal itself returns nil and leaves
	// both pairs in Content.
	//
	// A duplicate returns IMMEDIATELY, unlike unrecognized keys which are all
	// collected. That asymmetry is deliberate: a duplicated key makes the file
	// ambiguous about which value is intended, so there is nothing useful to say
	// about the rest of it until that is resolved. An unrecognized key leaves the
	// remaining keys perfectly readable, so those are all reported at once.
	var problems []KeyProblem
	seen := make(map[string]int, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if firstLine, dup := seen[key.Value]; dup {
			return nil, fmt.Errorf("parsing %s: line %d: %s is defined twice "+
				"(first on line %d) — a config key may appear only once",
				path, key.Line, key.Value, firstLine)
		}
		seen[key.Value] = key.Line

		if _, ok := registry()[key.Value]; ok {
			continue
		}
		// Deliberately does NOT recurse into the value: an unrecognized key is
		// reported once, whatever shape its value has. A nested block, a
		// sequence, and a flat scalar are the same node-level event.
		problems = append(problems, KeyProblem{Key: key.Value, Line: key.Line})
	}
	if len(problems) > 0 {
		return nil, &UnknownKeysError{Path: path, Problems: problems}
	}

	// Second pass: decode each value into its field individually, driven by the
	// same registry that just validated the keys. Decoding the whole node into
	// the struct would work, but yaml.v3's type errors name the Go TYPE
	// ("cannot unmarshal !!str `sixty` into int") and never the config key,
	// which is the one thing the user needs.
	structVal := reflect.ValueOf(cfg).Elem()
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		// No ok-check needed, and that is a COUPLING to pass 1 above rather than
		// an oversight: pass 1 returned an error on any key not in the registry,
		// so every key here is known. A zero keyInfo would decode into field 0.
		ki := registry()[key.Value]
		if err := val.Decode(structVal.Field(ki.FieldIndex).Addr().Interface()); err != nil {
			return nil, fmt.Errorf("parsing %s: line %d: %s: %w", path, key.Line, key.Value, err)
		}
	}
	trimStringFields(cfg)

	return cfg, nil
}

// nodeKindName renders a yaml.Kind for a user-facing message.
func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "sequence"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	case yaml.MappingNode:
		return "mapping"
	case yaml.DocumentNode:
		return "document"
	default:
		return "unknown node"
	}
}

// trimStringFields trims every string field on the config.
//
// Trimming happens at the two write boundaries — Load (from disk) and Set (from
// the CLI) — rather than in the accessors, so the in-memory value is canonical
// and every reader sees the same string. Trimming in an accessor would be
// trimming in SOME accessors: agentops/merge.go reads the exported
// cfg.Validate field directly, and that divergence between "the value" and
// "what some function computes from it" is the dual-representation defect
// QUM-1086 deletes, one level down. It is applied to EVERY string field rather
// than a named subset, because a per-key trim list is itself another
// hand-maintained list of the kind being removed.
func trimStringFields(cfg *Config) {
	v := reflect.ValueOf(cfg).Elem()
	for _, ki := range registry() {
		if ki.Kind != reflect.String {
			continue
		}
		f := v.Field(ki.FieldIndex)
		f.SetString(strings.TrimSpace(f.String()))
	}
}

// Get returns the value for the given config key and whether it is SET.
//
// `ok` reports that the key is recognized AND holds a non-zero value. That
// matches every consumer's usage (`v, ok := cfg.Get(...)` treating !ok as "not
// configured") and keeps one consistent notion of "set" across Get, Keys and
// Save. An unrecognized key reads as ("", false) rather than erroring — the
// consumer form has no error slot.
func (c *Config) Get(key string) (string, bool) {
	ki, ok := registry()[key]
	if !ok {
		return "", false
	}
	f := reflect.ValueOf(c).Elem().Field(ki.FieldIndex)
	if ki.Kind == reflect.Int {
		if f.Int() == 0 {
			return "", false
		}
		return strconv.FormatInt(f.Int(), 10), true
	}
	s := f.String()
	return s, s != ""
}

// Set updates the value for the given config key.
//
// Returns an error for an unrecognized key rather than silently persisting a
// typo (the previous behaviour, which is how a misspelled worktree.setup could
// leave an operator believing the commit guards were installed).
func (c *Config) Set(key, value string) error {
	ki, ok := registry()[key]
	if !ok {
		return &UnknownKeysError{FromSet: true, Problems: []KeyProblem{{Key: key}}}
	}

	f := reflect.ValueOf(c).Elem().Field(ki.FieldIndex)
	value = strings.TrimSpace(value)

	switch ki.Kind {
	case reflect.Int:
		if value == "" {
			// Explicitly "unset — use the built-in default".
			f.SetInt(0)
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s expects an integer, got %q\n\n%s", key, value, keyReferenceRow(ki))
		}
		f.SetInt(int64(n))
		return nil
	case reflect.String:
		f.SetString(value)
		return nil
	default:
		// Unreachable: registry() panics on any other kind. Returning an error
		// rather than falling through to SetString means that if the guard is
		// ever loosened, this fails with a message instead of a reflect panic
		// out of a CLI command.
		return fmt.Errorf("config: key %s has unsupported kind %s", key, ki.Kind)
	}
}

// keyReferenceRow renders the reference row for a single key — used when the
// key was recognized and only the value was wrong, where the whole table would
// be noise.
func keyReferenceRow(ki keyInfo) string {
	typ := "string"
	if ki.Kind == reflect.Int {
		typ = "int"
	}
	return fmt.Sprintf("  %s  %s  default: %s  %s\n", ki.Name, typ, ki.Default, ki.Purpose)
}

// Keys returns the recognized keys that are actually SET, sorted. It backs
// `sprawl config show`; KnownKeys and Reference are the full-list surfaces.
func (c *Config) Keys() []string {
	var keys []string
	for _, k := range KnownKeys() {
		if _, ok := c.Get(k); ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// saveHeader is prepended to a written config so the file explains itself.
const saveHeader = "# Managed by `sprawl config`. Run `sprawl config --help` for the key reference.\n" +
	"# An absent or empty value means \"use the built-in default\".\n"

// Save writes the config back to .sprawl/config.yaml, creating the directory if
// needed.
//
// It marshals the struct, so no field can be silently dropped — which is what
// made `sprawl config set` a data-loss path before QUM-1086 (Save marshalled a
// side map that Load had already truncated). Unset keys are omitted via
// `omitempty`, so a single `config set` cannot litter the file with empty
// values.
func (c *Config) Save() error {
	dir := filepath.Join(c.sprawlRoot, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return fmt.Errorf("creating .sprawl directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(saveHeader+string(data)), 0o644); err != nil { //nolint:gosec // G306: config.yaml is checked into git, world-readable is intentional
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
