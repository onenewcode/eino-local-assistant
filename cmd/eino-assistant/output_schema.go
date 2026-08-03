package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// outputSchemaValidator is deliberately a local post-processing validator.
// It does not alter provider requests or the ReAct loop.
type outputSchemaValidator struct {
	schema *jsonschema.Schema
}

func loadOutputSchema(path string) (func(string) error, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read output schema %q: %w", path, err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse output schema %q: %w", path, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve output schema %q: %w", path, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(absPath, document); err != nil {
		return nil, fmt.Errorf("load output schema %q: %w", path, err)
	}
	schema, err := compiler.Compile(absPath)
	if err != nil {
		return nil, fmt.Errorf("compile output schema %q: %w", path, err)
	}
	return (&outputSchemaValidator{schema: schema}).Validate, nil
}

func (v *outputSchemaValidator) Validate(content string) error {
	if v == nil || v.schema == nil {
		return nil
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(content)))
	if err != nil {
		return fmt.Errorf("final response is not valid JSON: %w", err)
	}
	if err := v.schema.Validate(instance); err != nil {
		return fmt.Errorf("final response does not match output schema: %w", err)
	}
	return nil
}
