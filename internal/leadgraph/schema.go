package leadgraph

import "embed"

// SchemaFS carries the DDL this package needs. It is exported so that the
// migration-idempotency fence can read the very files that ship, rather than a
// copy written for the test.
//
//go:embed schema/*.sql
var SchemaFS embed.FS
