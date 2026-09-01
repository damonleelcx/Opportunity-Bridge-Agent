package store

// Accounts and sign-ins.
//
// Why an account exists at all: every other record in this store is keyed by a
// subject id, and until now nothing said which subject belonged to whom. The
// consequence was not theoretical — GET /api/sessions/{id} answered for any id,
// and ids are sequential, so one loop read every visitor's transcript, profile
// and consents. See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
//
// Vocabulary, deliberately: this product already calls one conversation a
// SESSION. The thing that proves who you are is a SIGN-IN, never a session, in
// code, in the interface and in the documentation. One word, one concept.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Account is a person's durable identity. SubjectID is minted once, at sign-up,
// and is what binds the profile, the tasks and the consents that already hang
// off a subject to a person who can come back tomorrow.
type Account struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	SubjectID    string    `json:"subject_id"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at,omitempty"`

	// Email is how somebody gets back in after forgetting a password, and it is
	// the ONLY route back: this service holds no phone number and has no support
	// desk that can identify a person. EmailVerified is what separates an address
	// somebody typed from one they have proved they can read — see
	// internal/store/emailtokens.go.
	//
	// Both are optional on purpose. Accounts created before this existed have
	// neither, and they keep working: an address is required of NEW sign-ups, not
	// retrospectively of people already using the service.
	// See docs/bugfix/2026-08-31-email-verification-and-reset.md
	Email         string    `json:"email,omitempty"`
	EmailVerified bool      `json:"email_verified,omitempty"`
	EmailSetAt    time.Time `json:"email_set_at,omitempty"`

	// AlsoOwns exists for one reason: the data that predates accounts. Twelve
	// visitors left twelve subjects behind, and re-keying them onto one subject
	// would mean merging twelve conflicting profiles into one and losing most of
	// them. Adopting them as a list keeps every record intact and still gives
	// them exactly one owner.
	AlsoOwns []string `json:"also_owns,omitempty"`

	// SpendDay and SpentTokens are this account's model spending for ONE UTC
	// day, and they are a pair: the day is what makes the count mean anything.
	// A count whose day is not today reads as zero, which is the entire reset
	// mechanism — there is no nightly sweep to fail. See spend.go.
	//
	// Both are omitempty and both default to zero, so accounts that predate the
	// cap simply start at nothing on their next turn. No backfill, no migration.
	SpendDay    string `json:"spend_day,omitempty"`
	SpentTokens int64  `json:"spent_tokens,omitempty"`
}

// Owns reports whether this account may act on a subject.
func (a *Account) Owns(subjectID string) bool {
	if a == nil || subjectID == "" {
		return false
	}
	if a.SubjectID == subjectID {
		return true
	}
	for _, s := range a.AlsoOwns {
		if s == subjectID {
			return true
		}
	}
	return false
}

// SignIn is one browser holding a valid cookie. It is keyed in the store by the
// token's hash, never by the token: a state file that leaks must not hand
// anybody a working credential.
type SignIn struct {
	Username  string    `json:"username"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NormaliseUsername is the single definition of when two names are the same
// name. Case-insensitive, trimmed: somebody who signs up as "Damon" and signs in
// as "damon" is one person, and — more to the point — must not be able to become
// two accounts that look identical in the interface.
func NormaliseUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// ErrUsernameTaken is returned rather than a generic failure so the interface
// can say which field to change.
var ErrUsernameTaken = fmt.Errorf("USERNAME_TAKEN")

// CreateAccount mints an account and the subject its records will hang off.
func (s *Store) CreateAccount(username, passwordHash string) (*Account, error) {
	key := NormaliseUsername(username)
	if key == "" {
		return nil, fmt.Errorf("USERNAME_EMPTY")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.s.Accounts[key]; exists {
		return nil, ErrUsernameTaken
	}
	a := &Account{
		Username:     strings.TrimSpace(username),
		PasswordHash: passwordHash,
		SubjectID:    s.nextID("sub"),
		CreatedAt:    time.Now().UTC(),
	}
	s.s.Accounts[key] = a
	s.persist()
	return cloneAccount(a), nil
}

// Account looks one up by name. The bool is false for "no such account", which
// the caller must treat identically to a wrong password.
func (s *Store) Account(username string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.s.Accounts[NormaliseUsername(username)]
	if !ok {
		return nil, false
	}
	return cloneAccount(a), true
}

// StartSignIn records a new sign-in against the token's hash and returns it.
func (s *Store) StartSignIn(username, tokenHash string, ttl time.Duration) *SignIn {
	now := time.Now().UTC()
	si := &SignIn{Username: NormaliseUsername(username), IssuedAt: now, ExpiresAt: now.Add(ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.SignIns[tokenHash] = si
	s.pruneSignInsLocked(now)
	if a, ok := s.s.Accounts[si.Username]; ok {
		a.LastSeenAt = now
	}
	s.persist()
	return si
}

// AccountBySignIn resolves a cookie's token hash to the account it belongs to.
// An expired sign-in resolves to nothing: expiry is checked here, once, rather
// than at each call site where it could be forgotten.
func (s *Store) AccountBySignIn(tokenHash string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	si, ok := s.s.SignIns[tokenHash]
	if !ok || time.Now().UTC().After(si.ExpiresAt) {
		return nil, false
	}
	a, ok := s.s.Accounts[si.Username]
	if !ok {
		return nil, false
	}
	return cloneAccount(a), true
}

// EndSignIn forgets one sign-in. Signing out has to actually revoke, not just
// clear the cookie on this machine.
func (s *Store) EndSignIn(tokenHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.s.SignIns[tokenHash]; !ok {
		return
	}
	delete(s.s.SignIns, tokenHash)
	s.persist()
}

// pruneSignInsLocked drops expired records. Callers hold the write lock.
// Without this the map only ever grows, and every entry in it is a credential.
func (s *Store) pruneSignInsLocked(now time.Time) {
	for h, si := range s.s.SignIns {
		if now.After(si.ExpiresAt) {
			delete(s.s.SignIns, h)
		}
	}
}

// AdoptOrphanedSubjects gives every subject with no owner to one account, once.
//
// This is the migration for data that predates accounts. It is deliberately
// additive: nothing is deleted, nothing is re-keyed, and the marker means a
// restart cannot sweep up a second time. It returns how many subjects were
// adopted so the caller can say so in a log rather than doing it silently.
func (s *Store) AdoptOrphanedSubjects(username string) (int, error) {
	key := NormaliseUsername(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s.LegacyAdopted {
		return 0, nil
	}
	owner, ok := s.s.Accounts[key]
	if !ok {
		return 0, fmt.Errorf("ACCOUNT_NOT_FOUND: no account %q to adopt the pre-account data", username)
	}
	owned := map[string]bool{}
	for _, a := range s.s.Accounts {
		owned[a.SubjectID] = true
		for _, sub := range a.AlsoOwns {
			owned[sub] = true
		}
	}
	orphans := map[string]bool{}
	for _, ses := range s.s.Sessions {
		if ses.SubjectID != "" && !owned[ses.SubjectID] {
			orphans[ses.SubjectID] = true
		}
	}
	list := make([]string, 0, len(orphans))
	for sub := range orphans {
		list = append(list, sub)
	}
	sort.Strings(list) // deterministic, so two runs of the same state agree
	owner.AlsoOwns = append(owner.AlsoOwns, list...)
	s.s.LegacyAdopted = true
	s.persist()
	return len(list), nil
}

func cloneAccount(a *Account) *Account {
	c := *a
	c.AlsoOwns = append([]string(nil), a.AlsoOwns...)
	return &c
}
