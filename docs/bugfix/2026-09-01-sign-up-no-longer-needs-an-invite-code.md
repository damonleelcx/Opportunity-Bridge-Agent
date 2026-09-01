# Sign-up no longer needs an invite code

**Decided:** 2026-09-01, by the operator, looking at the 注册账号 form.
**Kind:** product change, not a defect fix. It is filed here because it *removes*
a fence that this directory records, and a removed fence with no note reads to
the next person as a regression.
**Area:** `Config.InviteCodes` and `splitList` in `internal/config/config.go`;
the sign-up handler and `inviteAccepted` in `internal/httpapi/auth.go`; the
`gateInvite` field in `web/static/app.html`, `app.js`, `i18n.js`;
`.env.example`; `deploy/k8s/10-secrets.yaml`.
**Status:** done. `TestSignUpNeedsNoInviteCode` fences it.

## What changed

Creating an account takes a username, a password and an email address. That is
all it takes. There is no 邀请码 field on the form, no `invite_code` in the
request body, and no `OBA_INVITE_CODES` setting.

Two refusals are gone with it:

| Was | Now |
| --- | --- |
| `SIGNUP_CLOSED` — a deployment with no codes configured refused **every** sign-up | sign-up succeeds |
| `INVITE_INVALID` — a missing or wrong code refused that sign-up | the field does not exist; a stale client that still sends one is ignored, not refused |

## Why the gate existed

It was introduced with accounts themselves
(2026-08-28-data-exposure-no-ownership-checks.md). Registration is a public
endpoint on a service that spends a paid model key per conversation, so an open
form is an open budget. `EMPTY MEANS SIGN-UP IS CLOSED` was the deliberate
direction of that default: a deployment that *forgot* to configure codes had to
refuse everybody rather than admit everybody, because the failure of a forgotten
setting must not be the expensive one.

That reasoning was sound and is not being called wrong here.

## Why it is going anyway

Who this product is for decides it. 阿桥 is reached from a forwarded link, a
poster, a QR code in a service window — by people who have nobody to ask for a
code. For them the gate was not a speed bump, it was the end of the road: the
form told them they needed something they had no way to obtain, and the remedy
sentence ("ask whoever sent you the link") assumed a person who does not exist
in that path. A registration form you cannot get through is the same as no
service, and it fails precisely the visitors the service is for.

The landing page had already been rebuilt around this exact problem — that a
stranger arriving cold reads an unexplained form as *this is not for me*
(docs/14-interface.md, "The landing page"). The invite code was the same wall,
one screen later.

## What now carries the load the gate was carrying

**This is the part to keep true, and it is thinner than it sounds.** The abuse
risk did not disappear with the gate; it fell onto what was already there:

- **the ingress rate limit** — `deploy/k8s/50-ingress.yaml`, 30 requests/minute
  average per source IP, burst 60. This is now the primary bound on cost;
- **the per-turn ceilings** in `agent.Budget` — iterations, tool calls, output
  tokens, wall clock. They bound ONE turn;
- an email address required at sign-up — not proof of a person, but a cost.

**~~What does not exist: any per-account spend cap.~~ CLOSED the same day.**
`agent.Budget` is per turn, not per account, and when this was written nothing
counted what an account had spent in total. That gap was the accepted trade here
and it did not stay open: daily spending ceilings shipped on 2026-09-01, per
account AND across the whole deployment — the second because a per-account
allowance on its own multiplies by however many accounts somebody registers, and
accounts are free now precisely because of this change.
See 2026-09-01-per-account-and-deployment-spend-caps.md.

The sentence is struck through rather than deleted so the reasoning above still
reads in order: the caps exist BECAUSE this change removed the gate, and anybody
weighing whether to re-add an invite code should see that the cheaper answer was
found somewhere else.

## The other option, and why not it

The alternative considered was to keep `OBA_INVITE_CODES` and simply **invert**
the empty case: no codes ⇒ open, codes configured ⇒ still required, with the UI
hiding the field behind an `invite_required` flag on `/api/health`. It keeps the
safety valve.

It was rejected on operator reality: production pulls `OBA_INVITE_CODES` from
AWS Secrets Manager, so "sign-up is open now" would still have depended on
somebody clearing a secret in another system. A product decision that is only
half true until an out-of-band step happens is the kind that stays half true.
Removing the read makes the deploy the whole change.

The AWS secret property is **left in place** and only unsynced from the
`ExternalSecret` — re-gating later is a one-line addition there, not a hunt for
a lost value.

## Regression tests

`internal/httpapi/server_test.go`:

| Test | Fence |
| --- | --- |
| `TestSignUpNeedsNoInviteCode` | username + password + address creates an account and signs it in, 200 |
| `TestSignUpIgnoresAStaleInviteCodeField` | a client that still sends `invite_code` is not refused for it |

Both are **positive** fences, which is the awkward shape here: they guard an
absence. They go red when a gate comes back, which is the event worth catching —
re-gating is a product decision and has to be said out loud, not slipped in.

**Mutation drill.** A forced `403 INVITE_INVALID` in front of the username check
turned both red with the real refusal in the failure message; restoring the file
turned them green. Run:

```
GOWORK=off go test ./internal/httpapi/ -count=1 -run TestSignUp
```

`TestSignUpRequiresAnAddressItCanReach` (in `email_test.go`) is now the *only*
thing refusing a sign-up. Worth knowing: the address is what remains between the
open form and an account.

## Superseded

Two fences in 2026-08-28-data-exposure-no-ownership-checks.md were deleted by
this change, and are struck through there rather than silently dropped:
`TestSignUpNeedsAValidInviteCode`, `TestSignUpIsClosedWithoutInviteCodes`.
