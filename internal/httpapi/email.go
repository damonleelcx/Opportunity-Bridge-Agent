package httpapi

// Confirming an address, and getting back in after forgetting a password.
//
// ── Why this exists ──────────────────────────────────────────────────────────
//
// Before it, an account was a username and a password and nothing else. Forget
// the password and the account was gone — with the profile, the tracked tasks
// and the consents hanging off its subject. There is no support desk here that
// could identify somebody, and the people this service is for are the least able
// to absorb "start again from nothing".
//
// ── The three rules this file exists to hold ─────────────────────────────────
//
//  1. A reset request NEVER says whether an address is registered. The answer is
//     the same 200 either way. Anything else turns this endpoint into a
//     membership oracle for a service whose members are unemployed people.
//  2. A token is redeemed for ONE purpose, by exact match. Never "any purpose
//     except X" — a verification link accepted as a password reset is a silent
//     account takeover.
//  3. Setting a password ends every sign-in for that account. Somebody resets
//     because they lost control; leaving the other cookie working means the
//     reset changed nothing that matters.
//
// See docs/bugfix/2026-08-31-email-verification-and-reset.md

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// mailSendTimeout bounds one relay conversation. A person is waiting on the
// other side of this request, and a relay that has stopped answering must not
// hold the form open until a proxy gives up on it.
const mailSendTimeout = 20 * time.Second

// setEmail attaches or replaces the address on the signed-in account.
//
// This is the route back for accounts that predate email, and the way anybody
// corrects a typo. Changing the address always drops the verified flag, so a
// new confirmation is always sent.
func (s *Server) setEmail(w http.ResponseWriter, r *http.Request) {
	acct := accountFor(r)
	if acct == nil {
		writeErr(w, http.StatusUnauthorized, "SIGNIN_REQUIRED", "Nobody is signed in.", "Sign in first.")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			`Send {"email":"..."}.`)
		return
	}
	if !mailer.Valid(body.Email) {
		writeErr(w, http.StatusBadRequest, "EMAIL_INVALID",
			"That does not look like an email address.", "Check for a missing @ or a typo in the domain.")
		return
	}
	if err := s.Store.SetEmail(acct.Username, body.Email); err != nil {
		if err == store.ErrEmailTaken {
			// Deliberately explicit HERE and nowhere else. This caller is signed
			// in and is being told about their OWN failure to use an address; the
			// unauthenticated reset endpoint says nothing of the kind.
			writeErr(w, http.StatusConflict, "EMAIL_TAKEN",
				"That address is already on another account.",
				"Use a different address, or sign in as the account that already has it.")
			return
		}
		writeErr(w, http.StatusBadRequest, "EMAIL_INVALID", err.Error(), "Check the address and try again.")
		return
	}
	sent, err := s.sendVerification(r, acct.Username, body.Email)
	out := map[string]any{"email": mailer.Address(body.Email), "email_verified": false, "sent": sent}
	if err != nil {
		// The address IS saved even when the mail fails. Discarding it would
		// lose the person's correction because of a relay outage they cannot
		// see, and the resend button is right there.
		out["send_error"] = err.Error()
	}
	writeJSON(w, out)
}

// requestVerification resends the confirmation for the signed-in account.
func (s *Server) requestVerification(w http.ResponseWriter, r *http.Request) {
	acct := accountFor(r)
	if acct == nil {
		writeErr(w, http.StatusUnauthorized, "SIGNIN_REQUIRED", "Nobody is signed in.", "Sign in first.")
		return
	}
	if acct.Email == "" {
		writeErr(w, http.StatusBadRequest, "EMAIL_MISSING",
			"There is no address on this account yet.", "Add one first, then confirm it.")
		return
	}
	if acct.EmailVerified {
		// Not an error. Idempotent is the right answer to "confirm it again".
		writeJSON(w, map[string]any{"email": acct.Email, "email_verified": true, "sent": false})
		return
	}
	sent, err := s.sendVerification(r, acct.Username, acct.Email)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "MAIL_FAILED",
			"The confirmation could not be sent: "+err.Error(),
			"Try again in a few minutes. Your account and everything in it are unaffected.")
		return
	}
	writeJSON(w, map[string]any{"email": acct.Email, "email_verified": false, "sent": sent})
}

