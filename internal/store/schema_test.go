package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Payload validation is against the schema the CALLER PINNED, and the validator
// deliberately supports only the keyword subset the DDL comment scopes
// json_schema to ("required payload fields"). Anything outside that subset is a
// hard error, never an ignore — see TestValidate_UnsupportedKeywordIsRefused for
// why that is the load-bearing decision rather than a limitation.

const objSchema = `{
  "type": "object",
  "required": ["agent_name", "turns"],
  "properties": {
    "agent_name": {"type": "string"},
    "turns": {"type": "integer"},
    "cost_usd": {"type": "number"},
    "legacy": {"type": "boolean"}
  }
}`

func TestValidate_MissingRequiredFieldIsRejected(t *testing.T) {
	err := Validate(json.RawMessage(objSchema), []byte(`{"turns": 3}`))
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("payload missing the required field `agent_name`: got err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), "agent_name") {
		t.Errorf("the error must name the offending field so an operator can act on it; got: %v", err)
	}
}

// TestValidate_CompletePayloadIsAccepted is the negative control for every
// rejection assertion in this file: a validator that refuses everything would
// satisfy all of them.
func TestValidate_CompletePayloadIsAccepted(t *testing.T) {
	payload := []byte(`{"agent_name":"finn","turns":3,"cost_usd":0.42,"legacy":false}`)
	if err := Validate(json.RawMessage(objSchema), payload); err != nil {
		t.Fatalf("a payload satisfying the schema must be accepted; got: %v", err)
	}
}

// TestValidate_OptionalFieldMayBeAbsent pins that `properties` alone does not
// imply required. run_finished carries card_version_id, which is nullable until
// M2 — if absence of a declared-but-not-required property were an error, every
// M1a lifecycle append would fail.
func TestValidate_OptionalFieldMayBeAbsent(t *testing.T) {
	if err := Validate(json.RawMessage(objSchema), []byte(`{"agent_name":"finn","turns":1}`)); err != nil {
		t.Fatalf("a declared but not-required property must be allowed to be absent; got: %v", err)
	}
}

func TestValidate_WrongTypeIsRejected(t *testing.T) {
	cases := map[string]string{
		"string where integer expected": `{"agent_name":"finn","turns":"three"}`,
		"number where string expected":  `{"agent_name":7,"turns":3}`,
		"string where number expected":  `{"agent_name":"finn","turns":3,"cost_usd":"free"}`,
		"string where boolean expected": `{"agent_name":"finn","turns":3,"legacy":"yes"}`,
	}
	for name, payload := range cases {
		err := Validate(json.RawMessage(objSchema), []byte(payload))
		if !errors.Is(err, ErrSchemaViolation) {
			t.Errorf("%s: got err=%v, want ErrSchemaViolation", name, err)
		}
	}
}

// TestValidate_IntegerRejectsFractional pins that `integer` is not an alias for
// `number`. A turn count of 2.5 is a bug upstream, and accepting it puts a
// value in the log that no consumer can interpret.
func TestValidate_IntegerRejectsFractional(t *testing.T) {
	if err := Validate(json.RawMessage(objSchema), []byte(`{"agent_name":"finn","turns":2.5}`)); !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("an `integer` property must reject 2.5; got err=%v", err)
	}
	// Control: the same field accepts a whole number written as JSON float.
	if err := Validate(json.RawMessage(objSchema), []byte(`{"agent_name":"finn","turns":2.0}`)); err != nil {
		t.Errorf("`integer` must accept a whole number: got %v", err)
	}
}

