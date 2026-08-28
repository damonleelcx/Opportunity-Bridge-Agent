package store_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// Fences for docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md

func persistentStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	return store.New(path, slog.New(slog.NewTextHandler(io.Discard, nil))), path
}

func TestPasswordVerifiesOnlyAgainstItself(t *testing.T) {
	h, err := store.HashPassword("a passphrase worth typing")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !store.VerifyPassword(h, "a passphrase worth typing") {
		t.Error("the right password did not verify")
	}
	if store.VerifyPassword(h, "a passphrase worth typin") {
		t.Error("a near miss verified")
	}
	if store.VerifyPassword(h, "") {
		t.Error("an empty password verified")
	}
	// The hash must not be the password, nor a bare digest of it: both are what
	// a leaked state file would hand straight to whoever read it.
	if strings.Contains(h, "a passphrase worth typing") {
		t.Error("the password appears in its own hash")
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$") {
		t.Errorf("hash does not record its own parameters, so the cost can never be raised: %q", h)
	}
}

func TestTwoHashesOfOnePasswordDiffer(t *testing.T) {
	a, _ := store.HashPassword("a passphrase worth typing")
	b, _ := store.HashPassword("a passphrase worth typing")
	if a == b {
		t.Error("no salt: identical passwords produce identical hashes, so one table answers for every account")
	}
}

func TestMalformedHashVerifiesNothing(t *testing.T) {
	for _, bad := range []string{"", "$", "pbkdf2-sha256$0$$", "plain$1$x$y", "pbkdf2-sha256$abc$x$y"} {
		if store.VerifyPassword(bad, "") || store.VerifyPassword(bad, "anything") {
			t.Errorf("a malformed hash %q accepted a password", bad)
		}
	}
}

func TestSignInTokenIsNeverPersisted(t *testing.T) {
	st, path := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	if _, err := st.CreateAccount("damon", hash); err != nil {
		t.Fatalf("account: %v", err)
	}
	token, tokenHash, err := store.NewSignInToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	st.StartSignIn("damon", tokenHash, time.Hour)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	// The cookie value is a live credential. A state file, a backup of one, or
	// a support bundle containing one must not be a set of working sign-ins.
	if strings.Contains(string(b), token) {
		t.Error("the raw sign-in token was written to the state file")
	}
	if !strings.Contains(string(b), tokenHash) {
		t.Error("the sign-in was not recorded under its hash, so nothing can resolve it")
	}
	if _, ok := st.AccountBySignIn(tokenHash); !ok {
		t.Error("a freshly issued sign-in does not resolve")
	}
	if _, ok := st.AccountBySignIn(store.HashSignInToken("some other token")); ok {
		t.Error("an unrelated token resolved to an account")
	}
}

// A sign-in has to stop working when it expires.
//
// Written against a short but REAL lifetime on purpose. Handing StartSignIn a
// negative TTL looks like the same test and is not: the record is pruned at
// write time, so the lookup then fails because nothing is there, and the expiry
// check itself is never exercised. It has to be present and stale.
func TestExpiredSignInDoesNotResolve(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	_, _ = st.CreateAccount("damon", hash)
	_, tokenHash, _ := store.NewSignInToken()

	st.StartSignIn("damon", tokenHash, 60*time.Millisecond)
	if _, ok := st.AccountBySignIn(tokenHash); !ok {
		t.Fatal("a live sign-in did not resolve; this test would then prove nothing")
	}
	time.Sleep(120 * time.Millisecond)
	if _, ok := st.AccountBySignIn(tokenHash); ok {
		t.Error("an expired sign-in still resolves; the cookie lifetime means nothing")
	}
}

// Separately: a record that is already stale when written is not kept at all.
// Every entry in that map is a credential, so it must not grow without bound.
func TestExpiredSignInsArePrunedOnWrite(t *testing.T) {
	st, path := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	_, _ = st.CreateAccount("damon", hash)
	_, stale, _ := store.NewSignInToken()
	st.StartSignIn("damon", stale, -time.Minute)

	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), stale) {
		t.Error("an already-expired sign-in was kept; the credential map only ever grows")
	}
}

func TestUsernamesAreOnePersonRegardlessOfCase(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	if _, err := st.CreateAccount("Damon", hash); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := st.CreateAccount("damon", hash); err == nil {
		t.Error("two accounts differing only by case were created; they look identical in the interface")
	}
	if _, ok := st.Account("DAMON"); !ok {
		t.Error("signing in with a different case did not find the account")
	}
}

func TestSessionListIsScopedToTheOwner(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	mine, _ := st.CreateAccount("mine", hash)
	theirs, _ := st.CreateAccount("theirs", hash)

	for _, a := range []*store.Account{mine, theirs} {
		ses := st.CreateSession(domain.RoleResident, a.SubjectID, "en")
		_ = st.MutateSession(ses.ID, func(s *store.Session) error {
			s.History = append(s.History, store.Turn{Role: "user", Text: "for " + a.Username})
			return nil
		})
	}
	rows := st.SessionSummariesFor(mine)
	if len(rows) != 1 {
		t.Fatalf("owner sees %d conversations, want exactly their own", len(rows))
	}
	if !strings.Contains(rows[0].Title, "mine") {
		t.Errorf("the wrong conversation was listed: %q", rows[0].Title)
	}
	if got := st.SessionSummariesFor(nil); got != nil {
		t.Errorf("a nil account was shown %d conversations", len(got))
	}
}

