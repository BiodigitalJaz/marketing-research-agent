// Package migrations embeds the goose SQL migrations so binaries and tests
// can apply them without a filesystem path.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
