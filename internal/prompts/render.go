package prompts

import (
	"bytes"
	"fmt"
	"text/template"
)

// Render executes a prompt body as a Go text/template against data.
// Unknown fields are errors, so a broken edit fails loudly.
func Render(name, body string, data any) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(body)
	if err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	return b.String(), nil
}
