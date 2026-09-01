package store_test

// Fences over the spending counters themselves. The gate that reads them is
// fenced separately in internal/httpapi/spend_test.go; these cover the thing it
// reads — the counting, and the reset the counting depends on.
// See docs/bugfix/2026-09-01-per-account-and-deployment-spend-caps.md

import (
	"io"
	"log/slog"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func spendStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func account(t *testing.T, s *store.Store, name string) {
	t.Helper()
	if _, err := s.CreateAccount(name, "hash"); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

// The two counters move together and stay separate: one turn charges its own
// account and the deployment, and nobody else's account.
func TestAddSpendChargesTheAccountAndTheDeployment(t *testing.T) {
	s := spendStore(t)
	account(t, s, "ada")
	account(t, s, "grace")

	acct, deployment := s.AddSpend("ada", 100)
	if acct != 100 || deployment != 100 {
		t.Fatalf("first charge returned account=%d deployment=%d, want 100/100", acct, deployment)
	}
	acct, deployment = s.AddSpend("grace", 30)
	if acct != 30 {
		t.Errorf("grace's own total is %d, want 30", acct)
	}
	if deployment != 130 {
		t.Errorf("deployment total is %d, want 130 — the two accounts are not adding up", deployment)
	}
	if ada, _ := s.SpentToday("ada"); ada != 100 {
		t.Errorf("ada was charged %d for a turn grace ran", ada)
	}
}

// The reset is a comparison, not an event: a count stamped with any day but
// today reads as zero. Nothing sweeps, so if this stops holding, every account
// stays capped forever from the first day it hits the limit.
func TestSpendFromAnEarlierDayReadsAsZero(t *testing.T) {
	s := spendStore(t)
	account(t, s, "ada")
	s.AddSpend("ada", 500)

	s.BackdateSpendForTest("ada", 1)

	acct, deployment := s.SpentToday("ada")
	if acct != 0 {
		t.Errorf("yesterday's account spending reads as %d today, want 0: the allowance never refills", acct)
	}
	if deployment != 0 {
		t.Errorf("yesterday's deployment spending reads as %d today, want 0: the service never comes back", deployment)
	}
}

// The first charge of a new day REPLACES the stale total rather than adding to
// it. Without this the read path would report zero while the write path kept
// accumulating on top of yesterday, and the cap would bite early for the rest of
// time — the nastiest shape of this bug, because reads look correct.
func TestFirstChargeOfANewDayStartsFromZero(t *testing.T) {
	s := spendStore(t)
	account(t, s, "ada")
	s.AddSpend("ada", 500)
	s.BackdateSpendForTest("ada", 1)

	acct, deployment := s.AddSpend("ada", 7)
	if acct != 7 {
		t.Errorf("the new day's first charge totals %d, want 7: yesterday is being carried forward", acct)
	}
	if deployment != 7 {
		t.Errorf("the new day's deployment total is %d, want 7", deployment)
	}
}

// Nothing is charged for a turn that cost nothing, and nothing is ever
// refunded. A negative charge would be a way to hand an account free allowance.
func TestAddSpendIgnoresNothingAndRefusesToRefund(t *testing.T) {
	s := spendStore(t)
	account(t, s, "ada")
	s.AddSpend("ada", 100)

	for _, n := range []int64{0, -1, -1000} {
		if acct, _ := s.AddSpend("ada", n); acct != 100 {
			t.Errorf("charging %d moved the total to %d; it must stay at 100", n, acct)
		}
	}
}

// An account that does not exist does not panic and does not invent a total.
// The caller has already resolved the account from a cookie, so this is the
// deleted-mid-request case, and a turn is a bad place to discover it.
func TestSpendOnAnUnknownAccountIsQuiet(t *testing.T) {
	s := spendStore(t)
	acct, deployment := s.AddSpend("nobody", 50)
	if acct != 0 {
		t.Errorf("an unknown account reports %d spent", acct)
	}
	// The deployment still paid for it, because the model still ran.
	if deployment != 50 {
		t.Errorf("deployment total is %d, want 50: the tokens were spent whoever spent them", deployment)
	}
	if a, _ := s.SpentToday("nobody"); a != 0 {
		t.Errorf("reading an unknown account reports %d", a)
	}
}
