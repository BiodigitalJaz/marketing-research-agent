// Package structured compiles JSON Schemas once and validates every
// structured LLM output against them, regardless of provider.
package structured

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrSchemaViolation wraps outputs that are not valid JSON or do not match
// the schema. Callers record it on the model_runs row; the router decides
// whether to retry or fail over.
var ErrSchemaViolation = errors.New("structured output violates schema")

// Schema is a compiled JSON Schema plus its raw form for sending to providers.
type Schema struct {
	Name string
	Raw  json.RawMessage
	sch  *jsonschema.Schema
}

// Compile parses and compiles raw. name labels the schema for providers
// that require one (OpenAI response_format).
func Compile(name string, raw json.RawMessage) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	url := "mem://" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("add schema %s: %w", name, err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", name, err)
	}
	return &Schema{Name: name, Raw: raw, sch: sch}, nil
}

// MustCompile is Compile for package-level schemas known at build time.
func MustCompile(name string, raw string) *Schema {
	s, err := Compile(name, json.RawMessage(raw))
	if err != nil {
		panic(err)
	}
	return s
}

// Validate checks text (a model's structured output) and returns it as
// compact JSON. Surrounding whitespace and a ```json fence are tolerated;
// anything else that is not schema-valid JSON is ErrSchemaViolation.
func (s *Schema) Validate(text string) (json.RawMessage, error) {
	body := stripFence(strings.TrimSpace(text))
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: not valid JSON: %w", ErrSchemaViolation, s.Name, err)
	}
	if err := s.sch.Validate(inst); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchemaViolation, s.Name, err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(body)); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchemaViolation, s.Name, err)
	}
	return buf.Bytes(), nil
}

// Decode validates text and unmarshals it into v.
func (s *Schema) Decode(text string, v any) error {
	raw, err := s.Validate(text)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode %s: %w", s.Name, err)
	}
	return nil
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
