package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOutputSchemaValidatesNestedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	const schema = `{
  "type": "object",
  "required": ["result"],
  "properties": {
    "result": {
      "type": "object",
      "required": ["items"],
      "properties": {"items": {"type": "array", "minItems": 1}}
    }
  }
}`
	if err := os.WriteFile(path, []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	validate, err := loadOutputSchema(path)
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	if err := validate(`{"result":{"items":["ok"]}}`); err != nil {
		t.Fatalf("valid instance rejected: %v", err)
	}
	if err := validate(`{"result":{"items":[]}}`); err == nil {
		t.Fatal("expected nested validation failure")
	}
}

func TestLoadOutputSchemaResolvesRelativeReferences(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "defs.json"), []byte(`{"$defs":{"answer":{"type":"string","minLength":1}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(`{"type":"object","properties":{"answer":{"$ref":"defs.json#/$defs/answer"}},"required":["answer"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	validate, err := loadOutputSchema(path)
	if err != nil {
		t.Fatalf("load schema with relative ref: %v", err)
	}
	if err := validate(`{"answer":"ok"}`); err != nil {
		t.Fatalf("valid relative ref instance rejected: %v", err)
	}
	if err := validate(`{"answer":""}`); err == nil {
		t.Fatal("expected relative ref validation failure")
	}
}

func TestLoadOutputSchemaRejectsNonJSONAndInvalidSchema(t *testing.T) {
	cases := []struct {
		name     string
		contents string
	}{
		{name: "non-json schema", contents: "{"},
		{name: "invalid schema keyword", contents: `{"type": "not-a-json-type"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schema.json")
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOutputSchema(path); err == nil {
				t.Fatal("expected schema load failure")
			}
		})
	}
}

func TestOutputSchemaRejectsNonJSONResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	validate, err := loadOutputSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	err = validate("not JSON")
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error=%v, want non-JSON classification", err)
	}
}
