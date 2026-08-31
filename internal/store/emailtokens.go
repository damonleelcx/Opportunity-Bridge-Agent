package store

// Email addresses on accounts, and the single-use links sent to them.
//
// Two things live here because they are one mechanism: an address is worth
// nothing until somebody has proved they can read mail at it, and the only way
// to prove that is a link that expires and works once.

import (
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
)

// TokenPurpose is what a link is for. It is checked by EXACT MATCH at every use
// site, never by excluding the purposes a caller does not want.
//
// Why that is spelled out: a sibling system's session layer refused purposes by
// DENYLIST (`if purpose == console { reject }`), so the day a third purpose was
// added it authenticated as something it was never meant to be. A verification
// link that is accepted as a password reset is exactly that failure, and it is
// silent — the link works, and the wrong thing happens.
// See docs/bugfix/2026-08-31-email-verification-and-reset.md
type TokenPurpose string

const (
	PurposeVerifyEmail   TokenPurpose = "verify_email"
	PurposeResetPassword TokenPurpose = "reset_password"
)

// EmailToken is one single-use link. Keyed in the store by the token's HASH, for
// the same reason sign-ins are: a leaked state file must not contain anything
// that can be used.
type EmailToken struct {
	Username  string       `json:"username"`
	Purpose   TokenPurpose `json:"purpose"`
	Email     string       `json:"email"`
	IssuedAt  time.Time    `json:"issued_at"`
	ExpiresAt time.Time    `json:"expires_at"`
	UsedAt    time.Time    `json:"used_at,omitempty"`
}

// Token lifetimes. A reset link is a credential and is short; a verification
// link is not, and a day is the difference between "I will do it tonight" and
// asking for another one.
const (
	VerifyTokenTTL = 24 * time.Hour
	ResetTokenTTL  = 1 * time.Hour
)

var (
	ErrEmailTaken   = fmt.Errorf("EMAIL_TAKEN")
	ErrNoSuchToken  = fmt.Errorf("TOKEN_UNKNOWN")
	ErrTokenExpired = fmt.Errorf("TOKEN_EXPIRED")
	ErrTokenUsed    = fmt.Errorf("TOKEN_USED")
)

