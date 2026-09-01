# Anyone could read, continue and delete anyone's conversation

**Found:** 2026-08-28, while verifying an unrelated deployment. `GET
/api/sessions` against production, **with no credentials**, returned five real
visitors' conversation titles. One of them was 「好穷，救救孩子吧。。。」.
**Area:** the whole HTTP surface — there was no authentication anywhere.
**Status:** fixed.

## Blast radius

Session ids are sequential (`ses_0001`, `ses_0002`, …), so nothing had to be
guessed. A `for` loop was the exploit.

| Endpoint | What any stranger could do |
|---|---|
| `GET /api/sessions` | list every visitor's conversation title |
| `GET /api/sessions/{id}` | read a stranger's transcript, profile, tasks and consents |
| `POST /api/sessions/{id}/messages` | continue their conversation, spending the deployment's model budget |
| `DELETE /api/sessions/{id}/profile` | delete their profile |
| `POST /api/consent` | grant or revoke permissions on their record |

The subjects of those conversations are people describing unemployment, hunger
and debt to a public service. This is the most sensitive data the product holds.

## Why

There was nothing to fix, in the sense that there was nothing there: no
authentication, no session ownership, no filter on the listing. `internal/httpapi`
had a `logging` middleware and no other. The Traefik ingress rate-limited by IP
and the manifest said, correctly, that "a rate limit is not a substitute for
authentication" — but nothing else ever arrived.

**Three layers:**

| Layer | Finding |
|---|---|
| Implementation | Every handler took an id and answered. None asked whose it was. |
| Design | The data model was already subject-keyed and ready for this — `TasksFor(subjectID)`, `Profile(subjectID)` — but nothing ever said which subject belonged to whom. Identity was the missing half of a model that otherwise assumed it. |
| Process | It shipped to a public domain with a public URL. Nothing in the release path asks "can a stranger read this". |

**Why it was not noticed earlier:** the interface only ever asks for the session
id it just created, so nothing in normal use touches somebody else's. It took
reading the deployed instance with `curl` to see it, and that only happened
because a different fix needed verifying against production.

## What changed

**Accounts.** Self-service sign-up behind an invite code, sign-in, sign-out.
Passwords are PBKDF2-HMAC-SHA256 at 600k iterations with a per-password salt,
compared in constant time. `crypto/pbkdf2` is standard library from Go 1.24, so
this adds no dependency. The iteration count is recorded inside each hash, so it
can be raised later without locking anybody out.

**One gate, wrapping every route** rather than applied per handler — a route
added next year is protected by forgetting nothing. Open: `GET /api/health`,
because the Kubernetes probes hit it and a gated health check is a pod that never
becomes ready; and the four `/api/auth` endpoints, because a sign-in page you
must be signed in to reach is a locked room with the key inside.

**One ownership helper**, which every session-scoped handler goes through. It
answers **404, never 403**: 403 confirms an id exists, and with sequential ids
that is most of what an enumerator wants.

**`createSession` no longer accepts `subject_id`.** The field is deleted, not
ignored — a silently ignored field reads, to the next person, exactly like one
that works. The subject comes from the signed-in account. Without this the
ownership checks would be decoration: you could simply ask for a session onto
somebody else's record.

**Sign-in cookies** are `HttpOnly`, `Secure`, `SameSite=Lax`, and only their
SHA-256 is persisted, so a leaked state file yields no working credential.
`SameSite=Lax` is also this application's entire CSRF defence — it withholds the
cookie from cross-site POST, and every mutating endpoint takes
`application/json`, which cannot be sent cross-origin without CORS. `auth.go`
says so where somebody might otherwise relax it.

**Sign-up is closed when no invite codes are configured.** The failure that
guards against is a public registration form attached to a paid model key,
arrived at by forgetting a setting.

**Pre-account data is adopted, not deleted.** Those messages belong to real
people, and after ownership checks they would have belonged to nobody — unable
to be read, corrected or deleted by anyone, which is worse for their author than
either alternative. One named account adopts every ownerless subject, once,
additively. Nothing is re-keyed: merging twelve subjects onto one would mean
merging twelve conflicting profiles and losing most of them.

