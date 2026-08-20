package store

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Payload validation against a PINNED event-type schema.
//
// WHY THIS IS HAND-ROLLED, and why that is a decision rather than a shortcut.
//
// Appendix A scopes `event_type_schemas.json_schema` to "required payload
// fields", so the enforceable surface is small: an object, a required list,
// per-property scalar types, and optionally a closed key set. This validator
// implements exactly that and REFUSES anything else.
//
// The refusal is the load-bearing part. A hand-rolled validator that silently
// IGNORED an unsupported keyword would be strictly worse than no validator: a
// schema author writes `maxLength`, gets no error, a reviewer reads a
// constraint, and nothing enforces it — a false green manufactured at
// schema-authoring time that no runtime observation could ever reveal. Failing
// loudly keeps the supported subset honest, and keeps a swap to a full JSON
// Schema library (M2/M4, behind this same signature) a drop-in.
//
// NULL is a violation, not an absence. An optional field is expressed by
// leaving it out of `required`; emitters must OMIT a field they have no value
// for rather than sending an explicit null. Treating null as absent would mean
// a required field could be satisfied by a null, which is how a "required"
// column ends up full of nulls.

var (
	rootKeywords = map[string]bool{
		"type": true, "required": true, "properties": true,
		"additionalProperties": true, "title": true, "description": true,
	}
	// Per-property: a type, plus documentation that constrains nothing.
	propertyKeywords = map[string]bool{"type": true, "title": true, "description": true}
	propertyTypes    = map[string]bool{
		"string": true, "number": true, "integer": true,
		"boolean": true, "object": true, "array": true,
	}
)

// Validate checks payload against schema, returning ErrSchemaViolation for a bad
// payload and ErrUnsupportedSchemaKeyword for a schema this validator cannot
// enforce. The two are distinct because they have different owners: the first is
// the emitter's bug, the second the schema author's.
func Validate(schema json.RawMessage, payload []byte) error {
	root, err := parseSchema(schema)
	if err != nil {
		return err
	}

	obj, err := parsePayloadObject(payload)
	if err != nil {
		return err
	}

	for _, name := range root.required {
		raw, ok := obj[name]
		if !ok {
			return fmt.Errorf("%w: missing required field %q", ErrSchemaViolation, name)
		}
		// An explicit null does not satisfy `required`, and this check has to be
		// here rather than in checkType: checkType only runs for DECLARED
		// properties, so a field named in `required` but absent from `properties`
		// was previously satisfied by {"x": null} — the exact "required column
		// full of nulls" outcome the header rules out. Unreachable with today's
		// seeds, which declare every required field; a one-line gap between a
		// stated rule and the code is still a gap.
		if string(raw) == "null" {
			return fmt.Errorf("%w: required field %q is null; omit a field you have no value for rather than sending null",
				ErrSchemaViolation, name)
		}
	}

	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error selection

	for _, name := range names {
		typ, declared := root.properties[name]
		if !declared {
			// Event schemas are additive-only within a name, so an undeclared
			// key is normal unless the schema explicitly closed the set.
			if !root.additionalAllowed {
				return fmt.Errorf("%w: undeclared field %q and additionalProperties is false", ErrSchemaViolation, name)
			}
			continue
		}
		if err := checkType(name, typ, obj[name]); err != nil {
			return err
		}
	}
	return nil
}

type parsedSchema struct {
	required          []string
	properties        map[string]string // property name -> declared type
	additionalAllowed bool
}

func parseSchema(schema json.RawMessage) (*parsedSchema, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, fmt.Errorf("store: event-type schema does not parse: %w", err)
	}
	for k := range root {
		if !rootKeywords[k] {
			return nil, fmt.Errorf("%w: root keyword %q", ErrUnsupportedSchemaKeyword, k)
		}
	}

	var typ string
	if raw, ok := root["type"]; ok {
		if err := json.Unmarshal(raw, &typ); err != nil {
			return nil, fmt.Errorf("%w: root `type` is not a string", ErrUnsupportedSchemaKeyword)
		}
	}
	if typ != "object" {
		return nil, fmt.Errorf("%w: root type %q (event payloads are objects)", ErrUnsupportedSchemaKeyword, typ)
	}

	out := &parsedSchema{properties: map[string]string{}, additionalAllowed: true}

	if raw, ok := root["required"]; ok {
		if err := json.Unmarshal(raw, &out.required); err != nil {
			return nil, fmt.Errorf("%w: `required` is not a list of strings", ErrUnsupportedSchemaKeyword)
		}
	}

	if raw, ok := root["additionalProperties"]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			// The schema-object form of additionalProperties is a real JSON
			// Schema feature this validator does not implement.
			return nil, fmt.Errorf("%w: `additionalProperties` must be a boolean here", ErrUnsupportedSchemaKeyword)
		}
		out.additionalAllowed = b
	}

	if raw, ok := root["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(raw, &props); err != nil {
			return nil, fmt.Errorf("%w: `properties` is not an object", ErrUnsupportedSchemaKeyword)
		}
		for name, rawProp := range props {
			var prop map[string]json.RawMessage
			if err := json.Unmarshal(rawProp, &prop); err != nil {
				return nil, fmt.Errorf("%w: property %q is not an object", ErrUnsupportedSchemaKeyword, name)
			}
			for k := range prop {
				if !propertyKeywords[k] {
					return nil, fmt.Errorf("%w: property %q uses %q", ErrUnsupportedSchemaKeyword, name, k)
				}
			}
			var ptype string
			if rawType, ok := prop["type"]; ok {
				if err := json.Unmarshal(rawType, &ptype); err != nil {
					return nil, fmt.Errorf("%w: property %q has a non-string `type`", ErrUnsupportedSchemaKeyword, name)
				}
			}
			if !propertyTypes[ptype] {
				return nil, fmt.Errorf("%w: property %q declares type %q", ErrUnsupportedSchemaKeyword, name, ptype)
			}
			out.properties[name] = ptype
		}
	}
	return out, nil
}

// parsePayloadObject distinguishes "not parseable as JSON at all" (the
// emitter is broken) from "valid JSON that is not an object" (a schema
// violation). Collapsing the two would report an emitter bug as a schema
// violation and send whoever reads the error to the wrong place.
func parsePayloadObject(payload []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		var anyValue any
		if json.Unmarshal(payload, &anyValue) == nil {
			return nil, fmt.Errorf("%w: payload is not a JSON object", ErrSchemaViolation)
		}
		return nil, fmt.Errorf("store: payload does not parse as JSON: %w", err)
	}
	if obj == nil {
		// Literal `null` unmarshals into a nil map without error.
		return nil, fmt.Errorf("%w: payload is null", ErrSchemaViolation)
	}
	return obj, nil
}

func checkType(name, typ string, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("store: field %q does not parse: %w", name, err)
	}
	bad := func() error {
		return fmt.Errorf("%w: field %q must be %s, got %T", ErrSchemaViolation, name, typ, v)
	}
	switch typ {
	case "string":
		if _, ok := v.(string); !ok {
			return bad()
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad()
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return bad()
		}
	case "integer":
		f, ok := v.(float64)
		if !ok {
			return bad()
		}
		// `integer` is not an alias for `number`: a fractional turn count is an
		// upstream bug, and letting it land puts a value in the log no consumer
		// can interpret. A whole number written as 2.0 is still an integer.
		if f != math.Trunc(f) {
			return fmt.Errorf("%w: field %q must be an integer, got %v", ErrSchemaViolation, name, f)
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return bad()
		}
	case "array":
		if _, ok := v.([]any); !ok {
			return bad()
		}
	}
	return nil
}
