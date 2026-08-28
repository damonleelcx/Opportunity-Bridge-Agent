package store

// Postgres persistence.
//
// WHY THIS EXISTS
//   The store's only durable home was a JSON file on a node-local volume with
//   no backup: one disk, one copy, and a node failure loses every conversation
//   on it. Postgres is not here for scale — the whole dataset is a few hundred
//   kilobytes — it is here so the data sits somewhere that is replicated off the
//   node by an existing, verified backup.
//
// WHY A WHOLE-SNAPSHOT SYNC RATHER THAN PER-ENTITY WRITES
//   Save() makes the database match the in-memory snapshot, in one transaction.
//   The alternative — a targeted write at each of the fourteen mutation sites —
//   is faster and is WRONG FIRST: the failure mode of a forgotten site is data
//   that is correct on screen for the rest of the process's life and simply
//   absent after the next restart. Nothing would catch it until somebody's
//   record went missing. Syncing everything cannot forget.
//
//   The cost is O(rows) per mutation. At the scale this serves — tens of
//   subjects, hundreds of rows — that is a handful of milliseconds against a
//   database on the same node, and the read path never touches it at all
//   because reads are served from memory. The moment to revisit this is when a
//   single Save() exceeds the time a turn can spare; the shape to move to is a
//   dirty set marked at those same fourteen sites.
//
// WHAT IS NOT STORED HERE
//   Nothing is written that the snapshot does not already hold. Passwords were
//   already hashes (PBKDF2, see account.go); sign-ins were already token
//   hashes. Moving them into postgres changes where they live, not what they
//   are.

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// pgBackend is the postgres implementation of persistence.
type pgBackend struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// openPG connects, applies the schema and returns a backend ready to use.
//
// It fails rather than degrading. A file-backed store that cannot write is
// still a working service that forgets; a deployment configured for postgres
// that cannot reach it is a deployment whose operator believes their data is
// safe. Those must not look the same from outside, so this one refuses to start.
func openPG(ctx context.Context, dsn string, log *slog.Logger) (*pgBackend, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL_INVALID: %w", err)
	}
	// Small on purpose: writes are serialised behind the store's own mutex, so
	// a large pool would buy nothing and would only make a connection storm
	// after a restart more likely.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute

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

func (b *pgBackend) Close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

// migrate applies every file in schema/ in name order, every start.
//
// There is no version table and nothing to query for "where is this database
// up to". Every statement is idempotent, so applying them all is the same
// operation whether it is the first start or the thousandth — and a tracking
// table that says a migration ran is only ever a claim about the schema, which
// can be wrong in the one direction that matters.
func (b *pgBackend) migrate(ctx context.Context) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("SCHEMA_UNREADABLE: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := schemaFS.ReadFile("schema/" + name)
		if err != nil {
			return fmt.Errorf("SCHEMA_UNREADABLE: %s: %w", name, err)
		}
		// Not wrapped in an explicit transaction: DDL that is already applied is
		// a no-op, and holding one transaction across a whole file turns a
		// single unrelated failure into "none of the schema exists".
		if _, err := b.pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("MIGRATION_FAILED: %s: %w", name, err)
		}
	}
	b.log.Info("schema applied", "code", "SCHEMA_READY", "files", len(names))
	return nil
}

// ---------------------------------------------------------------------- load

func (b *pgBackend) Load(ctx context.Context, s *snapshot) error {
	if err := b.loadSessions(ctx, s); err != nil {
		return err
	}
	if err := b.loadProfiles(ctx, s); err != nil {
		return err
	}
	if err := b.loadTasks(ctx, s); err != nil {
		return err
	}
	if err := b.loadConsent(ctx, s); err != nil {
		return err
	}
	if err := b.loadApprovals(ctx, s); err != nil {
		return err
	}
	if err := b.loadSignals(ctx, s); err != nil {
		return err
	}
	if err := b.loadAccounts(ctx, s); err != nil {
		return err
	}
	if err := b.loadSignIns(ctx, s); err != nil {
		return err
	}
	return b.loadMeta(ctx, s)
}