// TestValidate_UnsupportedKeywordIsRefused is the decision that makes a
// hand-rolled validator defensible rather than a corner cut.
//
// A validator that silently IGNORED `maxLength` would let a schema author write
// a constraint that reads as enforced and is not — the author gets no error, the
// reviewer sees a constraint, and nothing enforces it. That is a false green
// manufactured at schema-authoring time, and it would be invisible forever.
// Failing loudly instead keeps the supported subset honest and makes swapping in
// a full JSON Schema library later a drop-in behind this same signature.
func TestValidate_UnsupportedKeywordIsRefused(t *testing.T) {
	unsupported := map[string]string{
		"root maxProperties":  `{"type":"object","maxProperties":2,"properties":{}}`,
		"property maxLength":  `{"type":"object","properties":{"a":{"type":"string","maxLength":10}}}`,
		"property enum":       `{"type":"object","properties":{"a":{"type":"string","enum":["x"]}}}`,
		"property pattern":    `{"type":"object","properties":{"a":{"type":"string","pattern":"^x"}}}`,
		"nested items":        `{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}}}`,
		"root oneOf":          `{"type":"object","oneOf":[{"required":["a"]}]}`,
		"unknown type name":   `{"type":"object","properties":{"a":{"type":"timestamp"}}}`,
		"non-object root":     `{"type":"array"}`,
		"root $ref":           `{"$ref":"#/definitions/x"}`,
		"property nested obj": `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`,
	}
	for name, schema := range unsupported {
		err := Validate(json.RawMessage(schema), []byte(`{"a":"x"}`))
		if !errors.Is(err, ErrUnsupportedSchemaKeyword) {
			t.Errorf("%s: got err=%v, want ErrUnsupportedSchemaKeyword — silently ignoring a keyword makes a schema that reads as enforced and is not", name, err)
		}
	}
}

// TestValidate_DocumentationKeywordsAreAllowed is the other direction: `title`
// and `description` constrain nothing, so refusing them would make every seed
// schema unwritable. Without this, the assertion above would be satisfied by a
// validator that refuses every schema.
func TestValidate_DocumentationKeywordsAreAllowed(t *testing.T) {
	schema := `{
	  "type": "object",
	  "title": "run finished",
	  "description": "emitted once per agent run",
	  "required": ["a"],
	  "properties": {"a": {"type": "string", "description": "the thing"}}
	}`
	if err := Validate(json.RawMessage(schema), []byte(`{"a":"x"}`)); err != nil {
		t.Fatalf("title/description constrain nothing and must be accepted; got: %v", err)
	}
}

// TestValidate_AdditionalPropertiesFalseIsEnforced pins that when a schema opts
// in, an undeclared key is a violation. Additive-only evolution means new keys
// arrive over time, so the DEFAULT must be permissive — asserted by the second
// leg, which is what stops this from being a validator that rejects any key it
// has not been told about.
func TestValidate_AdditionalPropertiesFalseIsEnforced(t *testing.T) {
	strict := `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string"}}}`
	if err := Validate(json.RawMessage(strict), []byte(`{"a":"x","surprise":1}`)); !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("additionalProperties:false must reject an undeclared key; got err=%v", err)
	}
	if err := Validate(json.RawMessage(strict), []byte(`{"a":"x"}`)); err != nil {
		t.Errorf("additionalProperties:false must still accept the declared key: %v", err)
	}

	lax := `{"type":"object","properties":{"a":{"type":"string"}}}`
	if err := Validate(json.RawMessage(lax), []byte(`{"a":"x","surprise":1}`)); err != nil {
		t.Errorf("without additionalProperties:false an undeclared key must be allowed (event schemas are additive-only): %v", err)
	}
}

func TestValidate_MalformedInputsAreRejected(t *testing.T) {
	if err := Validate(json.RawMessage(`{"type":"object"`), []byte(`{}`)); err == nil {
		t.Error("a malformed schema must be an error, not a pass")
	}
	if err := Validate(json.RawMessage(objSchema), []byte(`{"agent_name":`)); err == nil {
		t.Error("a malformed payload must be an error, not a pass")
	}
	if err := Validate(json.RawMessage(objSchema), []byte(`[]`)); !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("a non-object payload must be a schema violation; got err=%v", err)
	}
}

// TestValidate_EmptyPayloadAgainstNoRequiredFields pins the seed schemas that
// legitimately require nothing, so an empty object is valid. Without it, a
// validator could reject `{}` unconditionally and every such append would fail.
func TestValidate_EmptyPayloadAgainstNoRequiredFields(t *testing.T) {
	if err := Validate(json.RawMessage(`{"type":"object","properties":{}}`), []byte(`{}`)); err != nil {
		t.Fatalf("an empty payload against a schema requiring nothing must be accepted; got: %v", err)
	}
}
