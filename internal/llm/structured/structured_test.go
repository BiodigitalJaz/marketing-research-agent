package structured

import (
	"errors"
	"testing"
)

const scoreSchema = `{
  "type": "object",
  "required": ["relevance", "explanation"],
  "additionalProperties": false,
  "properties": {
    "relevance": {"type": "number", "minimum": 0, "maximum": 1},
    "explanation": {"type": "string", "maxLength": 200}
  }
}`

func TestValidate(t *testing.T) {
	s := MustCompile("score", scoreSchema)
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"valid", `{"relevance": 0.7, "explanation": "fits"}`, true},
		{"fenced", "```json\n{\"relevance\": 1, \"explanation\": \"x\"}\n```", true},
		{"out of range", `{"relevance": 1.5, "explanation": "x"}`, false},
		{"missing field", `{"relevance": 0.5}`, false},
		{"extra field", `{"relevance": 0.5, "explanation": "x", "z": 1}`, false},
		{"prose", `Sure! {"relevance": 0.5}`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := s.Validate(tt.in)
			if tt.ok && err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrSchemaViolation) {
				t.Fatalf("want ErrSchemaViolation, got %v (%s)", err, out)
			}
		})
	}
}

func TestDecodeAndCompileErrors(t *testing.T) {
	s := MustCompile("score", scoreSchema)
	var v struct {
		Relevance float64 `json:"relevance"`
	}
	if err := s.Decode(`{"relevance": 0.25, "explanation": "x"}`, &v); err != nil || v.Relevance != 0.25 {
		t.Fatalf("decode: %v %v", v, err)
	}
	if _, err := Compile("bad", []byte(`{"type": 12}`)); err == nil {
		t.Fatal("expected compile error for invalid schema")
	}
}