func (b *pgBackend) loadSessions(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT doc FROM sessions`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: sessions: %w", err)
		}
		var ses Session
		if err := json.Unmarshal(raw, &ses); err != nil {
			return fmt.Errorf("LOAD_FAILED: sessions: %w", err)
		}
		s.Sessions[ses.ID] = &ses
	}
	return rows.Err()
}

func (b *pgBackend) loadProfiles(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT doc FROM profiles`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: profiles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: profiles: %w", err)
		}
		var p domain.Profile
		if err := json.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("LOAD_FAILED: profiles: %w", err)
		}
		s.Profiles[p.SubjectID] = &p
	}
	return rows.Err()
}

func (b *pgBackend) loadTasks(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT doc FROM case_tasks`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: case_tasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: case_tasks: %w", err)
		}
		var t domain.CaseTask
		if err := json.Unmarshal(raw, &t); err != nil {
			return fmt.Errorf("LOAD_FAILED: case_tasks: %w", err)
		}
		s.Tasks[t.ID] = &t
	}
	return rows.Err()
}

func (b *pgBackend) loadConsent(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx,
		`SELECT subject_id, scope, granted, granted_at, note FROM consent`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: consent: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subject, scope, note string
		var granted bool
		var at *time.Time
		if err := rows.Scan(&subject, &scope, &granted, &at, &note); err != nil {
			return fmt.Errorf("LOAD_FAILED: consent: %w", err)
		}
		if s.Consent[subject] == nil {
			s.Consent[subject] = map[domain.ConsentScope]domain.ConsentGrant{}
		}
		g := domain.ConsentGrant{
			Scope: domain.ConsentScope(scope), Granted: granted, Note: note,
		}
		if at != nil {
			g.GrantedAt = at.UTC()
		}
		s.Consent[subject][g.Scope] = g
	}
	return rows.Err()
}

func (b *pgBackend) loadApprovals(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT doc FROM approvals`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: approvals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: approvals: %w", err)
		}
		var a PendingApproval
		if err := json.Unmarshal(raw, &a); err != nil {
			return fmt.Errorf("LOAD_FAILED: approvals: %w", err)
		}
		s.Approvals[a.ID] = &a
	}
	return rows.Err()
}

func (b *pgBackend) loadSignals(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT doc FROM demand_signals ORDER BY ord`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: demand_signals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: demand_signals: %w", err)
		}
		var sig domain.DemandSignal
		if err := json.Unmarshal(raw, &sig); err != nil {
			return fmt.Errorf("LOAD_FAILED: demand_signals: %w", err)
		}
		s.Signals = append(s.Signals, sig)
	}
	return rows.Err()
}

func (b *pgBackend) loadAccounts(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT username, doc FROM accounts`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: accounts: %w", err)
		}
		var a Account
		if err := json.Unmarshal(raw, &a); err != nil {
			return fmt.Errorf("LOAD_FAILED: accounts: %w", err)
		}
		s.Accounts[key] = &a
	}
	return rows.Err()
}

func (b *pgBackend) loadSignIns(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx,
		`SELECT token_hash, username, expires_at FROM signins`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: signins: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash, user string
		var exp time.Time
		if err := rows.Scan(&hash, &user, &exp); err != nil {
			return fmt.Errorf("LOAD_FAILED: signins: %w", err)
		}
		s.SignIns[hash] = &SignIn{Username: user, ExpiresAt: exp.UTC()}
	}
	return rows.Err()
}

func (b *pgBackend) loadMeta(ctx context.Context, s *snapshot) error {
	rows, err := b.pool.Query(ctx, `SELECT key, value FROM meta`)
	if err != nil {
		return fmt.Errorf("LOAD_FAILED: meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return fmt.Errorf("LOAD_FAILED: meta: %w", err)
		}
		switch key {
		case metaSeq:
			if err := json.Unmarshal(raw, &s.Seq); err != nil {
				return fmt.Errorf("LOAD_FAILED: meta.%s: %w", key, err)
			}
		case metaLegacyAdopted:
			if err := json.Unmarshal(raw, &s.LegacyAdopted); err != nil {
				return fmt.Errorf("LOAD_FAILED: meta.%s: %w", key, err)
			}
		}
	}
	return rows.Err()
}

const (
	metaSeq           = "seq"
	metaLegacyAdopted = "legacy_adopted"
)

