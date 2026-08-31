# Forgetting a password meant losing the account

**Reported:** 2026-08-31 — "reuse the same production k8s server's mail server
and support@heros-agent.space; implement verify email and reset password."
**Area:** a new `internal/mailer`, `internal/store` (addresses and single-use
tokens), `internal/httpapi/email.go`, the gate in `web/static`, and the
deployment manifests on both sides of the cluster.
**Status:** built and green. **Two prerequisites are NOT done and cannot be done
from here** — see "What is blocked".

## What was wrong

An account was a username and a password. Nothing else. Forget the password and
the account was gone, and with it the subject that the profile, the tracked tasks
and the consents all hang off. There is no support desk here that can identify a
person, and the people this service exists for are the least able to absorb
"start again from nothing".

There was no email field anywhere in the codebase. The only match for "email"
was a PII redaction regex.

## The decisions, and who made them

Put to damon, and chosen:

- **A separate SASL user for this service**, rather than widening its IAM to read
  the other project's platform secret or copying that credential. Two systems,
  two independently revocable credentials.
- **New accounts require an address; existing ones keep working.** It is the only
  route back in, so a new account without one is an account someone will lose.
  Retrospectively demanding one would stop people mid-errand.
- **An unconfirmed address blocks nothing except password reset.** Somebody can
  sign in, ask, and get things done without ever opening the mail. Confirmation
  is what makes a reset possible, and nothing else.

## The three rules the code exists to hold

1. **A reset request never says whether an address is registered.** Same status,
   same body, every time — answered before any lookup happens, so the response
   *cannot* depend on one. Anything else is a membership oracle for a service
   whose membership is "people who lost their job".
2. **A token is redeemed for ONE purpose, by exact match.** Never "any purpose
   except X". A sibling system's session layer refused purposes by denylist, and
   the day a third purpose was added it authenticated as something it was never
   meant to be. A confirmation link accepted as a password reset is that failure,
   and it is silent: the link works, and the wrong thing happens.
3. **Setting a password ends every sign-in for that account.** Somebody resets
   because they lost control; leaving the other cookie working means the reset
   changed nothing they care about.

## Details worth keeping

- **Tokens are stored hashed**, like sign-ins, so a leaked state file contains
  nothing usable. Issuing a new link for the same purpose drops the previous one,
  or pressing "send it again" grows the number of working keys in a mailbox.
- **The reset token is scrubbed from the URL** the moment the page reads it. A
  credential in an address bar survives in history, in a screenshot and in
  whatever the browser syncs. Verified in a browser: `/app?reset=X&keep=1`
  becomes `/app?keep=1`.
- **A confirmation link is a GET that changes state**, which is what an email
  link can be. Corporate scanners and prefetchers follow links before people do,
  so a token can be spent by a machine. The already-used path therefore checks
  whether the address ended up confirmed and answers success if it did — telling
  somebody "expired" about something that worked would send them round a loop.
- **Changing an address always clears the confirmed flag**, including changing it
  to the address already there. An account taken over for five minutes must not
  keep a badge it never earned for the new address.
- **A reset also confirms the address.** Somebody who used a reset link has
  proved they read that mailbox; not recording it would leave them unable to use
  a reset link next time.
- **`/api/health` reports `mail_enabled`**, and the gate offers "forgotten your
  password" only when it is true. A form that silently does nothing is worse
  than an absent one: the person waits for a message nobody sent.
- **Mail is off unless SMTP host, From and public origin are ALL set**, and
  startup names which one is missing. A relay with no origin sends links that go
  nowhere; an origin with no relay sends nothing.

## Two defects the tests caught, both mine

- `Valid("two@@at.example")` returned true. The check used the LAST `@`, so the
  local part came out as `two@` — non-empty — and the domain looked fine. Now
  exactly one `@` is required.
- The confirmation was mailed to the **raw** address a person typed while the
  stored one is normalised, and `MarkEmailVerified` compares the two. Normalised
  once, at the point the message is built.

## The relay, and the constraint that changed the From address

The relay is a pod in the `heros` namespace on this same cluster (both
namespaces confirmed live: `heros` 29d, `opportunity-bridge` 3d4h). It presents a
certificate for `mail.heros-agent.space`, so that name must be dialled and is
reached through a `hostAliases` entry pinned to the Service's pinned ClusterIP
`10.43.120.55` — **not** a CoreDNS rewrite, which would also answer
cert-manager's own challenge self-check for that name and break renewal sixty
days later.

⚠️ **`SPOOF_PROTECTION=1` means the requested From address is not available to a
separate login.** The relay permits a login to send only as its own address or an
alias pointing at it. Sending as `support@heros-agent.space` from a new `jobs@`
user would need an alias `support@ -> jobs@`, and that alias also changes where
**inbound** support@ mail is delivered — the trade heros-agent's own manifest
declines in writing. So:

    From:     jobs@heros-agent.space
    Reply-To: support@heros-agent.space

A person who replies still reaches the support mailbox, and no inbound routing
changes. Both are config (`OBA_SMTP_FROM`, `OBA_SMTP_REPLY_TO`); if the alias is
acceptable after all, it is a one-line change on each side.

## Applied: the relay now accepts connections from this namespace

`kubectl -n heros apply -f` the NetworkPolicy alone, 2026-08-31. `configured`,
generation 1 → 2, one ingress rule added and nothing else touched. No pod
restarted anywhere.

