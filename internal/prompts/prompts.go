package prompts

import (
	"embed"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
)

//go:embed defaults/*.md
var defaultsFS embed.FS

// Default is a shipped prompt version. Changing a prompt means adding a new
// file (task.vN+1.md), never editing an existing one.
type Default struct {
	Task    string
	Version int32
	Body    string
}

var fileRe = regexp.MustCompile(`^([a-z_]+)\.v(\d+)\.md$`)

// Defaults returns every shipped prompt version, sorted by task then version.
func Defaults() ([]Default, error) {
	entries, err := defaultsFS.ReadDir("defaults")
	if err != nil {
		return nil, err
	}
	var out []Default
	for _, e := range entries {
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("prompt file %q does not match task.vN.md", e.Name())
		}
		v, err := strconv.ParseInt(m[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("prompt file %q: %w", e.Name(), err)
		}
		body, err := defaultsFS.ReadFile(path.Join("defaults", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Default{Task: m[1], Version: int32(v), Body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Task != out[j].Task {
			return out[i].Task < out[j].Task
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}
