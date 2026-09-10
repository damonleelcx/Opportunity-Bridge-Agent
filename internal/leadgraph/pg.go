package leadgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Where the graph survives a restart.
//
// WHY THIS FAILS LOUDLY WHERE THE SIBLING PRODUCT DEGRADES QUIETLY
//
//	internal/store logs a failed write and carries on: a resident's question is
//	still answered, and what is lost is the memory of it. That is right for a
//	service somebody is talking to right now.
//
//	This is a system of record a firm pays for. A write that did not persist
//	must not report success - a consultant who is told "已记到关系脉络" and finds
//	it gone on Monday has been lied to by the product, and will stop telling it
//	things, which is the death this whole design is built to avoid. So a failed
//	write returns an error, and where a signature cannot carry one the store
//	marks itself degraded and the HTTP layer refuses writes.
//
// WHY ONE GENERIC UPSERT AND NOT EIGHT
//
//	Every table here has the same shape: some promoted columns that are queried
//	or constrained, plus the record itself as a document. One upsert that takes
//	the table, the columns and the document means adding a record type is a call
//	site, not a new hand-written statement to get subtly wrong.

type pgBackend struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// openPG connects and applies the schema. It fails rather than degrading: a
// deployment configured for postgres that cannot reach it is a deployment whose
// operator believes their data is safe.
func openPG(ctx context.Context, dsn string, log *slog.Logger) (*pgBackend, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL_INVALID: %w", err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_UNREACHABLE: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("DATABASE_UNREACHABLE: %w", err)
	}
	b := &pgBackend{pool: pool, log: log}
	if err := b.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return b, nil
}

// migrate applies every schema file in name order, on every start.
//
// Each statement carries its own IF NOT EXISTS, so the second run and the
// thousandth are the same as the first. A tracking table would be a claim ABOUT
// the schema; the schema is the truth.
func (b *pgBackend) migrate(ctx context.Context) error {
	entries, err := SchemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("SCHEMA_UNREADABLE: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := SchemaFS.ReadFile("schema/" + name)
		if err != nil {
			return fmt.Errorf("SCHEMA_UNREADABLE: %s: %w", name, err)
		}
		// Applied as one statement batch per file, NOT one transaction across
		// files: a later file failing must not undo an earlier one that is
		// already true of the database.
		if _, err := b.pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("SCHEMA_FAILED: %s: %w", name, err)
		}
	}
	b.log.Info("schema applied", "code", "SCHEMA_READY", "files", len(names))
	return nil
}

func (b *pgBackend) close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

// upsert writes one record. cols are the promoted columns including the key;
// key names the column(s) that decide identity.
func (b *pgBackend) upsert(table string, key []string, cols map[string]any, doc any) error {
	if b == nil {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("ENCODE_FAILED: %s: %w", table, err)
	}
	cols["doc"] = raw

	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)

	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	sets := make([]string, 0, len(names))
	for i, n := range names {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = cols[n]
		if !contains(key, n) {
			sets = append(sets, fmt.Sprintf("%s = EXCLUDED.%s", n, n))
		}
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s",
		table, join(names), join(placeholders), join(key), join(sets))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("WRITE_FAILED: %s: %w", table, err)
	}
	return nil
}

func (b *pgBackend) delete(table, where string, args ...any) error {
	if b == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.pool.Exec(ctx, "DELETE FROM "+table+" WHERE "+where, args...); err != nil {
		return fmt.Errorf("DELETE_FAILED: %s: %w", table, err)
	}
	return nil
}

// loadAll reads every document back into memory at start. Reads are served from
// memory afterwards, which is why the read path is identical with and without
// a database.
func (b *pgBackend) loadAll(ctx context.Context, table string, fn func(json.RawMessage) error) error {
	rows, err := b.pool.Query(ctx, "SELECT doc FROM "+table)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: %s: %w", table, err)
		}
		if err := fn(raw); err != nil {
			return fmt.Errorf("LOAD_CORRUPT: %s: %w", table, err)
		}
	}
	return rows.Err()
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