## Vocabulary

This product calls one conversation a **session**. What proves who you are is a
**sign-in** (`SignIn`, `sign_ins`, 「登录」), never a session, in code, interface
or documentation. One word, one concept.

## What this supersedes

`docs/bugfix/2026-08-28-subject-identity-and-tracked-steps.md` carried the
subject in `localStorage` because there was no identity at all, and weakened the
consent card's retention promise to "tied to this device" to stay honest about
it. The account is the real thing: the `localStorage` carry is removed, and the
promise is strong again because it is now true — the record follows the account
to another browser and another machine.

## Deploying this

Order matters, and the first startup will log one warning that is expected:

1. Put `OBA_DEMO_ACCOUNT` into AWS Secrets Manager under
   `opportunity-bridge/auth`; the `ExternalSecret` pulls it in.
   (This step read `OBA_INVITE_CODES` too, until sign-up stopped needing one —
   see 2026-09-01-sign-up-no-longer-needs-an-invite-code.md.)
2. Deploy. **The site now requires an account, and no accounts exist yet.**
   Startup logs `LEGACY_ADOPTION_FAILED` because the demo account is not there —
   correct, and not fatal.
3. Sign up as the account named in `OBA_DEMO_ACCOUNT`.
4. Restart the pod once. Adoption runs, logs `LEGACY_ADOPTED` with a count, and
   the marker stops it ever running again.

## Regression tests

`internal/httpapi/server_test.go`:

| Test | Fence |
| --- | --- |
| `TestOneAccountCannotReachAnother` | read, continue, delete, consent and list, all refused across accounts — and the owner still can |
| `TestNothingIsReachableWithoutSigningIn` | no account, no data and no spending; health stays open for the probes |
| `TestCreateSessionIgnoresASpoofedSubject` | naming a subject in the body does not claim it |
| ~~`TestSignUpNeedsAValidInviteCode`~~ | **superseded.** Missing and wrong codes both refused — deliberately removed with the invite gate on 2026-09-01, replaced by `TestSignUpNeedsNoInviteCode`. Not a lapsed fence |
| ~~`TestSignUpIsClosedWithoutInviteCodes`~~ | **superseded**, same change. Unconfigured no longer means closed; sign-up is open |
| `TestWrongPasswordIsRefusedAndSaysNothingMore` | no account-existence oracle |
| `TestSignInCookieCarriesItsProtections` | HttpOnly, Secure, SameSite |
| `TestSignOutRevokesServerSide` | a copied cookie stops working too |

`internal/store/accounts_test.go` covers hashing, salting, token storage,
expiry, pruning, username normalisation, scoped listing, and adoption.

**Sixteen mutation drills. Five found tests of mine that could not fail**, and
each is worth knowing:

- the isolation test shared one `*http.Client` between both identities, because
  `httptest.Server.Client()` returns the same pointer — "two accounts" was one
  browser;
- the list-scoping check passed on an always-empty list, because the owner's
  conversation had no user turn and was hidden from everybody;
- asserting `403` could not tell "sign-up closed" from "invite invalid", so
  deleting the closed-by-default rule stayed green;
- sign-out looked revoked because the cookie jar had dropped the cookie, not
  because the server had forgotten it;
- an expired sign-in was pruned at write time, so the lookup failed on absence
  and the expiry check itself was never reached.

```
GOWORK=off go test ./internal/httpapi/ ./internal/store/ -count=1
```

## Still open

- **No password reset.** A mail relay does exist on this cluster
  (`support@heros-agent.space`), so this is a decision about scope rather than a
  blocker: a reset flow needs single-use expiring tokens, rate limiting and a
  template, and closing the exposure should not wait for it. Until then a
  forgotten password means a new invite.
- **Invite codes are reusable and never expire.** A leaked code means open
  sign-up until it is rotated.
- **Rate limiting is per IP at the ingress**, plus a per-username backoff on
  sign-in. Neither is per account.
- **One replica.** Sign-ins live in the same single-node JSON snapshot as
  everything else.
