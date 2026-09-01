package store

// Test-only surface. This file is compiled into the test binary and nothing
// else, which is where a "wipe everything" helper belongs: exported from the
// package proper it would be one mistaken call away from being the last thing
// that ever happens to somebody's records.

import (
	"context"
	"fmt"
	"time"
)

// BackdateSpendForTest moves the DAY STAMP on the spending counters back,
// leaving the counts alone.
//
// Tests need to cross a midnight boundary, and moving the process clock instead
// would be testing Go's time package rather than the reset — which is a
// comparison between a stored day and today's, and nothing else. Backdating the
// stamp exercises exactly that comparison.
//
// Test-only for the obvious reason: production has no business rewriting when
// somebody's spending happened.
func (s *Store) BackdateSpendForTest(username string, days int) {
	day := spendDayOf(time.Now().AddDate(0, 0, -days))
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.s.Accounts[NormaliseUsername(username)]; ok {
		a.SpendDay = day
	}
	s.s.Spend.Day = day
}

// TruncateAllForTest empties every table. It names them explicitly rather than
// discovering them from the catalogue, so that a table added to the schema
// without a thought for the tests fails loudly here instead of quietly leaving
// rows behind and making the next test's assertions depend on the previous
// test's data.
func (s *Store) TruncateAllForTest(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("TruncateAllForTest called on a store with no database")
	}
	_, err := s.db.pool.Exec(ctx, `
		TRUNCATE sessions, profiles, case_tasks, consent,
		         approvals, demand_signals, accounts, signins, meta`)
	if err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	// The in-memory snapshot still holds what was just deleted; the caller
	// reopens the store, so this is only here to make the intent explicit.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s = newSnapshot()
	return nil
}
