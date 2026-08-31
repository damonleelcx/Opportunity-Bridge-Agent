// Package mailer sends the two messages this service has to put in somebody's
// inbox: confirm this address, and set a new password.
//
// Design note, and it is the same one as internal/tts: NIL MEANS OFF, and off is
// a legitimate deployment. A build with no SMTP configured still runs, still
// signs people up and still signs them in — it just cannot offer a password
// reset, and it says so rather than presenting a form that silently does
// nothing. The alternative, refusing to start without SMTP, would make a local
// checkout unusable for everything unrelated to mail.
package mailer

import (
	"context"
	"fmt"
	"strings"
)

// Message is one outgoing mail. Plain text only: these are two short
// transactional messages read by people on cheap phones, and an HTML part would
// add a rendering surface, a tracking temptation and a spam signal for nothing.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender is the seam. One method, so a deployment with no mail vendor, a test
// and the real relay are the same shape to every caller.
type Sender interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// Address is a normalised email address, and the ONE definition of when two
// addresses are the same address.
//
// Lowercased and trimmed, exactly as store.NormaliseUsername is for names. The
// local part is technically case-sensitive per RFC 5321; treating it as such
// here would let one person hold two accounts that look identical in a list, and
// would make "an address already registered" depend on how somebody typed it.
// Every real provider folds case, and this service is not the place to be
// pedantically correct at a person's expense.
func Address(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Valid reports whether an address is worth trying to deliver to.
//
// Deliberately shallow. A regex that tries to implement RFC 5322 rejects real
// addresses, and this check is not what protects anything — the confirmation
// mail is. It catches the typo classes that are certainly wrong (no @, nothing
// either side of it, no dot in the domain, whitespace) and lets the delivery
// attempt decide the rest.
func Valid(s string) bool {
	a := Address(s)
	if a == "" || len(a) > 254 || strings.ContainsAny(a, " \t\r\n,;<>\"") {
		return false
	}
	// Exactly one @. LastIndex alone accepted "two@@at.example" — the local part
	// came out as "two@", non-empty, and the domain looked fine. Quoted local
	// parts may legally contain an @, and are not supported here on purpose:
	// nobody signing up for a public employment service has one, and accepting
	// the syntax would mean accepting the escaping rules that go with it.
	if strings.Count(a, "@") != 1 {
		return false
	}
	at := strings.IndexByte(a, '@')
	if at <= 0 || at == len(a)-1 {
		return false
	}
	domain := a[at+1:]
	dot := strings.Index(domain, ".")
	return dot > 0 && dot < len(domain)-1 && !strings.Contains(domain, "..")
}

// ErrNotConfigured is what a caller gets from a nil Sender through Send. It
// exists so "this deployment cannot mail" is a value to branch on rather than a
// nil check every caller has to remember.
var ErrNotConfigured = fmt.Errorf("MAIL_NOT_CONFIGURED")
