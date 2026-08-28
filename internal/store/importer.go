package store

// One-time import of the JSON state file into postgres.
//
// This exists because the records it moves are real people's messages, and the
// alternative to moving them is abandoning them: after the switch the file is
// no longer read, so anything left in it is gone as far as anybody using the
// service is concerned.
//
// It is deliberately not automatic. An import that ran on startup would be an
// import that ran again on the next restart, against whatever the database had
// become in the meantime — and because Save() makes the database match the
// snapshot, a second run from a stale file would not merely duplicate, it would
// SWEEP AWAY everything written since. So it is an explicit command, it refuses
// to touch a database that already holds anything, and it says what it moved.

import (
	"context"
	"fmt"
	"log/slog"
)

// ImportCounts reports what was moved, so the caller can log it rather than
// saying "done" and leaving the operator to go and count rows themselves.
type ImportCounts struct {
	Sessions int
	Profiles int
	Tasks    int
	Consents int
	Signals  int
	Accounts int
	SignIns  int
	Seq      int
}

func (c ImportCounts) String() string {
	return fmt.Sprintf("sessions=%d profiles=%d tasks=%d consents=%d signals=%d accounts=%d sign_ins=%d seq=%d",
		c.Sessions, c.Profiles, c.Tasks, c.Consents, c.Signals, c.Accounts, c.SignIns, c.Seq)
}

// ImportFileToPostgres copies a JSON state file into an EMPTY postgres database.
//
// Refusing a non-empty target is the whole safety property. There is no --force:
// the only reason to overwrite a populated database here would be to discard
// what is in it, and that is a restore from a backup, not an import.
func ImportFileToPostgres(ctx context.Context, path, dsn string, log *slog.Logger) (ImportCounts, error) {
	var counts ImportCounts
	if log == nil {
		log = slog.Default()
	}

	src := New(path, log)
	src.mu.RLock()
	if len(src.s.Sessions) == 0 && len(src.s.Accounts) == 0 && len(src.s.Profiles) == 0 {
		src.mu.RUnlock()
		return counts, fmt.Errorf("IMPORT_SOURCE_EMPTY: %q holds no sessions, accounts or profiles; "+
			"check the path — importing nothing over nothing is not a migration", path)
	}
	src.mu.RUnlock()

	dst, err := OpenPostgres(ctx, dsn, log)
	if err != nil {
		return counts, err
	}
	defer dst.Close()

	dst.mu.Lock()
	defer dst.mu.Unlock()
	if n := len(dst.s.Sessions) + len(dst.s.Accounts) + len(dst.s.Profiles); n > 0 {
		return counts, fmt.Errorf("IMPORT_TARGET_NOT_EMPTY: the database already holds %d records; "+
			"refusing, because this import replaces the whole contents and would discard them", n)
	}

	src.mu.RLock()
	dst.s = src.s // the snapshot is the whole state; there is nothing else to carry
	src.mu.RUnlock()

	counts = ImportCounts{
		Sessions: len(dst.s.Sessions),
		Profiles: len(dst.s.Profiles),
		Tasks:    len(dst.s.Tasks),
		Signals:  len(dst.s.Signals),
		Accounts: len(dst.s.Accounts),
		SignIns:  len(dst.s.SignIns),
		Seq:      dst.s.Seq,
	}
	for _, scopes := range dst.s.Consent {
		counts.Consents += len(scopes)
	}

	// persist() logs a failure and continues, which is right for a turn and
	// wrong for a migration: "imported" must not be printed over a write that
	// did not happen. So this calls the backend directly and returns the error.
	if err := dst.db.Save(ctx, &dst.s); err != nil {
		return counts, fmt.Errorf("IMPORT_FAILED: nothing was written: %w", err)
	}
	log.Info("state imported into postgres", "code", "STATE_IMPORTED",
		"source", path, "counts", counts.String())
	return counts, nil
}