// ---------------------------------------------------------------------- save

// Save makes the database match the snapshot, atomically.
//
// Every table is upserted from the snapshot and then swept of anything the
// snapshot no longer holds, so a delete in memory becomes a delete on disk
// without a separate code path that could be forgotten. One transaction, so a
// reader never sees half a turn.
func (b *pgBackend) Save(ctx context.Context, s *snapshot) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("SAVE_FAILED: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := b.saveSessions(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveProfiles(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveTasks(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveConsent(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveApprovals(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveSignals(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveAccounts(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveSignIns(ctx, tx, s); err != nil {
		return err
	}
	if err := b.saveMeta(ctx, tx, s); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("SAVE_FAILED: commit: %w", err)
	}
	return nil
}

func (b *pgBackend) saveSessions(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.Sessions))
	for id, ses := range s.Sessions {
		doc, err := json.Marshal(ses)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: session %s: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, subject_id, role, locale, intent, created_at, updated_at, doc)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (id) DO UPDATE SET
			  subject_id = EXCLUDED.subject_id, role = EXCLUDED.role,
			  locale = EXCLUDED.locale, intent = EXCLUDED.intent,
			  updated_at = EXCLUDED.updated_at, doc = EXCLUDED.doc`,
			ses.ID, ses.SubjectID, string(ses.Role), ses.Locale, ses.Intent,
			ses.CreatedAt, ses.UpdatedAt, doc); err != nil {
			return fmt.Errorf("SAVE_FAILED: session %s: %w", id, err)
		}
		keep = append(keep, id)
	}
	return sweep(ctx, tx, "sessions", "id", keep)
}

func (b *pgBackend) saveProfiles(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.Profiles))
	for id, p := range s.Profiles {
		doc, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: profile %s: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO profiles (subject_id, doc, updated_at) VALUES ($1,$2,now())
			ON CONFLICT (subject_id) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`,
			id, doc); err != nil {
			return fmt.Errorf("SAVE_FAILED: profile %s: %w", id, err)
		}
		keep = append(keep, id)
	}
	return sweep(ctx, tx, "profiles", "subject_id", keep)
}

func (b *pgBackend) saveTasks(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.Tasks))
	for id, t := range s.Tasks {
		doc, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: task %s: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO case_tasks (id, subject_id, status, doc, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET
			  subject_id = EXCLUDED.subject_id, status = EXCLUDED.status,
			  doc = EXCLUDED.doc, updated_at = EXCLUDED.updated_at`,
			t.ID, t.SubjectID, string(t.Status), doc, t.CreatedAt, t.UpdatedAt); err != nil {
			return fmt.Errorf("SAVE_FAILED: task %s: %w", id, err)
		}
		keep = append(keep, id)
	}
	return sweep(ctx, tx, "case_tasks", "id", keep)
}

func (b *pgBackend) saveConsent(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	// The row's identity is the PAIR (subject, scope), so the sweep is a join
	// against two parallel arrays rather than against one concatenated key.
	//
	// The concatenation is what was tried first and it is worth recording why it
	// cannot work: joining the two halves with a NUL separator produces a string
	// postgres will not accept, because NUL is the one byte its text type
	// forbids. The whole Save transaction then failed — and since Save is one
	// transaction, that took the session, profile and task writes down with it
	// on every subsequent turn. Nothing surfaced it except a round-trip test,
	// because a failed write only logs.
	keepSubjects := make([]string, 0)
	keepScopes := make([]string, 0)
	for subject, scopes := range s.Consent {
		for scope, g := range scopes {
			var at any
			if !g.GrantedAt.IsZero() {
				at = g.GrantedAt
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO consent (subject_id, scope, granted, granted_at, note)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (subject_id, scope) DO UPDATE SET
				  granted = EXCLUDED.granted, granted_at = EXCLUDED.granted_at,
				  note = EXCLUDED.note`,
				subject, string(scope), g.Granted, at, g.Note); err != nil {
				return fmt.Errorf("SAVE_FAILED: consent %s/%s: %w", subject, scope, err)
			}
			keepSubjects = append(keepSubjects, subject)
			keepScopes = append(keepScopes, string(scope))
		}
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM consent c WHERE NOT EXISTS (
		  SELECT 1 FROM unnest($1::text[], $2::text[]) AS k(subject_id, scope)
		  WHERE k.subject_id = c.subject_id AND k.scope = c.scope
		)`, keepSubjects, keepScopes)
	if err != nil {
		return fmt.Errorf("SAVE_FAILED: consent sweep: %w", err)
	}
	return nil
}

func (b *pgBackend) saveApprovals(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.Approvals))
	for id, a := range s.Approvals {
		doc, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: approval %s: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO approvals (id, session_id, decided, doc, created_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (id) DO UPDATE SET
			  session_id = EXCLUDED.session_id, decided = EXCLUDED.decided, doc = EXCLUDED.doc`,
			a.ID, a.SessionID, a.Decided, doc, a.CreatedAt); err != nil {
			return fmt.Errorf("SAVE_FAILED: approval %s: %w", id, err)
		}
		keep = append(keep, id)
	}
	return sweep(ctx, tx, "approvals", "id", keep)
}