// The pre-account data has to end up with exactly one owner, without anything
// being deleted or re-keyed — and a restart must not sweep a second time.
func TestAdoptingLegacySubjectsIsIdempotentAndAdditive(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")

	orphan := st.CreateSession(domain.RoleResident, "", "en") // no account: pre-account data
	st.SetConsent(orphan.SubjectID, domain.ConsentStoreProfile, true, "before accounts")

	demo, _ := st.CreateAccount("demo", hash)
	n, err := st.AdoptOrphanedSubjects("demo")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted %d subjects, want the one orphan", n)
	}
	demo, _ = st.Account("demo")
	if !demo.Owns(orphan.SubjectID) {
		t.Error("the orphaned subject has no owner, so nobody can read, correct or delete it")
	}
	if g := st.Consent(orphan.SubjectID, domain.ConsentStoreProfile); !g.Granted {
		t.Error("adoption disturbed the record it adopted")
	}

	again, err := st.AdoptOrphanedSubjects("demo")
	if err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if again != 0 {
		t.Errorf("adoption ran twice and took %d more subjects; every restart would keep sweeping", again)
	}
	demo, _ = st.Account("demo")
	if len(demo.AlsoOwns) != 1 {
		t.Errorf("the owned list grew on a second run: %v", demo.AlsoOwns)
	}
}

// The owned-set check is not housekeeping. Without it, adoption hands the demo
// account ownership of every REAL account's subject — turning a migration for
// abandoned data into the exposure it was meant to close.
func TestAdoptionNeverTakesASubjectThatAlreadyHasAnOwner(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")

	orphan := st.CreateSession(domain.RoleResident, "", "en")
	real, _ := st.CreateAccount("realperson", hash)
	st.CreateSession(domain.RoleResident, real.SubjectID, "en")
	demo, _ := st.CreateAccount("demo", hash)
	st.CreateSession(domain.RoleResident, demo.SubjectID, "en")

	n, err := st.AdoptOrphanedSubjects("demo")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted %d subjects, want only the one with no owner", n)
	}
	got, _ := st.Account("demo")
	for _, taken := range got.AlsoOwns {
		if taken == real.SubjectID {
			t.Error("adoption gave the demo account a real account's subject, and with it their whole record")
		}
		if taken == demo.SubjectID {
			t.Error("the demo account adopted its own subject; the owned list is now self-referential")
		}
		if taken != orphan.SubjectID {
			t.Errorf("adoption took an unexpected subject: %q", taken)
		}
	}
}

func TestAdoptingRefusesAnAccountThatDoesNotExist(t *testing.T) {
	st, _ := persistentStore(t)
	if _, err := st.AdoptOrphanedSubjects("nobody"); err == nil {
		t.Error("adoption silently did nothing for a misspelled account name")
	}
}

// A stranger's subject must not be reachable through an account just because
// the account exists.
func TestOwnsIsExactlyTheOwnedSubjects(t *testing.T) {
	a := &store.Account{SubjectID: "sub_0001", AlsoOwns: []string{"sub_0009"}}
	for _, want := range []string{"sub_0001", "sub_0009"} {
		if !a.Owns(want) {
			t.Errorf("account does not own %s", want)
		}
	}
	for _, no := range []string{"", "sub_0002", "sub_000", "sub_00019"} {
		if a.Owns(no) {
			t.Errorf("account claims to own %q", no)
		}
	}
}

// The state file is the only place accounts live. It has to survive a restart,
// or every deploy signs everybody out and loses their conversations.
func TestAccountsSurviveAReload(t *testing.T) {
	st, path := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	made, _ := st.CreateAccount("damon", hash)

	reopened := store.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, ok := reopened.Account("damon")
	if !ok {
		t.Fatal("the account did not survive a reload; every deploy would sign everybody out")
	}
	if got.SubjectID != made.SubjectID {
		t.Errorf("the subject changed across a reload: %q then %q", made.SubjectID, got.SubjectID)
	}
	if !store.VerifyPassword(got.PasswordHash, "a passphrase worth typing") {
		t.Error("the password hash did not survive a reload")
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["accounts"]; !ok {
		t.Error("accounts are not in the state file under the key the schema says")
	}
}

// The marker's own job, separate from the owned-set check that makes a repeat
// run a no-op: once adoption has happened, a subject that appears LATER must
// not be swept up. Otherwise a restart quietly hands new records to the demo
// account for ever.
func TestAdoptionDoesNotSweepUpSubjectsCreatedAfterwards(t *testing.T) {
	st, _ := persistentStore(t)
	hash, _ := store.HashPassword("a passphrase worth typing")
	before := st.CreateSession(domain.RoleResident, "", "en")
	_, _ = st.CreateAccount("demo", hash)
	if _, err := st.AdoptOrphanedSubjects("demo"); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	after := st.CreateSession(domain.RoleResident, "", "en")
	n, err := st.AdoptOrphanedSubjects("demo")
	if err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if n != 0 {
		t.Errorf("adoption ran again and took %d subjects", n)
	}
	demo, _ := st.Account("demo")
	if !demo.Owns(before.SubjectID) {
		t.Error("the pre-existing orphan lost its owner")
	}
	if demo.Owns(after.SubjectID) {
		t.Error("a subject created after the cutover was adopted; adoption is a migration, not a policy")
	}
}
