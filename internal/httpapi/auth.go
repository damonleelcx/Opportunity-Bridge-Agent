package httpapi

// Sign-up, sign-in, sign-out, and the gate that stands in front of everything
// else.
//
// What this closes: before it existed, this service had no authentication at
// all. GET /api/sessions listed every visitor's conversation, GET
// /api/sessions/{id} returned a stranger's whole transcript, profile, tasks and
// consents, and ids are sequential — so nothing had to be guessed. The write
// side was open too: anyone could continue somebody else's conversation on your
// model budget, delete their profile, or change their consents.
// See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
//
// Vocabulary: a SESSION here is one conversation, as it has always been. The
// thing that proves who you are is a SIGN-IN. The two words are not
// interchangeable anywhere in this codebase.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

const (
	signInCookie = "oba_signin"
	// minPasswordRunes counts runes, not bytes: a nine-character Chinese
	// passphrase is not a short password, and a byte-length rule would tell its
	// author otherwise.
	minPasswordRunes = 10
	maxUsernameRunes = 40
)

// accountKey is the request-context key for the signed-in account. Unexported
// and of a private type so nothing outside this package can forge one.
type accountKey struct{}

// accountFor returns the account this request is acting as, if any.
func accountFor(r *http.Request) *store.Account {
	a, _ := r.Context().Value(accountKey{}).(*store.Account)
	return a
}

func withAccount(ctx context.Context, a *store.Account) context.Context {
	return context.WithValue(ctx, accountKey{}, a)
}

// openPaths are reachable without a sign-in, and the list is deliberately
// short:
//
//   - /api/health carries the Kubernetes probes. A gated health check means a
//     pod that can never become ready.
//   - the sign-in /api/auth endpoints, because a sign-in page that requires you
//     to be signed in is a locked room with the key inside. The password-reset
//     pair and the GET that a confirmation link points at are open for the same
//     reason, and are safe for reasons of their own — see the cases below.
//
// Everything else — every byte of data, every call that spends model tokens —
// is behind the gate.
func isOpenPath(method, path string) bool {
	switch path {
	case "/api/health":
		return method == http.MethodGet
	case "/api/auth/signup", "/api/auth/signin", "/api/auth/signout", "/api/auth/me":
		return true
	case "/api/auth/verify":
		// GET only. The link in a confirmation mail is clicked by somebody who
		// may not be signed in on that device — a phone mail app opens its own
		// browser. POST /api/auth/verify is the RESEND, which needs an account
		// and is therefore NOT listed here.
		return method == http.MethodGet
	case "/api/auth/reset", "/api/auth/reset/confirm":
		// Open by necessity: somebody who cannot sign in is precisely who needs
		// these. What protects them is not the gate — it is that the request
		// endpoint answers identically whether or not the address exists, and
		// that the confirm endpoint needs a single-use token nobody can guess.
		return method == http.MethodPost
	}
	// The static shell is public. It has to be, to render the sign-in form at
	// all, and it is already published in an open-source repository — there is
	// nothing in it that a sign-in would be protecting.
	return !strings.HasPrefix(path, "/api/")
}

