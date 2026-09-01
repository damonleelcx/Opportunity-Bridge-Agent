package store_test

// These run against a REAL postgres, never a stand-in.
//
// The schema they exercise is the one the application ships and applies at
// start, because a test that builds its own simplified tables proves that the
// simplification works. Set OBA_TEST_DATABASE_URL, or run `make test-pg`, which
// starts one in a container and points this at it.
//
// When the variable is unset these tests SKIP, which is the dangerous state: a
// skipped test is green. The skip message says so, `make test-pg` exists so
// there is no excuse not to run them, and the release checklist names them.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OBA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OBA_TEST_DATABASE_URL is not set: the postgres backend is NOT covered by this run. " +
			"Run `make test-pg` to cover it.")
	}
	return dsn
}

// freshPG returns a store on an empty database. It truncates rather than
// dropping so that every test runs against the schema the migrations produced.
func freshPG(t *testing.T) *store.Store {
	t.Helper()
	dsn := pgDSN(t)
	st, err := store.OpenPostgres(context.Background(), dsn, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.TruncateAllForTest(context.Background()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	st.Close()

	st, err = store.OpenPostgres(context.Background(), dsn, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// reopen closes a store and opens a new one on the same database, which is the
// only way to prove something was actually written rather than merely
// remembered: every read in this package is served from memory.
func reopen(t *testing.T, st *store.Store) *store.Store {
	t.Helper()
	st.Close()
	next, err := store.OpenPostgres(context.Background(), pgDSN(t), nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(next.Close)
	return next
}

// The whole point of the change: an account survives the process that made it.
func TestAccountAndSignInSurviveARestart(t *testing.T) {
	st := freshPG(t)

	hash, err := store.HashPassword("a-real-password")
	if err != nil {
		t.Fatal(err)
	}
	acct, err := st.CreateAccount("Damon", hash)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st.StartSignIn("Damon", "token-hash-1", time.Hour)

	st = reopen(t, st)

	got, ok := st.Account("damon")
	if !ok {
		t.Fatal("the account did not survive a restart")
	}
	if got.SubjectID != acct.SubjectID {
		t.Errorf("subject = %q, want %q — a changed subject orphans every record it owns",
			got.SubjectID, acct.SubjectID)
	}
	if !store.VerifyPassword(got.PasswordHash, "a-real-password") {
		t.Error("the password hash did not survive intact")
	}
	if _, ok := st.AccountBySignIn("token-hash-1"); !ok {
		t.Error("the sign-in did not survive; every signed-in person would be signed out by a deploy")
	}
}

// Signing out has to reach the database. If it only reached memory, the cookie
// would start working again after a restart.
func TestSignOutRemovesTheRowNotJustTheMemory(t *testing.T) {
	st := freshPG(t)
	hash, _ := store.HashPassword("pw")
	if _, err := st.CreateAccount("out", hash); err != nil {
		t.Fatal(err)
	}
	st.StartSignIn("out", "token-hash-2", time.Hour)
	st.EndSignIn("token-hash-2")

	st = reopen(t, st)
	if _, ok := st.AccountBySignIn("token-hash-2"); ok {
		t.Fatal("a signed-out token still resolves after a restart")
	}
}

// A conversation, its profile, its consent and its tasks are one person's
// record. They have to come back together or not at all.
func TestARecordSurvivesWhole(t *testing.T) {
	st := freshPG(t)
	ses := st.CreateSession(domain.RoleResident, "sub_x", "zh-CN")
	if err := st.MutateSession(ses.ID, func(s *store.Session) error {
		s.History = append(s.History, store.Turn{Role: "user", Text: "我在深圳", At: time.Now().UTC()})
		s.Intent = "individual_pathway"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st.SaveProfile(domain.Profile{SubjectID: "sub_x", City: "深圳"})
	st.SetConsent("sub_x", domain.ConsentStoreProfile, true, "asked in the interface")
	task := st.CreateTask(domain.CaseTask{SubjectID: "sub_x", Title: "打 12333", Domain: "employment"})

	st = reopen(t, st)

	back, ok := st.Session(ses.ID)
	if !ok {
		t.Fatal("the session did not survive")
	}
	if len(back.History) != 1 || back.History[0].Text != "我在深圳" {
		t.Errorf("the turns did not survive: %+v", back.History)
	}
	if back.Intent != "individual_pathway" {
		t.Errorf("intent = %q", back.Intent)
	}
	if got := st.Profile("sub_x"); got.City != "深圳" {
		t.Errorf("profile city = %q, want 深圳 — non-ASCII must round-trip", got.City)
	}
	if g := st.Consent("sub_x", domain.ConsentStoreProfile); !g.Granted {
		t.Error("consent did not survive; the person would be asked again")
	}
	tasks := st.TasksFor("sub_x")
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Errorf("tasks did not survive: %+v", tasks)
	}
}

// Forgetting must reach the database too — this is a deletion somebody asked
// for, and a row that outlives it is the failure that matters most here.
func TestForgetProfileDeletesTheRow(t *testing.T) {
	st := freshPG(t)
	st.SaveProfile(domain.Profile{SubjectID: "sub_forget", City: "成都"})
	st = reopen(t, st)
	if got := st.Profile("sub_forget"); got.City != "成都" {
		t.Fatalf("setup did not persist: %+v", got)
	}
	st.ForgetProfile("sub_forget")

	st = reopen(t, st)
	if got := st.Profile("sub_forget"); got.City != "" {
		t.Fatalf("a forgotten profile came back after a restart: %+v", got)
	}
}

// The id counter is shared across every prefix and must not restart, or the
// next session would be handed an id that already belongs to somebody.
func TestIDSequenceDoesNotRestart(t *testing.T) {
	st := freshPG(t)
	first := st.CreateSession(domain.RoleResident, "s", "zh-CN")
	st = reopen(t, st)
	second := st.CreateSession(domain.RoleResident, "s", "zh-CN")
	if first.ID == second.ID {
		t.Fatalf("the id counter restarted: both sessions are %q", first.ID)
	}
}

// The spending counters survive a restart, on BOTH sides — the account's total
// (a field inside accounts.doc) and the deployment's (the `deployment_spend` key
// in meta). Neither needed DDL, which is exactly why this fence matters: a field
// that is never written to the database still reads back correctly from memory,
// so nothing else here would notice. What it would cost in production is a
// daily ceiling that silently resets on every pod roll.
// See docs/bugfix/2026-09-01-per-account-and-deployment-spend-caps.md
func TestSpendCountersSurviveARestart(t *testing.T) {
	st := freshPG(t)
	if _, err := st.CreateAccount("spender", "hash"); err != nil {
		t.Fatalf("create: %v", err)
	}
	st.AddSpend("spender", 1234)

	st = reopen(t, st)

	acct, deployment := st.SpentToday("spender")
	if acct != 1234 {
		t.Errorf("the account's spending came back as %d, want 1234: a restart hands out a fresh allowance", acct)
	}
	if deployment != 1234 {
		t.Errorf("the deployment total came back as %d, want 1234: a pod roll resets the circuit breaker", deployment)
	}
}

// Every migration is applied on every start, so applying them twice has to be
// the same as applying them once. This is the fence on that claim.
func TestOpeningTwiceAppliesTheSchemaTwiceWithoutFailing(t *testing.T) {
	dsn := pgDSN(t)
	for i := range 3 {
		st, err := store.OpenPostgres(context.Background(), dsn, nil)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		st.Close()
	}
}

// A database that cannot be reached must stop the process, not start it empty.
// Starting empty would answer every signed-up person as a stranger, and the
// first write would then sweep their rows away.
func TestAnUnreachableDatabaseRefusesToStart(t *testing.T) {
	pgDSN(t) // skip in the same conditions as the rest of the file
	// A short deadline, because OpenPostgres now RETRIES a refused connection
	// for up to a minute (a new pod can be refused by a NetworkPolicy that
	// permits it, until the CNI catches up). The property under test is that it
	// eventually gives up rather than starting, not how long it waits.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := store.OpenPostgres(ctx,
		"postgres://nobody:nobody@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1", nil)
	if err == nil {
		t.Fatal("an unreachable database started anyway; the operator would believe their data is safe")
	}
}

// A fresh store must be able to issue a link on its very first sign-up.
//
// Every map in the snapshot has to be built in BOTH places that produce one —
// the empty state and the loader — and a nil map here does not fail at startup,
// it panics on the first write. The first write is the first person who signs
// up. See docs/bugfix/2026-08-31-email-verification-and-reset.md
func TestFreshStoreCanIssueAnEmailToken(t *testing.T) {
	s := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s.CreateAccount("first", "hash"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetEmail("first", "First@Example.com"); err != nil {
		t.Fatalf("set email: %v", err)
	}
	tok, err := s.IssueEmailToken("first", store.PurposeVerifyEmail, "first@example.com", store.VerifyTokenTTL)
	if err != nil || tok == "" {
		t.Fatalf("issue: %v", err)
	}
	// And the address was normalised on the way in, or two spellings become two
	// accounts and a reset link becomes ambiguous.
	a, ok := s.Account("first")
	if !ok || a.Email != "first@example.com" {
		t.Errorf("email = %q, want the normalised form", a.Email)
	}
	if a.EmailVerified {
		t.Error("a freshly set address is verified; nobody has proved they can read it")
	}
}