// verifyEmail is the target of the link in the confirmation mail.
//
// It is a GET that changes state, which is what an email link can be. The
// consequence is handled rather than ignored: corporate mail scanners and link
// prefetchers follow links before a person does, so a token can be spent by a
// machine. When that happens the person's own click finds the token used — and
// this answers SUCCESS if the address is verified, because from where they are
// standing it is, and telling them "expired" about something that worked would
// send them round a loop for nothing.
func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.verifyRedirect(w, r, "missing")
		return
	}
	t, err := s.Store.RedeemEmailToken(token, store.PurposeVerifyEmail)
	if err != nil {
		if err == store.ErrTokenUsed {
			// Spent — possibly by a scanner. Was the job done?
			if a, ok := s.Store.Account(tokenOwner(s, token)); ok && a.EmailVerified {
				s.verifyRedirect(w, r, "ok")
				return
			}
		}
		s.Log.Info("verification link refused", "code", "VERIFY_REFUSED", "reason", err.Error())
		s.verifyRedirect(w, r, "invalid")
		return
	}
	if err := s.Store.MarkEmailVerified(t.Username, t.Email); err != nil {
		s.Log.Warn("verification token was valid but could not be applied",
			"code", "VERIFY_NOT_APPLIED", "error", err.Error())
		s.verifyRedirect(w, r, "stale")
		return
	}
	s.Log.Info("email confirmed", "code", "EMAIL_VERIFIED")
	s.verifyRedirect(w, r, "ok")
}

// verifyRedirect sends the browser back to the app with a result it can render.
// A bare JSON body would be the wrong answer to a link somebody clicked in their
// mail client: they are a person looking at a browser, not an API caller.
func (s *Server) verifyRedirect(w http.ResponseWriter, r *http.Request, result string) {
	http.Redirect(w, r, "/app?verified="+url.QueryEscape(result), http.StatusSeeOther)
}

// tokenOwner is a best-effort lookup used only on the already-used path, to tell
// "a scanner spent this and the address IS confirmed" apart from "this link is
// meaningless". It returns "" when the token is unknown, which resolves to no
// account and therefore to the invalid branch.
func tokenOwner(s *Server, token string) string {
	if t, ok := s.Store.EmailTokenInfo(token); ok {
		return t.Username
	}
	return ""
}

// requestReset starts a password reset.
//
// 🔴 IT ALWAYS ANSWERS 200. Not when the address is registered, not when it is
// verified, not when the deployment has no relay: the same body every time.
// Anything that varies by whether an account exists is a membership oracle, and
// the membership here is "people who lost their job".
func (s *Server) requestReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			`Send {"email":"..."}.`)
		return
	}
	// Answered before any lookup, so the response cannot depend on one.
	defer writeJSON(w, map[string]any{
		"accepted": true,
		"note":     "If that address is on a confirmed account, a link is on its way. It expires in an hour.",
	})

	addr := mailer.Address(body.Email)
	if !mailer.Valid(addr) {
		return
	}
	// Rate-limited by ADDRESS, so one address cannot be used to post mail at
	// somebody repeatedly. Keyed through the same limiter as sign-in attempts.
	if !s.attemptAllowed("reset:" + addr) {
		s.Log.Warn("reset requests throttled", "code", "RESET_THROTTLED")
		return
	}
	s.attemptFailed("reset:" + addr)

	acct, ok := s.Store.AccountByEmail(addr)
	if !ok {
		// Logged without the address: the log of this service must not become a
		// list of who asked.
		s.Log.Info("reset requested for an address with no confirmed account",
			"code", "RESET_NO_MATCH")
		return
	}
	token, err := s.Store.IssueEmailToken(acct.Username, store.PurposeResetPassword, addr, store.ResetTokenTTL)
	if err != nil {
		s.Log.Error("could not mint a reset token", "code", "RESET_TOKEN_FAILED", "error", err.Error())
		return
	}
	link := s.Cfg.PublicOrigin + "/app?reset=" + url.QueryEscape(token)
	if err := s.mail(r, acct.Email, resetSubject(s.locale()), resetBody(s.locale(), acct.Username, link)); err != nil {
		s.Log.Error("reset mail failed", "code", "RESET_MAIL_FAILED", "error", err.Error())
	}
}

// confirmReset sets the new password against a single-use token.
func (s *Server) confirmReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			`Send {"token":"...","password":"..."}.`)
		return
	}
	if len([]rune(body.Password)) < minPasswordRunes {
		writeErr(w, http.StatusBadRequest, "PASSWORD_TOO_SHORT",
			"A password needs at least 10 characters.",
			"Length beats punctuation. A short sentence you will remember works well.")
		return
	}
	t, err := s.Store.RedeemEmailToken(body.Token, store.PurposeResetPassword)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "RESET_LINK_INVALID",
			"That link has expired or has already been used.",
			"Ask for a new one from the sign-in page. Links last one hour and work once.")
		return
	}
	hash, err := store.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED",
			"The password could not be stored.", "Try again.")
		return
	}
	ended, err := s.Store.SetPassword(t.Username, hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "RESET_FAILED", err.Error(), "Try again.")
		return
	}
	// The reset is also proof they can read that mailbox. Not marking it would
	// leave somebody who used a reset link still unable to use a reset link.
	_ = s.Store.MarkEmailVerified(t.Username, t.Email)
	s.attemptSucceeded(t.Username)
	s.Log.Info("password reset", "code", "PASSWORD_RESET", "sign_ins_ended", ended)
	writeJSON(w, map[string]any{"ok": true, "sign_ins_ended": ended,
		"note": "Every device that was signed in to this account has been signed out."})
}

