# The sign-in error told people there was no password reset — while one sat on the same form

**Found:** 2026-08-31, reading the refusal while testing something else.
**Area:** `writeSignInRefused` in `internal/httpapi/auth.go`.
**Status:** fixed.

## What it looked like

Getting a password wrong answered:

> SIGNIN_REFUSED: That username and password do not match. — Check both. If you
> have forgotten the password, **ask for a new invite — there is no reset yet.**

On a deployment with mail configured, the 忘了密码 control is two lines below that
sentence, and it works.

## Why

The sentence was true when it was written. `docs/bugfix/2026-08-31-email-verification-and-reset.md`
then added the reset — the form, the tokens, the mail, the gate control — and
nothing pointed back at this string. Copy that states a **product limitation**
goes stale silently: no test fails, no log fires, nothing renders wrong. It just
starts lying, and only to the people who hit it.

The remedy field is also the part nobody re-reads. It is written once, at the
moment the failure is first handled, and it is the one piece of the response that
is pure prose — so it ages exactly like a comment does, without a compiler.

## Why it mattered

This is the error shown to somebody who cannot get in. The remedy is the only
line on the screen telling them what to do next, and it was sending them to ask a
stranger for a new invite — losing the account, and with it the profile, the
tracked steps and the consents — instead of pressing the button in front of them.
The people this is built for are the ones most likely to believe the service over
what they can see on the page.

## The fix

The remedy now tracks what this deployment can actually do:

```go
remedy := "Check both. This deployment cannot send mail, so there is no password reset here — " +
    "ask whoever sent you the invite link for a new invite."
if s.Mail != nil {
    remedy = "Check both. If you have forgotten the password, use the password reset on this " +
        "form; it mails a link to the account's confirmed address."
}
```

Both branches are load-bearing. With no mail there really is no reset and the
忘了密码 control really is hidden (`mail_enabled` on `/api/health` drives both), so
"use the reset" would send somebody hunting for a button that is not there — the
message now says *why* it is missing, which is the thing the old one never did.

`writeSignInRefused` became a method to reach `s.Mail`. It still produces one
answer for "no such account" and "wrong password".

### The hazard this introduces, and why it is safe

That function exists to close an account-existence oracle: a different reply for
an unknown username is a free way to ask whether a named person has an account on
a service about unemployment and benefits. A varying remedy is exactly how that
hole would reopen — through the one field nobody compares.

It is safe here because it branches on `s.Mail`, a property of the **deployment**:
identical for every username, knowable from `/api/health` without signing in.
What it must never branch on is anything about the account — whether it exists,
whether it has an address, whether that address is confirmed. That is why the
wording describes how reset works in general and never this account in
particular, and why the rule is written into the comment above the function
rather than left to be re-derived.

## Verification

Both drilled with `-count=1`:

- `TestSignInRefusalOffersTheResetOnlyWhenThereIsOne` (`internal/httpapi/email_test.go`)
  — with mail, the remedy names the reset and contains none of `no reset` /
  `not yet` / `cannot send mail`; without mail, it says why and still leaves
  something to do. Restoring the old sentence fails three assertions.
- `TestTheRefusalIsIdenticalForKnownAndUnknownAccounts` — compares **whole
  response bodies** for a real and an unknown username, in both mail modes.
  `TestWrongPasswordIsRefusedAndSaysNothingMore` predates the remedy varying at
  all and decodes only `Code` and `Message`, so a divergence in the third field
  would pass it; this one exists because the change above made that field the
  likely place to leak. Making the remedy name account existence fails it in
  both modes.

Live, two instances of the same binary differing only in SMTP settings:
`/api/health` reports `mail_enabled` false and true, the two refusals read as
intended, and in the browser the mail-enabled gate shows **Forgotten your
password?** directly under the message that points at it, while the mail-less
gate hides it and explains its absence.

## Not fixed here

Server error messages are English on a Chinese page — this one included. That is
the existing convention for the whole `/api` error channel (the gate deliberately
shows the server's own message rather than a generic translated one), so it is a
separate piece of work, not a property of this string.
