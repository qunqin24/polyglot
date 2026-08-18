// Package migrations embeds Polyglot's SQL schema files. They are applied in
// filename order at startup; there is no separate migration tool to run.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