// gate resolves the sign-in cookie and refuses anything that needs an account
// and does not have one.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acct, ok := s.resolveAccount(r); ok {
			r = r.WithContext(withAccount(r.Context(), acct))
		} else if !isOpenPath(r.Method, r.URL.Path) {
			writeErr(w, http.StatusUnauthorized, "SIGNIN_REQUIRED",
				"You need to be signed in to do that.",
				"Sign in, or create an account with an invite code.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resolveAccount(r *http.Request) (*store.Account, bool) {
	c, err := r.Cookie(signInCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	return s.Store.AccountBySignIn(store.HashSignInToken(c.Value))
}

// ---------------------------------------------------------------- handlers

func (s *Server) signUp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		InviteCode string `json:"invite_code"`
		Email      string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID",
			"The request body was not valid JSON.", "Send a username, a password and an invite code.")
		return
	}
	// Sign-up closed is the state a deployment lands in when nobody configured
	// invite codes. Refusing is the safe direction: the alternative is an open
	// registration form on a public endpoint attached to a paid model key.
	if len(s.Cfg.InviteCodes) == 0 {
		writeErr(w, http.StatusForbidden, "SIGNUP_CLOSED",
			"This deployment is not accepting new accounts.",
			"Ask whoever runs it for an account, or set OBA_INVITE_CODES to open sign-up.")
		return
	}
	if !s.inviteAccepted(body.InviteCode) {
		writeErr(w, http.StatusForbidden, "INVITE_INVALID",
			"That invite code is not valid.", "Check the code you were given, including its case.")
		return
	}
	if n := len([]rune(store.NormaliseUsername(body.Username))); n == 0 || n > maxUsernameRunes {
		writeErr(w, http.StatusBadRequest, "USERNAME_INVALID",
			"A username is required, up to 40 characters.", "Pick a shorter name.")
		return
	}
	if len([]rune(body.Password)) < minPasswordRunes {
		writeErr(w, http.StatusBadRequest, "PASSWORD_TOO_SHORT",
			"A password needs at least 10 characters.",
			"Length beats punctuation. A short sentence you will remember works well.")
		return
	}
	// An address is required of NEW accounts and is not asked of existing ones.
	//
	// Why required at all: it is the ONLY route back into an account whose
	// password is forgotten. There is no support desk here that could identify
	// somebody, and losing the account loses the profile, the tracked tasks and
	// the consents hanging off its subject. Why not retrospective: people
	// already using the service must not be stopped mid-errand to supply one —
	// they get an "add an address" control instead, and are told plainly what
	// they cannot do until they use it.
	// See docs/bugfix/2026-08-31-email-verification-and-reset.md
	if !mailer.Valid(body.Email) {
		writeErr(w, http.StatusBadRequest, "EMAIL_INVALID",
			"An email address is required, and that one does not look like one.",
			"It is the only way back in if you forget your password. Check for a missing @ or a typo in the domain.")
		return
	}
	hash, err := store.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED",
			"The account could not be created.", "Try again; if it keeps failing, report it.")
		return
	}
	acct, err := s.Store.CreateAccount(body.Username, hash)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			writeErr(w, http.StatusConflict, "USERNAME_TAKEN",
				"That username is already in use.", "Pick another one.")
			return
		}
		writeErr(w, http.StatusBadRequest, "ACCOUNT_NOT_CREATED", err.Error(), "Check the username and try again.")
		return
	}
	// The address is attached AFTER the account exists, so a clash on it cannot
	// leave a half-made account behind. A clash here is not fatal to the sign-up:
	// they have an account and are signed in, and the settings panel is where
	// they sort the address out.
	emailErr := s.Store.SetEmail(acct.Username, body.Email)
	if emailErr != nil {
		s.Log.Warn("account created without its address",
			"code", "SIGNUP_EMAIL_NOT_SET", "username", acct.Username, "error", emailErr.Error())
	}
	s.Log.Info("account created", "code", "ACCOUNT_CREATED", "username", acct.Username)
	s.issueSignIn(w, acct)
	acct, _ = s.Store.Account(acct.Username)
	out := meFor(acct)
	if emailErr != nil {
		out["email_error"] = emailErr.Error()
	} else if sent, err := s.sendVerification(r, acct.Username, body.Email); err != nil {
		// Never fatal. A relay outage must not stop somebody signing up; the
		// resend control is in the settings panel.
		s.Log.Warn("confirmation mail failed at sign-up",
			"code", "VERIFY_MAIL_FAILED", "error", err.Error())
		out["verification_sent"] = false
	} else {
		out["verification_sent"] = sent
	}
	writeJSON(w, out)
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID",
			"The request body was not valid JSON.", "Send a username and a password.")
		return
	}
	if !s.attemptAllowed(body.Username) {
		writeErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS",
			"Too many failed attempts for that account.", "Wait a minute and try again.")
		return
	}
	acct, ok := s.Store.Account(body.Username)
	if !ok {
		// Still pay for a verification, so the time this takes does not answer
		// "does this person have an account here" — which, for a service about
		// unemployment and benefits, is itself a disclosure.
		store.SpendVerificationTime(body.Password)
		s.attemptFailed(body.Username)
		s.writeSignInRefused(w)
		return
	}
	if !store.VerifyPassword(acct.PasswordHash, body.Password) {
		s.attemptFailed(body.Username)
		s.writeSignInRefused(w)
		return
	}
	s.attemptSucceeded(body.Username)
	s.issueSignIn(w, acct)
	writeJSON(w, meFor(acct))
}

