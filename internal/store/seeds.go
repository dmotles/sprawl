package store

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// Embedded seed event-type schemas (Appendix B item 14).
//
// They are compiled into the binary because degraded-mode validation must work
// with the database unreachable: the appender is handed a PINNED schema_id and
// has to know what that id means without being able to SELECT it.
//
// Ids are DERIVED rather than random — uuid.NewSHA1 over "<name>@<version>" —
// so the embedded registry and the SQL that seeds event_type_schemas agree
// without either being generated from the other, and so a pinned schema_id
// written into a real log years ago still resolves.
//
// CONSEQUENCE, and it is a hard one: the derivation inputs are FROZEN. Changing
// the namespace, the "@" separator, or an event type's name changes every id
// derived from it and strands every schema_id already recorded. An event type
// gets a new NAME (per the design's additive-only rule) or a new VERSION; it
// never gets a new derivation.
//
//go:embed seeds/*.json
var seedsFS embed.FS

// seedNamespace is the frozen UUIDv5 namespace for derived schema ids.
// Never change this value. See the CONSEQUENCE note above.
var seedNamespace = uuid.MustParse("8c1a0d3e-6b47-4f52-9a1d-2f7e5c904b18")

// SeedID derives the stable id for an event type's (name, version).
func SeedID(name string, version int) uuid.UUID {
	return uuid.NewSHA1(seedNamespace, []byte(fmt.Sprintf("%s@%d", name, version)))
}

// seedDoc is the on-disk shape of a seeds/*.json file.
type seedDoc struct {
	Name       string          `json:"name"`
	Version    int             `json:"version"`
	Opens      bool            `json:"opens"`
	Closes     string          `json:"closes"`
	JSONSchema json.RawMessage `json:"json_schema"`
}

// EventTypeSchema is one immutable, versioned event-type definition.
type EventTypeSchema struct {
	ID         uuid.UUID
	Name       string
	Version    int
	JSONSchema json.RawMessage
	// Closes holds the NAME (not an id) of the opener this type closes, because
	// a close type closes every version of its opener.
	Closes string
	// Opens marks a type that creates an outstanding contract. The appender
	// keys open_contracts maintenance off exactly this field.
	Opens bool
}

// Registry resolves event-type schemas by PINNED id.
//
// ByName exists for seeding and for tests. The appender does not use it: the
// design's version-pinning rule (Appendix B item 6) is that an emit call carries
// a schema_id and the appender validates against THAT, never against whatever
// is latest.
type Registry struct {
	byID   map[uuid.UUID]*EventTypeSchema
	byName map[string]*EventTypeSchema
	all    []*EventTypeSchema
}

func nameVersionKey(name string, version int) string {
	return fmt.Sprintf("%s@%d", name, version)
}

// NewRegistry indexes schemas. It rejects duplicate ids and duplicate
// (name, version) pairs: either would make a pinned reference ambiguous, which
// is the one thing the whole versioning scheme exists to prevent.
func NewRegistry(schemas []*EventTypeSchema) (*Registry, error) {
	r := &Registry{
		byID:   make(map[uuid.UUID]*EventTypeSchema, len(schemas)),
		byName: make(map[string]*EventTypeSchema, len(schemas)),
	}
	for _, s := range schemas {
		if _, dup := r.byID[s.ID]; dup {
			return nil, fmt.Errorf("store: duplicate event-type schema id %s (%s)", s.ID, s.Name)
		}
		key := nameVersionKey(s.Name, s.Version)
		if _, dup := r.byName[key]; dup {
			return nil, fmt.Errorf("store: duplicate event-type schema %s", key)
		}
		r.byID[s.ID] = s
		r.byName[key] = s
		r.all = append(r.all, s)
	}
	sort.Slice(r.all, func(i, j int) bool {
		if r.all[i].Name != r.all[j].Name {
			return r.all[i].Name < r.all[j].Name
		}
		return r.all[i].Version < r.all[j].Version
	})
	return r, nil
}

func (r *Registry) ByID(id uuid.UUID) (*EventTypeSchema, bool) {
	s, ok := r.byID[id]
	return s, ok
}

func (r *Registry) ByName(name string, version int) (*EventTypeSchema, bool) {
	s, ok := r.byName[nameVersionKey(name, version)]
	return s, ok
}

// All returns the schemas in a stable (name, version) order.
func (r *Registry) All() []*EventTypeSchema { return r.all }

var seedRegistryOnce = sync.OnceValues(loadSeedRegistry)

// SeedRegistry returns the embedded seed schemas, parsed once.
func SeedRegistry() (*Registry, error) { return seedRegistryOnce() }

func loadSeedRegistry() (*Registry, error) {
	entries, err := fs.ReadDir(seedsFS, "seeds")
	if err != nil {
		return nil, fmt.Errorf("store: reading embedded seeds: %w", err)
	}
	var schemas []*EventTypeSchema
	for _, e := range entries {
		body, err := fs.ReadFile(seedsFS, "seeds/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: reading seed %s: %w", e.Name(), err)
		}
		var doc seedDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("store: parsing seed %s: %w", e.Name(), err)
		}
		if doc.Name == "" || doc.Version <= 0 {
			return nil, fmt.Errorf("store: seed %s has no name or a non-positive version", e.Name())
		}
		if doc.Opens && doc.Closes != "" {
			return nil, fmt.Errorf("store: seed %s both opens and closes a contract", e.Name())
		}
		schemas = append(schemas, &EventTypeSchema{
			ID:         SeedID(doc.Name, doc.Version),
			Name:       doc.Name,
			Version:    doc.Version,
			JSONSchema: doc.JSONSchema,
			Closes:     doc.Closes,
			Opens:      doc.Opens,
		})
	}
	return NewRegistry(schemas)
}