⚠️ **The dry run caught a silent no-op first.** Extracting the single
NetworkPolicy document out of the kustomize overlay loses its namespace — the
overlay's `namespace:` is a TRANSFORMER, not a field on the resource. `kubectl
diff` reported creating a brand-new NetworkPolicy called `mail` in `default`
(`@@ -0,0 +1,62 @@`), which would have applied cleanly, reported success and done
nothing at all to the real policy in `heros`. Applying a document pulled out of
an overlay needs `-n` supplied by hand.

Proven live, with the negative case, because a selector that matches nothing
looks exactly like one that works:

| probe pod in `opportunity-bridge` | expected | result |
|---|---|---|
| **with** `app.kubernetes.io/name: opportunity-bridge` | permitted | REACHABLE |
| **without** it | refused | REFUSED |

So the rule admits this service and not the namespace it lives in.

## The relay side is done, and proven by sending

Applied 2026-08-31, in this order, each dry-run first:

| step | object | restarts |
|---|---|---|
| 1 | heros `NetworkPolicy/mail` — permit 587 from this namespace | none |
| 2 | Secrets Manager: `jobs-password` added to `heros/mail`; `opportunity-bridge/mail` created | none |
| 3 | heros `ExternalSecret/heros-mail` — map `jobs-password` | none |
| 4 | heros `Deployment/mail` — seed the second account | **mail only, 23s** |
| 5 | `ExternalSecret/opportunity-bridge-model` — the credential for this service | none |
| 6 | `ConfigMap/opportunity-bridge-config` — the SMTP settings | none |

The order matters and is not incidental. The property had to exist in Secrets
Manager BEFORE the ExternalSecret named it: a data entry naming a missing
property fails the whole ExternalSecret, and that one also carries
`relay-user`/`relay-password` — the credential the relay sends WITH. Losing those
stops outbound mail for everything in that namespace while every pod stays
Running.

Only the mail pod restarted, and only once: the secret had to exist first because
`secretKeyRef` env is injected at pod start. The init container's own log shows
the intended idempotence — `account support@heros-agent.space already present —
leaving it alone` beside `seeded account jobs@heros-agent.space`.

**Proven by sending a real message** from a pod in this namespace, carrying this
service's label and the minted credential:

```
greeting ok
starttls ok (certificate verified for mail.heros-agent.space)
auth ok as jobs@heros-agent.space
SENT
spoof correctly refused: 553 5.7.1 <support@heros-agent.space>:
    Sender address rejected: not owned by user jobs@heros-agent.space
```

Relay-side: `status=sent (250 2.0.0 ... Saved)`, delivered into the support@
mailbox over LMTP.

⚠️ **That last line settles the From address as a fact rather than a reading.**
The spoof-protection constraint was inferred from the manifest's comments; it is
now measured. A `jobs@` login cannot send as `support@`, so `From: jobs@` with
`Reply-To: support@` is the only shape available without an alias that would
change where inbound support@ mail is delivered.

Nothing else in the heros namespace was touched: agentd 14d, console 18d,
admin-console 16d, postgres 29d — no restarts.

## What is still to do

**This service is not yet running the code.** The pod predates all of it, so the
SMTP settings and the credential sit there unused, and `hostAliases` (in
`30-deployment.yaml`) is deliberately unapplied — applying it would restart the
pod for no gain. Both land with the image that carries this feature.

⚠️ **Building that image needs a clean tree first.** `deploy/build.sh` builds
from the WORKING DIRECTORY, not from HEAD, and this checkout carries somebody
else's uncommitted interface work (`avatar.css`, `tokens.css`, `styles.css`,
`docs/14-interface.md`, and four `bubble-*` media files) alongside this session's
own uncommitted changes. Building now would ship all of it.

## What could not be verified locally

The SMTP leg. Go on macOS verifies certificates through the platform verifier, so
a self-signed relay certificate cannot be trusted via `SSL_CERT_FILE` and the
local probe could not complete a STARTTLS handshake.

Worth recording: **the product behaved correctly under that probe.** It refused
to send, with `MAIL_TLS_FAILED`, rather than falling back to clear text — a
password-reset body is a credential in transit. The wire format is covered by
unit tests instead (`TestComposedMessageIsWellFormed`), and the endpoint flows
are covered end to end through the `Sender` seam.

## Regression tests

`-count=1` matters.

| test | file | holds |
|------|------|-------|
| `TestSignUpRequiresAnAddressItCanReach` | `internal/httpapi/email_test.go` | five malformed addresses are refused at sign-up |
| `TestSignUpSendsAConfirmationAndDoesNotTrustTheAddressYet` | same | the mail goes to the NORMALISED address, the link is absolute against the configured origin, and the address starts unconfirmed |
| `TestConfirmationLinkWorksOnceAndOnlyForItsOwnPurpose` | same | a verification token is **refused** as a password reset, and the link confirms once |
| `TestResetRequestNeverRevealsWhetherAnAccountExists` | same | identical status AND body for a registered and an unknown address, and nothing is mailed to the stranger |
| `TestResetIgnoresUnconfirmedAddresses` | same | no link is sent to an address nobody proved they can read |
| `TestResetSetsThePasswordAndSignsEveryDeviceOut` | same | the pre-reset cookie is dead, the new password works, the old one does not |
| `TestResetLinkIsSingleUse` | same | a spent link fails and does not change the password |
| `TestFreshStoreCanIssueAnEmailToken` | `internal/store/pg_test.go` | the token map exists in a brand-new store — a nil map panics on the first sign-up a deployment ever has |
| `TestComposedMessageIsWellFormed` | `internal/mailer/smtp_test.go` | Date, Message-ID, Auto-Submitted, RFC 2047 subject, CRLF, and dot-stuffing |
| `TestAddressValidityRejectsWhatIsCertainlyWrong` | same | including `two@@at.example`, which passed before |

Verified in a browser: all four gate modes render with the right fields, and the
reset token is removed from the URL on arrival.
