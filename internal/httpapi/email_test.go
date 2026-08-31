package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
)

// fakeMail records what would have gone out. The BODY matters as much as the
// count: a link is the credential, and a test that only counts messages cannot
// tell a working reset from one that mailed an empty link.
type fakeMail struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (f *fakeMail) Name() string { return "fake" }
func (f *fakeMail) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeMail) last() (mailer.Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return mailer.Message{}, false
	}
	return f.sent[len(f.sent)-1], true
}
func (f *fakeMail) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func mailServer(t *testing.T, m *fakeMail) *httptest.Server {
	t.Helper()
	return newServerTweaking(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}},
		func(c *config.Config) { c.PublicOrigin = "https://jobs.example.test" },
		func(s *httpapi.Server) { s.Mail = m })
}

// tokenIn pulls the single-use token out of the link in a message body. Reading
// the body rather than reaching into the store is deliberate: this is what a
// person actually receives, and a link that is malformed there is broken no
// matter how good the store looks.
func tokenIn(t *testing.T, body, param string) string {
	t.Helper()
	i := strings.Index(body, param+"=")
	if i < 0 {
		t.Fatalf("no %s= link in the message:\n%s", param, body)
	}
	rest := body[i+len(param)+1:]
	if j := strings.IndexAny(rest, "\r\n "); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		t.Fatalf("the %s link carries an empty token:\n%s", param, body)
	}
	return rest
}

// signUpWith creates an account through the real endpoint, so every test below
// exercises the path a person actually takes.
func signUpWith(t *testing.T, srv *httptest.Server, username, email string) *http.Client {
	t.Helper()
	c := anon(t, srv)
	res := postAs(t, c, srv.URL+"/api/auth/signup", map[string]string{
		"username": username, "password": "a-long-enough-passphrase",
		"invite_code": "let-me-in", "email": email,
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("sign-up answered %d: %s", res.StatusCode, b)
	}
	return c
}

// A new account cannot be created without an address, because that address is
// the only route back into it. Everything hanging off the subject — the profile,
// the tracked tasks, the consents — is lost with a forgotten password otherwise,
// and there is no support desk here that could identify anybody.
// See docs/bugfix/2026-08-31-email-verification-and-reset.md
func TestSignUpRequiresAnAddressItCanReach(t *testing.T) {
	srv := mailServer(t, &fakeMail{})
	defer srv.Close()
	for _, bad := range []string{"", "not-an-address", "no@domain", "two@@at.example", "sp ace@x.example"} {
		res := postAs(t, anon(t, srv), srv.URL+"/api/auth/signup", map[string]string{
			"username": "u" + bad, "password": "a-long-enough-passphrase",
			"invite_code": "let-me-in", "email": bad,
		})
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("sign-up with email %q answered %d, want 400", bad, res.StatusCode)
		}
	}
}

// Signing up sends the confirmation, and the address starts UNCONFIRMED. An
// address somebody typed is not one they have proved they can read.
func TestSignUpSendsAConfirmationAndDoesNotTrustTheAddressYet(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	c := signUpWith(t, srv, "newcomer", "Newcomer@Example.Test")

	if m.count() != 1 {
		t.Fatalf("%d messages sent at sign-up, want 1", m.count())
	}
	msg, _ := m.last()
	if msg.To != "newcomer@example.test" {
		t.Errorf("sent to %q; the address must be normalised, or two spellings become two accounts", msg.To)
	}
	if !strings.Contains(msg.Body, "https://jobs.example.test/api/auth/verify?token=") {
		t.Errorf("the link is not absolute against the configured origin:\n%s", msg.Body)
	}

	var me map[string]any
	res := getAs(t, c, srv.URL+"/api/auth/me")
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if me["email"] != "newcomer@example.test" {
		t.Errorf("me.email = %v", me["email"])
	}
	if me["email_verified"] != false {
		t.Error("the address is confirmed before anybody opened the link")
	}
}

// The confirmation link works once, and confirms only what it was issued for.
func TestConfirmationLinkWorksOnceAndOnlyForItsOwnPurpose(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	c := signUpWith(t, srv, "reader", "reader@example.test")
	msg, _ := m.last()
	token := tokenIn(t, msg.Body, "token")

	// A confirmation token must NOT be spendable as a password reset. This is
	// the denylist failure: a purpose check written as "anything but X" accepts
	// a link the person was never asked to treat as a credential.
	res := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset/confirm",
		map[string]string{"token": token, "password": "another-long-passphrase"})
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("a verification link was accepted as a password reset")
	}

	// srv.Client() trusts the test certificate; the redirect is not followed so
	// the Location header can be asserted.
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r1, err := client.Get(srv.URL + "/api/auth/verify?token=" + token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusSeeOther {
		t.Errorf("verify answered %d, want a redirect a browser can follow", r1.StatusCode)
	}
	if loc := r1.Header.Get("Location"); !strings.Contains(loc, "verified=ok") {
		t.Errorf("redirect = %q, want verified=ok", loc)
	}

	var me map[string]any
	res = getAs(t, c, srv.URL+"/api/auth/me")
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if me["email_verified"] != true {
		t.Fatal("the address is still unconfirmed after the link was opened")
	}
}