// ---------------------------------------------------------------- sending

// sendVerification mints a confirmation link and sends it. It returns whether a
// message actually went, so a caller can say "check your inbox" only when one
// did.
func (s *Server) sendVerification(r *http.Request, username, email string) (bool, error) {
	if s.Mail == nil {
		return false, nil
	}
	// Normalised here, once. The stored address is normalised by SetEmail and
	// AccountByEmail matches on the normalised form, so mailing the raw string a
	// person typed would send the confirmation somewhere subtly different from
	// the address the token is recorded against — and MarkEmailVerified compares
	// the two.
	email = mailer.Address(email)
	token, err := s.Store.IssueEmailToken(username, store.PurposeVerifyEmail, email, store.VerifyTokenTTL)
	if err != nil {
		return false, err
	}
	link := s.Cfg.PublicOrigin + "/api/auth/verify?token=" + url.QueryEscape(token)
	if err := s.mail(r, email, verifySubject(s.locale()), verifyBody(s.locale(), username, link)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) mail(r *http.Request, to, subject, bodyText string) error {
	if s.Mail == nil {
		return mailer.ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(r.Context(), mailSendTimeout)
	defer cancel()
	return s.Mail.Send(ctx, mailer.Message{To: to, Subject: subject, Body: bodyText})
}

// locale is the language this deployment writes to people in. The same setting
// the answers use: a service that answers in Chinese and mails in English has
// picked the wrong moment to switch.
func (s *Server) locale() string { return s.defaultLocale() }

func mailFooter(locale, origin string) string {
	if strings.HasPrefix(locale, "zh") {
		return "\n\n——\n阿桥 · 机会桥梁\n" + origin + "\n这封信是系统自动发的，直接回信也有人看。"
	}
	return "\n\n--\nAqiao · Opportunity Bridge\n" + origin + "\nThis message was sent automatically; a reply does reach a person."
}

func verifySubject(locale string) string {
	if strings.HasPrefix(locale, "zh") {
		return "确认你的邮箱地址"
	}
	return "Confirm your email address"
}

func verifyBody(locale, username, link string) string {
	if strings.HasPrefix(locale, "zh") {
		return fmt.Sprintf(
			"你好，%s：\n\n"+
				"点下面这个链接，确认这个邮箱是你的：\n\n%s\n\n"+
				"确认之后，万一以后忘了密码，就能用这个邮箱找回来。不确认也不影响你正常使用——只是找回密码这条路走不通。\n\n"+
				"链接 24 小时内有效，只能用一次。\n"+
				"如果这不是你操作的，不用管这封信，什么都不会发生。",
			username, link)
	}
	return fmt.Sprintf(
		"Hello %s,\n\n"+
			"Confirm this address is yours by opening this link:\n\n%s\n\n"+
			"Once confirmed, you can use it to get back in if you ever forget your password. "+
			"Not confirming changes nothing else — everything else keeps working.\n\n"+
			"The link lasts 24 hours and works once.\n"+
			"If this was not you, ignore this message. Nothing happens.",
		username, link)
}

func resetSubject(locale string) string {
	if strings.HasPrefix(locale, "zh") {
		return "重设你的密码"
	}
	return "Set a new password"
}

func resetBody(locale, username, link string) string {
	if strings.HasPrefix(locale, "zh") {
		return fmt.Sprintf(
			"你好，%s：\n\n"+
				"有人（希望是你）想给这个账号设一个新密码。点下面的链接就可以设：\n\n%s\n\n"+
				"链接 1 小时内有效，只能用一次。设完之后，所有已登录的设备都会退出登录，需要重新登录一次。\n\n"+
				"如果这不是你操作的，不用管这封信——你的密码不会变。真不放心就直接回信。",
			username, link)
	}
	return fmt.Sprintf(
		"Hello %s,\n\n"+
			"Somebody — hopefully you — asked to set a new password on this account. Use this link:\n\n%s\n\n"+
			"It lasts one hour and works once. Setting a new password signs out every device that is "+
			"currently signed in, so you will sign in again afterwards.\n\n"+
			"If this was not you, ignore this message: your password does not change. Reply if you are worried.",
		username, link)
}