func (b *pgBackend) saveSignals(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	for i, sig := range s.Signals {
		doc, err := json.Marshal(sig)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: signal %d: %w", i, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO demand_signals (ord, doc) VALUES ($1,$2)
			ON CONFLICT (ord) DO UPDATE SET doc = EXCLUDED.doc`, i, doc); err != nil {
			return fmt.Errorf("SAVE_FAILED: signal %d: %w", i, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM demand_signals WHERE ord >= $1`, len(s.Signals)); err != nil {
		return fmt.Errorf("SAVE_FAILED: signal sweep: %w", err)
	}
	return nil
}

func (b *pgBackend) saveAccounts(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.Accounts))
	for key, a := range s.Accounts {
		doc, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: account %s: %w", key, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO accounts (username, subject_id, doc, created_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (username) DO UPDATE SET
			  subject_id = EXCLUDED.subject_id, doc = EXCLUDED.doc`,
			key, a.SubjectID, doc, a.CreatedAt); err != nil {
			return fmt.Errorf("SAVE_FAILED: account %s: %w", key, err)
		}
		keep = append(keep, key)
	}
	return sweep(ctx, tx, "accounts", "username", keep)
}

func (b *pgBackend) saveSignIns(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	keep := make([]string, 0, len(s.SignIns))
	for hash, si := range s.SignIns {
		if _, err := tx.Exec(ctx, `
			INSERT INTO signins (token_hash, username, expires_at) VALUES ($1,$2,$3)
			ON CONFLICT (token_hash) DO UPDATE SET
			  username = EXCLUDED.username, expires_at = EXCLUDED.expires_at`,
			hash, si.Username, si.ExpiresAt); err != nil {
			return fmt.Errorf("SAVE_FAILED: signin: %w", err)
		}
		keep = append(keep, hash)
	}
	// The sweep is what makes signing out and expiry real on disk: the store
	// deletes the entry from memory, and this removes the row.
	return sweep(ctx, tx, "signins", "token_hash", keep)
}

func (b *pgBackend) saveMeta(ctx context.Context, tx pgx.Tx, s *snapshot) error {
	for key, val := range map[string]any{
		metaSeq:           s.Seq,
		metaLegacyAdopted: s.LegacyAdopted,
	} {
		raw, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("SAVE_FAILED: meta %s: %w", key, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO meta (key, value) VALUES ($1,$2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, raw); err != nil {
			return fmt.Errorf("SAVE_FAILED: meta %s: %w", key, err)
		}
	}
	return nil
}

// sweep removes rows whose key the snapshot no longer holds.
//
// `<> ALL($1)` against an empty array is TRUE for every row, which is the
// wanted behaviour — an emptied collection empties the table — but it is also
// exactly the kind of expression that reads as a no-op, so it is written once
// here rather than at eight call sites.
func sweep(ctx context.Context, tx pgx.Tx, table, keyCol string, keep []string) error {
	sql := fmt.Sprintf(`DELETE FROM %s WHERE %s <> ALL($1)`, table, keyCol)
	if _, err := tx.Exec(ctx, sql, keep); err != nil {
		return fmt.Errorf("SAVE_FAILED: %s sweep: %w", table, err)
	}
	return nil
}