// 🔴 The reset endpoint must answer identically whether or not the address is
// registered.
//
// The membership of this service is "people who lost their job". An endpoint
// that says "no such address" for one input and "check your inbox" for another
// is a membership oracle anybody can query, and it needs no account to use.
// See docs/bugfix/2026-08-31-email-verification-and-reset.md
func TestResetRequestNeverRevealsWhetherAnAccountExists(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	c := signUpWith(t, srv, "known", "known@example.test")
	confirm(t, srv, c, m)

	type answer struct {
		status int
		body   string
	}
	read := func(email string) answer {
		res := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset", map[string]string{"email": email})
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return answer{res.StatusCode, string(b)}
	}
	registered := read("known@example.test")
	stranger := read("nobody@example.test")

	if registered.status != http.StatusOK || stranger.status != http.StatusOK {
		t.Errorf("statuses differ or are not 200: registered=%d stranger=%d",
			registered.status, stranger.status)
	}
	if registered.body != stranger.body {
		t.Errorf("the bodies differ, which answers 'is this person registered here':\n  %s\n  %s",
			registered.body, stranger.body)
	}
	// And nothing was mailed to the stranger's address.
	for _, msg := range m.sent {
		if msg.To == "nobody@example.test" {
			t.Error("a message was sent to an address with no account")
		}
	}
}

// An UNCONFIRMED address must not receive a reset link either. Otherwise anybody
// can put a stranger's address on a throwaway account and have this service mail
// that stranger about an account they never opened.
func TestResetIgnoresUnconfirmedAddresses(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	signUpWith(t, srv, "unconfirmed", "unconfirmed@example.test") // never opens the link
	before := m.count()

	res := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset",
		map[string]string{"email": "unconfirmed@example.test"})
	res.Body.Close()
	if m.count() != before {
		msg, _ := m.last()
		t.Errorf("a reset link was sent to an unconfirmed address: %q", msg.Subject)
	}
}

// The whole way back in, and the thing it must take with it: every sign-in.
func TestResetSetsThePasswordAndSignsEveryDeviceOut(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	c := signUpWith(t, srv, "forgetful", "forgetful@example.test")
	confirm(t, srv, c, m)

	// The account is signed in on this client — stand in for the attacker's
	// browser, or the phone left on a bus.
	if res := getAs(t, c, srv.URL+"/api/auth/me"); res.StatusCode != http.StatusOK {
		res.Body.Close()
		t.Fatal("precondition: the client should be signed in")
	} else {
		res.Body.Close()
	}

	res := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset",
		map[string]string{"email": "forgetful@example.test"})
	res.Body.Close()
	msg, ok := m.last()
	if !ok || !strings.Contains(msg.Body, "https://jobs.example.test/app?reset=") {
		t.Fatalf("no usable reset link was sent:\n%+v", msg)
	}
	token := tokenIn(t, msg.Body, "reset")

	res = postAs(t, anon(t, srv), srv.URL+"/api/auth/reset/confirm",
		map[string]string{"token": token, "password": "a-brand-new-passphrase"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("confirm answered %d: %s", res.StatusCode, b)
	}

	// 🔴 The old cookie must be dead. A reset that leaves the session that was
	// stolen still working has changed nothing the person cares about.
	after := getAs(t, c, srv.URL+"/api/auth/me")
	defer after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("the pre-reset sign-in still works (%d); resetting must sign every device out",
			after.StatusCode)
	}

	// The new password works, and the old one does not.
	ok2 := postAs(t, anon(t, srv), srv.URL+"/api/auth/signin",
		map[string]string{"username": "forgetful", "password": "a-brand-new-passphrase"})
	ok2.Body.Close()
	if ok2.StatusCode != http.StatusOK {
		t.Errorf("the new password does not work (%d)", ok2.StatusCode)
	}
	old := postAs(t, anon(t, srv), srv.URL+"/api/auth/signin",
		map[string]string{"username": "forgetful", "password": "a-long-enough-passphrase"})
	old.Body.Close()
	if old.StatusCode == http.StatusOK {
		t.Error("the old password still works after a reset")
	}
}

// A reset link works once. A mailbox somebody else can read must not contain a
// key that keeps working.
func TestResetLinkIsSingleUse(t *testing.T) {
	m := &fakeMail{}
	srv := mailServer(t, m)
	defer srv.Close()
	c := signUpWith(t, srv, "once", "once@example.test")
	confirm(t, srv, c, m)

	res := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset", map[string]string{"email": "once@example.test"})
	res.Body.Close()
	msg, _ := m.last()
	token := tokenIn(t, msg.Body, "reset")

	first := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset/confirm",
		map[string]string{"token": token, "password": "first-new-passphrase"})
	first.Body.Close()
	second := postAs(t, anon(t, srv), srv.URL+"/api/auth/reset/confirm",
		map[string]string{"token": token, "password": "second-new-passphrase"})
	second.Body.Close()
	if second.StatusCode == http.StatusOK {
		t.Error("the same reset link worked twice")
	}
	// And the second attempt did not take: the first password is the live one.
	sec := postAs(t, anon(t, srv), srv.URL+"/api/auth/signin",
		map[string]string{"username": "once", "password": "second-new-passphrase"})
	sec.Body.Close()
	if sec.StatusCode == http.StatusOK {
		t.Error("the second use of a spent link changed the password anyway")
	}
}

// confirm opens the verification link that was just mailed.
func confirm(t *testing.T, srv *httptest.Server, c *http.Client, m *fakeMail) {
	t.Helper()
	msg, ok := m.last()
	if !ok {
		t.Fatal("no confirmation mail to open")
	}
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Get(srv.URL + "/api/auth/verify?token=" + tokenIn(t, msg.Body, "token"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	res.Body.Close()
}
