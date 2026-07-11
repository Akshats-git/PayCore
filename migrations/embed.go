// Package migrations embeds the SQL migration files into the binary so they can
// be applied at startup with no external files to ship alongside the executable.
package migrations

import "embed"

// FS holds every *.sql migration in this directory. golang-migrate reads its
// ordered list of versions from these filenames.
//
//go:embed *.sql
var FS embed.FS