// SetEmail attaches an address to an account and marks it unverified.
//
// Changing an address always clears the verified flag, including when somebody
// "changes" it to the one already there. That is not pedantry: an account taken
// over for five minutes could otherwise be pointed at an attacker's address and
// keep a verified badge it never earned for that address.
func (s *Store) SetEmail(username, email string) error {
	key := NormaliseUsername(username)
	addr := mailer.Address(email)
	if !mailer.Valid(addr) {
		return fmt.Errorf("EMAIL_INVALID: %q is not a deliverable address", email)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.s.Accounts[key]
	if !ok {
		return fmt.Errorf("ACCOUNT_NOT_FOUND: no account %q", username)
	}
	// One address, one account. Without this, two people can register the same
	// address and a reset link becomes ambiguous — and whichever account the
	// lookup happened to return is the one that gets taken over.
	for k, other := range s.s.Accounts {
		if k != key && mailer.Address(other.Email) == addr {
			return ErrEmailTaken
		}
	}
	a.Email = addr
	a.EmailVerified = false
	a.EmailSetAt = time.Now().UTC()
	s.persist()
	return nil
}

// AccountByEmail finds the account holding a VERIFIED address.
//
// Unverified addresses are deliberately invisible here. A password reset that
// honoured an unverified address would let anybody claim a stranger's address on
// a throwaway account and then reset... nothing, since the address is on their
// own account — but it would also mail a stranger about an account that is not
// theirs, repeatedly, from a service they never signed up to.
func (s *Store) AccountByEmail(email string) (*Account, bool) {
	addr := mailer.Address(email)
	if addr == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.s.Accounts {
		if a.EmailVerified && mailer.Address(a.Email) == addr {
			return cloneAccount(a), true
		}
	}
	return nil, false
}

// IssueEmailToken mints a single-use link for one purpose and stores its hash.
//
// Any token outstanding for the SAME account and purpose is dropped first.
// Otherwise asking for a second reset link leaves the first one live, and the
// number of working credentials in somebody's mailbox grows with every press of
// a button that looks like it does nothing.
func (s *Store) IssueEmailToken(username string, p TokenPurpose, email string, ttl time.Duration) (string, error) {
	token, hash, err := NewSignInToken()
	if err != nil {
		return "", err
	}
	key := NormaliseUsername(username)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, t := range s.s.EmailTokens {
		if t.Username == key && t.Purpose == p {
			delete(s.s.EmailTokens, h)
		}
	}
	s.pruneEmailTokensLocked(now)
	s.s.EmailTokens[hash] = &EmailToken{
		Username: key, Purpose: p, Email: mailer.Address(email),
		IssuedAt: now, ExpiresAt: now.Add(ttl),
	}
	s.persist()
	return token, nil
}

// RedeemEmailToken consumes a token for ONE purpose and returns it.
//
// The purpose is compared by equality against the one the caller asked for. A
// caller cannot pass "anything but X", and there is no variant of this that
// returns the token without checking.
func (s *Store) RedeemEmailToken(token string, want TokenPurpose) (*EmailToken, error) {
	hash := HashSignInToken(token)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.s.EmailTokens[hash]
	if !ok {
		return nil, ErrNoSuchToken
	}
	if t.Purpose != want {
		// Not "expired", not "unknown": this token exists and is for something
		// else. Returning it would be the denylist bug.
		return nil, fmt.Errorf("TOKEN_WRONG_PURPOSE: this link is for %q, not %q", t.Purpose, want)
	}
	if !t.UsedAt.IsZero() {
		return nil, ErrTokenUsed
	}
	if now.After(t.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	t.UsedAt = now
	out := *t
	s.persist()
	return &out, nil
}

// MarkEmailVerified records that somebody proved they can read mail at the
// address, and only for the address the link was issued for. An account whose
// address changed after the link was sent does NOT become verified by clicking
// the old one.
func (s *Store) MarkEmailVerified(username, email string) error {
	key := NormaliseUsername(username)
	addr := mailer.Address(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.s.Accounts[key]
	if !ok {
		return fmt.Errorf("ACCOUNT_NOT_FOUND: no account %q", username)
	}
	if mailer.Address(a.Email) != addr {
		return fmt.Errorf("EMAIL_CHANGED: the link was issued for a different address")
	}
	a.EmailVerified = true
	s.persist()
	return nil
}

// SetPassword replaces the hash and ENDS EVERY SIGN-IN for that account.
//
// The sign-out is the point, not a courtesy. Somebody resets a password because
// they lost control of the account; leaving the attacker's cookie working means
// the reset changed nothing they care about.
func (s *Store) SetPassword(username, passwordHash string) (signInsEnded int, err error) {
	key := NormaliseUsername(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.s.Accounts[key]
	if !ok {
		return 0, fmt.Errorf("ACCOUNT_NOT_FOUND: no account %q", username)
	}
	a.PasswordHash = passwordHash
	for h, si := range s.s.SignIns {
		if si.Username == key {
			delete(s.s.SignIns, h)
			signInsEnded++
		}
	}
	// Any outstanding reset link is spent too: the new password is set, and a
	// second link in a mailbox is a second way in.
	for h, t := range s.s.EmailTokens {
		if t.Username == key && t.Purpose == PurposeResetPassword {
			delete(s.s.EmailTokens, h)
		}
	}
	s.persist()
	return signInsEnded, nil
}

// pruneEmailTokensLocked drops what is spent or stale. Callers hold the write
// lock. Like sign-ins, every entry here is a credential, so the map must not
// only grow.
func (s *Store) pruneEmailTokensLocked(now time.Time) {
	for h, t := range s.s.EmailTokens {
		if now.After(t.ExpiresAt) || (!t.UsedAt.IsZero() && now.Sub(t.UsedAt) > time.Hour) {
			delete(s.s.EmailTokens, h)
		}
	}
}

// EmailSummary is what the interface is told about an account's address. The
// address itself is included because it is the person's own, and seeing which
// address a reset would go to is most of the value of showing it at all.
type EmailSummary struct {
	Email    string `json:"email,omitempty"`
	Verified bool   `json:"email_verified"`
}

func summariseEmail(a *Account) EmailSummary {
	if a == nil {
		return EmailSummary{}
	}
	return EmailSummary{Email: a.Email, Verified: a.EmailVerified}
}

var _ = strings.TrimSpace

// EmailTokenInfo reads a token WITHOUT consuming it.
//
// It exists for exactly one caller: telling "a link scanner already spent this
// verification link, and the address is confirmed" apart from "this link means
// nothing". It deliberately returns no way to act — the caller gets who it
// belonged to, and must still redeem through RedeemEmailToken to change
// anything. See docs/bugfix/2026-08-31-email-verification-and-reset.md
func (s *Store) EmailTokenInfo(token string) (*EmailToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.s.EmailTokens[HashSignInToken(token)]
	if !ok {
		return nil, false
	}
	out := *t
	return &out, true
}
