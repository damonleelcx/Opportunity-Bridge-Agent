package store

// Spending counters: what an account has spent today, and what the whole
// deployment has spent today.
//
// Why this exists at all. Every turn spends model tokens against a paid key,
// and sign-up needs no invite code, so anybody who finds the address can hold
// an account. The stopping conditions in agent.Budget bound ONE turn — eight
// iterations of sixteen thousand output tokens is a legitimate turn — and
// nothing bounded how many turns. This is the thing that does.
//
// Why TWO counters rather than one. A per-account allowance bounds one account,
// and accounts are free and unlimited, so on its own it multiplies by however
// many somebody registers. The deployment counter is the circuit breaker that
// bounds all of them at once. Neither is redundant: the first stops one person
// draining the budget, the second stops five hundred throwaway accounts doing
// it between them.
// See docs/bugfix/2026-09-01-per-account-and-deployment-spend-caps.md
//
// Why this is not "memory" in the sense the package doc means. The three kinds
// above — task state, history, long-term memory — are all things the agent
// knows ABOUT A PERSON and reasons with. These two numbers are operational
// accounting: nothing in a turn ever reads them, they are never shown to the
// model, and they carry no meaning once the day rolls over.
//
// Why the day is stored beside the count. The reset is a COMPARISON, not an
// event: a counter whose day is not today reads as zero. That is the whole
// mechanism. The alternative — a nightly sweep that zeroes every account — is a
// background job that can fail silently, and its failure mode is everybody
// locked out of a service that looks healthy.

import "time"

// spendDayOf is the UTC calendar day a counter belongs to.
//
// UTC rather than local time, and this is the deliberate part: the reset moment
// has to be the same for the counter, the operator reading /api/health, and the
// sentence shown to somebody who has run out. A server-local day would drift
// from all three the first time the host's zone or DST changed.
func spendDayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// DeploymentSpend is the whole service's running total for one UTC day.
//
// It lives in the snapshot rather than in a table of its own because it is a
// scalar that belongs to the store as a whole, which is exactly what the `meta`
// table already holds (see metaSeq, metaLegacyAdopted in pg.go). A table for
// two fields that are overwritten in place would buy nothing.
type DeploymentSpend struct {
	Day    string `json:"day,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
}

// tokensOn reports the total for `day`, which is zero on any other day.
func (d DeploymentSpend) tokensOn(day string) int64 {
	if d.Day != day {
		return 0
	}
	return d.Tokens
}

// SpentToday reports what this account and this deployment have spent so far
// today, in tokens.
//
// Both come back from ONE acquisition of the lock, on purpose. Read separately
// they can straddle a midnight rollover or another turn's write and return two
// numbers that were never true at the same moment — and the caller is about to
// make one decision out of both of them.
//
// An unknown username reports zero rather than failing: the caller has already
// resolved the account from a sign-in cookie, so "not found" here would mean the
// account was deleted mid-request, and refusing that person's turn on the way to
// discovering it helps nobody.
func (s *Store) SpentToday(username string) (account, deployment int64) {
	day := spendDayOf(time.Now())
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.s.Accounts[NormaliseUsername(username)]; ok && a.SpendDay == day {
		account = a.SpentTokens
	}
	return account, s.s.Spend.tokensOn(day)
}

// AddSpend records the tokens a turn cost, against the account that spent them
// and against the deployment total.
//
// One call, one lock, one write. Two separate calls would persist the whole
// snapshot twice per turn for two fields that always change together, and could
// leave the deployment total counting a turn the account did not.
//
// A zero or negative total is ignored: a turn that failed before reaching the
// model has nothing to charge for, and no path here should ever refund.
//
// The new totals are returned from INSIDE the lock. Callers want to know
// whether this turn is the one that crossed a ceiling, and re-reading afterwards
// to find out would race a concurrent turn: two turns could each read a total
// that already includes the other, and both report the crossing, or neither.
func (s *Store) AddSpend(username string, tokens int64) (account, deployment int64) {
	if tokens <= 0 {
		return s.SpentToday(username)
	}
	day := spendDayOf(time.Now())
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stale day means a new day: overwrite rather than add. This is the reset,
	// and it happens on the write path as well as the read path — otherwise the
	// first turn of a new day would be added to yesterday's total.
	if s.s.Spend.Day != day {
		s.s.Spend = DeploymentSpend{Day: day}
	}
	s.s.Spend.Tokens += tokens
	if a, ok := s.s.Accounts[NormaliseUsername(username)]; ok {
		if a.SpendDay != day {
			a.SpendDay, a.SpentTokens = day, 0
		}
		a.SpentTokens += tokens
		account = a.SpentTokens
	}
	s.persist()
	return account, s.s.Spend.Tokens
}
