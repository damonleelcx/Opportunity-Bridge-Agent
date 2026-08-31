package mailer

import (
	"strings"
	"testing"
)

// What actually goes on the wire. These are the parts that are wrong silently:
// a receiver scores a message without Date or Message-ID as spam, an un-encoded
// Chinese subject is mangled, and a body line that is a lone "." ends the DATA
// phase early — truncating the message and leaving the rest of it to be read as
// SMTP commands. See docs/bugfix/2026-08-31-email-verification-and-reset.md
func TestComposedMessageIsWellFormed(t *testing.T) {
	s := &SMTP{Host: "mail.example.test", From: "jobs@example.test", ReplyTo: "support@example.test"}
	out := s.compose("person@example.test", Message{
		Subject: "确认你的邮箱地址",
		Body:    "第一行\n.\n.leading dot\n最后一行",
	})

	for _, want := range []string{
		"From: jobs@example.test\r\n",
		"To: person@example.test\r\n",
		"Reply-To: support@example.test\r\n",
		"Auto-Submitted: auto-generated\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"Date: ",
		"Message-ID: <",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A non-ASCII subject must be RFC 2047 encoded, never sent raw.
	if !strings.Contains(out, "Subject: =?UTF-8?B?") {
		t.Error("a Chinese subject was not encoded; receivers mangle or reject it")
	}
	if strings.Contains(out, "Subject: 确认") {
		t.Error("the raw 8-bit subject is on the wire")
	}
	// Dot-stuffing: a lone "." must be sent as "..", and a line merely starting
	// with a dot must be escaped too.
	body := out[strings.Index(out, "\r\n\r\n")+4:]
	if !strings.Contains(body, "\r\n..\r\n") {
		t.Errorf("a lone dot line was not stuffed; the message would be truncated there:\n%q", body)
	}
	if !strings.Contains(body, "\r\n..leading dot\r\n") {
		t.Errorf("a leading dot was not stuffed:\n%q", body)
	}
	// Every line ends CRLF, or the message is malformed.
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("a bare LF survives in the message")
	}
}

// Addresses are compared after normalisation, and the shallow validity check has
// to reject the classes that are certainly wrong without rejecting real
// addresses. `two@@at.example` passed before this test existed: the check used
// the LAST @, so the local part came out as "two@" and looked non-empty.
func TestAddressValidityRejectsWhatIsCertainlyWrong(t *testing.T) {
	for _, ok := range []string{
		"a@b.co", "First.Last+tag@sub.example.co.uk", " padded@example.test ",
	} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false; a real address was refused", ok)
		}
	}
	for _, bad := range []string{
		"", "no-at-sign", "@example.test", "person@", "two@@at.example",
		"person@nodot", "sp ace@example.test", "person@ex..ample.test",
		"a@b.", "<person@example.test>", "one@x.test,two@x.test",
	} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
	if Address(" Person@Example.TEST ") != "person@example.test" {
		t.Error("addresses are not normalised; two spellings become two accounts")
	}
}
