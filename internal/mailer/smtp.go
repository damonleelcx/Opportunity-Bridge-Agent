package mailer

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTP delivers through a submission relay.
//
// ── What this talks to, and why the hostname matters ─────────────────────────
//
// In production this is the mail relay that already runs in this cluster, in
// another namespace, reached on 587 with STARTTLS. Host MUST be the name on the
// relay's certificate: the TLS handshake verifies it, and a ClusterIP or a
// .svc.cluster.local name will not be on a Let's Encrypt certificate. The
// deployment reaches the right pod by putting that name in the pod's own
// /etc/hosts (hostAliases), NOT by a DNS rewrite — a cluster-wide rewrite of a
// public name also answers cert-manager's own challenge self-check for it, which
// breaks renewal sixty days later with nothing pointing at the cause.
// See deploy/k8s/30-deployment.yaml.
type SMTP struct {
	Host string // the name on the certificate
	Port string
	// From is the envelope sender and the From: header. The relay runs with
	// spoof protection, so this must be an address the credential below is
	// permitted to send as — normally its own.
	From string
	// ReplyTo, when set, is where a person's reply goes. It is separate from
	// From because the address a relay credential may SEND as and the mailbox a
	// human should REACH are different questions, and answering them with one
	// address means an alias that changes where inbound mail is delivered.
	ReplyTo  string
	Username string
	Password string
	Log      *slog.Logger
}

func (s *SMTP) Name() string { return "smtp" }

// Send delivers one message, or returns an error naming which step failed.
//
// The steps are spelled out rather than using smtp.SendMail because that helper
// decides on its own whether to use TLS, and "it fell back to plain text" is not
// something this may ever do quietly: the body of a password-reset mail is a
// credential in transit.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	to := Address(m.To)
	if !Valid(to) {
		return fmt.Errorf("MAIL_ADDRESS_INVALID: %q is not a deliverable address", m.To)
	}
	addr := net.JoinHostPort(s.Host, s.Port)

	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("MAIL_UNREACHABLE: could not reach the relay at %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("MAIL_GREETING_FAILED: %s did not answer as an SMTP server: %w", addr, err)
	}
	defer c.Close()

	// STARTTLS is required, never negotiated-if-offered. A relay that has lost
	// its certificate must fail loudly here rather than accept a reset link and
	// a credential over clear text.
	ok, _ := c.Extension("STARTTLS")
	if !ok {
		return fmt.Errorf("MAIL_NO_STARTTLS: %s offers no STARTTLS; refusing to send a credential in clear text", addr)
	}
	if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("MAIL_TLS_FAILED: STARTTLS to %s failed (the certificate must name %q): %w",
			addr, s.Host, err)
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("MAIL_AUTH_FAILED: the relay rejected the credential for %q: %w", s.Username, err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("MAIL_SENDER_REFUSED: the relay refused %q as a sender "+
			"(spoof protection permits only addresses this credential owns): %w", s.From, err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("MAIL_RECIPIENT_REFUSED: the relay refused %q: %w", to, err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("MAIL_DATA_REFUSED: %w", err)
	}
	if _, err := wc.Write([]byte(s.compose(to, m))); err != nil {
		wc.Close()
		return fmt.Errorf("MAIL_WRITE_FAILED: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("MAIL_NOT_ACCEPTED: the relay did not accept the message: %w", err)
	}
	_ = c.Quit()
	if s.Log != nil {
		// The address is NOT logged. This log line exists to show the pipe is
		// alive; recording who was mailed would put a list of this service's
		// users in the log of a service whose users are unemployed people.
		s.Log.Info("mail accepted by the relay",
			"code", "MAIL_SENT", "relay", addr, "subject", m.Subject)
	}
	return nil
}

// compose builds the RFC 5322 message.
//
// Headers are written here rather than assembled by a library so that what goes
// on the wire is readable in one place. Date and Message-ID are supplied because
// a message without them is scored as spam by every receiver, and this service's
// mail arriving in a junk folder is indistinguishable to the person from it
// never being sent.
func (s *SMTP) compose(to string, m Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.From)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	if s.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", s.ReplyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%d.%s>\r\n", time.Now().UTC().UnixNano(), s.Host)
	// Transactional mail must not be sent to a list, and must not generate an
	// out-of-office storm back at the support mailbox.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	// Dot-stuffing: a line consisting of a single "." ends the DATA phase, so a
	// body containing one would truncate the message and leave the rest of it
	// being interpreted as SMTP commands.
	for _, line := range strings.Split(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, ".") {
			b.WriteString(".")
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	return b.String()
}

// encodeHeader wraps a subject in RFC 2047 when it is not plain ASCII. These
// subjects are Chinese in a Chinese deployment, and an un-encoded 8-bit header
// is mangled or rejected.
func encodeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return mimeEncode(s)
		}
	}
	return s
}

func mimeEncode(s string) string {
	return "=?UTF-8?B?" + base64Encode(s) + "?="
}

var base64Std = base64.StdEncoding

func base64Encode(s string) string { return base64Std.EncodeToString([]byte(s)) }