// writeSignInRefused is one message for both "no such account" and "wrong
// password". Telling them apart is a free account-existence oracle.
//
// The remedy tracks what this deployment can actually do. It used to say flatly
// that there is no password reset — true when it was written, false since
// docs/bugfix/2026-08-31-email-verification-and-reset.md added one. So the
// person who most needed the reset was told, by the service itself, not to look
// for it: the 忘了密码 control is right there on the same form, and the sentence
// under the error said it did not exist.
//
// Branching on s.Mail is safe here, and the distinction matters: it is a
// property of the DEPLOYMENT, identical for every username, so it adds nothing
// to the oracle this function exists to close. Branching on anything about the
// account — whether it exists, whether it has an address, whether that address
// is confirmed — would reopen exactly that hole, which is why the wording below
// describes how reset works in general and never this account in particular.
// See docs/bugfix/2026-08-31-signin-error-denied-a-reset-that-exists.md
func (s *Server) writeSignInRefused(w http.ResponseWriter) {
	remedy := "Check both. This deployment cannot send mail, so there is no password reset here — " +
		"ask whoever sent you the invite link for a new invite."
	if s.Mail != nil {
		remedy = "Check both. If you have forgotten the password, use the password reset on this " +
			"form; it mails a link to the account's confirmed address."
	}
	writeErr(w, http.StatusUnauthorized, "SIGNIN_REFUSED",
		"That username and password do not match.", remedy)
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	// Revoked server-side, not just cleared in this browser: a cookie deleted
	// on one machine is still a live credential everywhere it was copied.
	if c, err := r.Cookie(signInCookie); err == nil && c.Value != "" {
		s.Store.EndSignIn(store.HashSignInToken(c.Value))
	}
	http.SetCookie(w, s.signInCookie("", -time.Hour))
	writeJSON(w, map[string]any{"signed_out": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	acct := accountFor(r)
	if acct == nil {
		writeErr(w, http.StatusUnauthorized, "SIGNIN_REQUIRED",
			"Nobody is signed in.", "Sign in, or create an account with an invite code.")
		return
	}
	writeJSON(w, meFor(acct))
}

func meFor(a *store.Account) map[string]any {
	return map[string]any{
		"username": a.Username, "subject_id": a.SubjectID,
		// The interface needs all three states, not two: no address at all (an
		// account from before this existed), an address awaiting confirmation,
		// and a confirmed one. Collapsing the first two would tell somebody with
		// no address to "check their inbox".
		"email": a.Email, "email_verified": a.EmailVerified,
	}
}

func (s *Server) issueSignIn(w http.ResponseWriter, a *store.Account) {
	token, hash, err := store.NewSignInToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SIGNIN_TOKEN_FAILED",
			"Could not start a sign-in.", "Try again.")
		return
	}
	ttl := s.Cfg.SignInTTL
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	s.Store.StartSignIn(a.Username, hash, ttl)
	http.SetCookie(w, s.signInCookie(token, ttl))
}

// signInCookie carries the flags that make the cookie worth having.
//
// SameSite=Lax is also this application's CSRF defence, and is why there is no
// token machinery here: Lax withholds the cookie from cross-site POST entirely,
// and every mutating endpoint takes application/json, which is not a "simple"
// request and so cannot be sent cross-origin without CORS — which is not
// enabled. Weakening SameSite would silently remove that protection, so do not.
func (s *Server) signInCookie(value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     signInCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().UTC().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	}
}

func (s *Server) inviteAccepted(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	// Constant-time against every configured code: a byte-by-byte comparison
	// that returns early leaks the prefix of a valid code to a patient caller.
	ok := false
	for _, want := range s.Cfg.InviteCodes {
		if subtle.ConstantTimeCompare([]byte(code), []byte(want)) == 1 {
			ok = true
		}
	}
	return ok
}

// ------------------------------------------------------- attempt throttling

// Password verification is deliberately expensive (600k PBKDF2 iterations), and
// on a one-core pod that makes the sign-in endpoint a CPU exhaustion target as
// well as a guessing target. The ingress rate-limits per IP; this limits per
// username, which is what a distributed guesser spreads across IPs.
//
// In memory on purpose: it protects a single-replica deployment, it is worth
// nothing after a restart anyway, and persisting it would put a write on the
// path of every failed guess.
type attemptLimiter struct {
	mu      sync.Mutex
	failed  map[string]int
	blocked map[string]time.Time
}

const (
	maxFailedAttempts = 8
	attemptCooldown   = time.Minute
)

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{failed: map[string]int{}, blocked: map[string]time.Time{}}
}

// limiter is built on first use so a zero-value Server — which every test
// constructs directly — is usable without a constructor.
func (s *Server) limiter() *attemptLimiter {
	s.limiterOnce.Do(func() { s.lim = newAttemptLimiter() })
	return s.lim
}

func (s *Server) attemptAllowed(username string) bool {
	l := s.limiter()
	key := store.NormaliseUsername(username)
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[key]
	if !ok {
		return true
	}
	if time.Now().UTC().After(until) {
		delete(l.blocked, key)
		delete(l.failed, key)
		return true
	}
	return false
}

func (s *Server) attemptFailed(username string) {
	l := s.limiter()
	key := store.NormaliseUsername(username)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failed[key]++
	if l.failed[key] >= maxFailedAttempts {
		l.blocked[key] = time.Now().UTC().Add(attemptCooldown)
	}
}

func (s *Server) attemptSucceeded(username string) {
	l := s.limiter()
	key := store.NormaliseUsername(username)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failed, key)
	delete(l.blocked, key)
}
