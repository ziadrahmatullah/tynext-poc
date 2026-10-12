// Package migrations embeds the SQL migration files so cmd/migrate carries the
// schema with it instead of depending on the working directory.
package migrations

import "embed"

// FS holds the golang-migrate up/down pairs.
//
//go:embed *.sql
var FS embed.FS
