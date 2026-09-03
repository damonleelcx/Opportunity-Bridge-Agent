# The recruiter role and the outreach handshake

**Status: implemented.** Added after the first four audiences shipped. This page
is the reasoning, because the feature inverts the product's direction of travel
and that is worth writing down before somebody "simplifies" it.

## What was asked for, and what was actually built

The request was: let company HR and headhunters find candidates.

Taken literally that is a candidate database, and this product cannot host one.
Everything in the tree points the other way — one person, their own record, their
own consent — and the founding sentence in
[01-goal-and-boundaries.md](01-goal-and-boundaries.md) is *it does not decide
eligibility, and it does not score people*. A searchable index of everybody who
ever used the service, ranked for employers, is that sentence inverted. It would
also be built on people who came here for help finding work and were never asked
whether they wanted to become inventory.

So the feature was built on a different footing:

> A recruiter searches a pool that people **opt into**, sees them **without
> names**, and reaches **nobody** until that person accepts a specific, named
> job.

The pool is not "our users". It is the subset who switched on one permission,
and it empties again when they switch it off.

## The four moving parts

| Part | Where | What it does |
|---|---|---|
| `RoleRecruiter` | `internal/domain/domain.go` | A fourth actor. Reaches exactly one intent. |
| `ConsentDiscoverable` | `internal/domain/domain.go` | `discoverable_by_employers`. Opt-in, separate from every other scope. |
| `talent_sourcing` | `internal/intent/intent.go` | The fifth intent. Five tools, six verifiers. |
| `domain.Outreach` | `internal/store/outreach.go` | The two-sided contact handshake. |

Tools: `candidate_search`, `outreach_request`, `outreach_list` (recruiter side);
`outreach_list`, `outreach_respond` (candidate side). All in
`internal/tools/recruiter.go`.

## Why a separate consent scope

Being counted in a statistic and being findable by an employer are different
exposures with different consequences. Somebody who agreed to
`aggregate_deidentified` — no name, no id, no way back — has not agreed to
appear in a stranger's search results. Reusing that scope, or implying
discoverability from `store_profile`, would have made the opt-in a thing people
did without knowing it.

Nobody is in the pool by default. No other scope implies it. `Store.
DiscoverableProfiles` filters on the scope **inside the store**, not in the tool
that calls it, so a future tool that forgets the check cannot fail open.

Consent is read live on every search rather than copied into an index at grant
time. That is what makes "you can withdraw this at any time" true on the next
search rather than at the next restart.

## Why the recruiter never sees a name

`candidateCard` in `internal/tools/recruiter.go` is the enforcement point. The
tool returns cards, never `domain.Profile`, so a field nobody meant to expose
cannot travel by accident. What is deliberately absent, and why:

| Withheld | Why |
|---|---|
| `SubjectID` | The `candidate_ref` exists so this never travels. |
| `HukouCity` | Hiring on household registration (户籍) is discrimination Chinese labour rules specifically address. This product will not make it one filter away. |
| `Cohorts` | `migrant_worker`, `older_worker`, `disability`, `caregiver`. These exist to **add** support on the resident's side. Exposed here they invert into exactly the screening the product refuses to automate. |
| `AccessNeeds` | Needing large text or a dialect speaker is a fact about *serving* somebody, never about employing them. |
| `Constraints` | "must be home by 17:00 (childcare)" reads as caregiving status. |
| `Experience.Details` | Free prose is where names, employers and addresses end up. The structured three — what, how long, what sector — are what a match needs. |

The search **schema** has only five fields: `skills`, `city`, `sectors`,
`min_years`, `limit`. Because tool arguments are validated with
`additionalProperties: false`, a model asked to screen on age or 户籍 cannot
smuggle it through — the call is refused before `Run` is entered. That is fenced
by `TestSearchSchemaOffersNoProtectedFilters`.

### The candidate_ref

`CandidateRef(recruiterID, subjectID)` is a hash of the pair, not a stored
mapping. Two properties follow:

- It cannot be reversed into a subject id, so a ref that leaks discloses nothing.
- It **differs per recruiter**, so two recruiters comparing result lists cannot
  tell they are looking at the same person. Without this, the pool is
  re-identifiable by intersecting several searches.

Recomputing it over the current pool, rather than storing a lookup table, is also
what makes withdrawal work: somebody who opted out is not in the set being
hashed, so their old ref stops resolving and no new request can reach them.

## The handshake

```
recruiter                          service                        candidate
    │  candidate_search  ─────────────>│
    │  <───── cards: ref, skills, city │   (no name, no channel)
    │                                  │
    │  outreach_request ──────────────>│
    │        [human approval gate]     │
    │                                  │──── message, job, employer ──>│
    │                                  │                                │
    │                                  │<───── accept + contact ────────│
    │  <──── channel released ─────────│                                │
    │                                  │<───── withdraw ────────────────│
    │  <──── channel closed ───────────│                                │
```

Three decisions inside that diagram are worth naming:

**`outreach_request` is `RiskIrreversible`.** It puts a named employer's approach
in front of a real person and cannot be unsent. It therefore goes through the
same approval gate as `application_submit` — a human sees the exact arguments
before anything happens. Note the ordering in `Registry.Call`: the gate fires
*before* `Run`, so a request to somebody who has withdrawn still raises an
approval and is only refused afterwards. Safe, but a wart; see "Known gaps".

**Accepting requires the person to name a contact.** An acceptance that released
nothing would look like a yes to both sides and reach nobody, and the person
would believe they had answered. `outreach_respond` refuses with
`CONTACT_REQUIRED` rather than accepting emptily. The contact is whatever the
person says it is — this service does not hand over an address it holds for
another purpose.

**Withdrawal closes the channel.** Consent that cannot be taken back was never
consent. `DecideOutreach` clears `Channel` on both decline and withdrawal, so
withdrawing changes the data and not just a label. What the recruiter already
wrote down is beyond our reach, and the tool says so plainly rather than
implying otherwise.

## The verifiers

Three new checks in `internal/guardrail/verify.go`, one per way this can hurt
somebody.

| Verifier | Severity | Catches |
|---|---|---|
| `no_candidate_scoring` | Block | "best candidate", "match score: 87", "I would rank A above B", 最佳人选, 匹配度 92 |
| `candidate_anonymity` | Block | A name or a number for somebody who has not accepted — either a leak or an invention |
| `outreach_is_an_ask` | Repair | Reporting a pending request as an arranged contact |

`no_candidate_scoring` has one subtlety that cost a test. Its remedy tells the
model to say it does not rank people — and the first version then fired on
*"I do not rank people"*, blocking the correct redraft and turning a good answer
into a refusal. `refusesToRank` now exempts first-person refusals, while
`"cand_c3d4 is weaker than cand_a1b2"` still fires: a negation is not a free
pass. Found by `turn-sourcing-ranking-blocked`, fenced by
`TestDecliningToRankIsNotItselfRanking`.

What is deliberately *allowed*: saying which of the required skills each person
listed. The employer needs a reason to act on, and a visible reason is one they
can disagree with. A single number is a ranking of people that nobody can argue
with, which is the thing being refused.

## Why residents can answer in three intents

`outreach_list` and `outreach_respond` are in the allowlists of
`individual_pathway`, `low_access_support` **and** `service_orchestration`.

`low_access_support` matters most: the people most likely to be approached are
the ones whose whole intent exists because the ordinary route is too expensive
to walk. If they could not answer a request there, the opt-in would be a door
with no handle on the inside for exactly the audience it matters most for.

`service_orchestration` covers assisted-at-a-window, which is a first-class mode
in this product — somebody who cannot read the request is the person most likely
to need staff to read it to them. The consent gate is unchanged:
`outreach_respond` carries `caseworkerNeedsShare` like every other read of a
resident's record, and the tool description forbids answering on the person's
behalf.

## No external talent database is wired, and why

Six candidate-data sources were evaluated by name. None can be wired to
`candidate_search` today, and the reasons split into three kinds.

| Source | Direction | Population | Verdict |
|---|---|---|---|
| BOSS直聘 | no public API | CN, all collars | No developer portal. Partner ATS integration is posting-out / resume-in |
| 脉脉 (Maimai) | no public API | CN white collar | No open platform. Only reverse-engineered libraries |
| 猎聘 CLI | **jobseeker-side** | CN white collar | Reads *your own* resume and applies for you — the direction we already have |
| People Data Labs | recruiter-side | global, thin | No "Candidate API" exists. `skills` fill rate **8.6%** |
| Apollo.io | recruiter-side | B2B sales contacts | Search returns no contact details; filters are firmographic |
| HeroHunt.ai | recruiter-side | devs, global | LinkedIn + GitHub + Stack Overflow. Returns relevancy **scores** |

**1. The Chinese platforms do not sell outbound search as an API.** BOSS直聘 and
猎聘 both have candidate search — 牛人搜索, 简历搜索 — inside their own recruiter
app, seat-licensed. That is their core monetisation and their PIPL exposure, so
it is deliberately not an endpoint. What their ATS partners (Moka, 北森) actually
integrate is the other two directions: push a job posting out, pull applicants
back in. 脉脉 has no open platform at all; the only "APIs" in public are
reverse-engineered client libraries.

Note when searching for these: `BOSS直聘 API 文档` returns *job adverts for API
engineers posted on BOSS直聘*, and `猎聘开放平台 API` returns a résumé template
for a job titled "开放平台API". Several apparent doc hits are listings on the
platform, not documentation of it.

**2. The one Chinese agent interface that does exist points the wrong way.**
猎聘's CLI authorises an agent to read and refine *the signed-in person's own*
resume, search jobs, and apply on their behalf — token-based, 90-day expiry,
60 req/min. That is a jobseeker tool. It is the direction 阿桥 already runs in,
and it would be a candidate for `individual_pathway` long before
`talent_sourcing`.

**3. The Western profile APIs cover the wrong people.** Their indexes are
LinkedIn- and developer-community-derived, and LinkedIn's localised China service
closed in 2023. Concretely:

- **People Data Labs** has no product called "Candidate API". The person products
  are Enrichment, Search, Identify, IP Enrichment, Autocomplete and Job Title
  Enrichment. "Candidate" appears in *Person Identify*, which returns "ranked
  candidate profiles" meaning **match candidates for entity resolution** — not
  job candidates. On the field this feature matches on, `skills` is filled for
  213M of 2.47B records: **8.6%**. Mobile phone 20.2%, work email 3.5%.
- **Apollo.io** permits recruiting in its terms, and People Search costs no
  credits — but ‼️ **it is not available on the Free plan at all.** A free-tier
  key is refused with `403 "The api/v1/mixed_people/api_search API is not
  included in your Free plan"` (measured 2026-08-31). "No credits" and "free" are
  different claims, and the widely repeated version conflates them. On a paid
  plan it returns no email or phone (enrichment does, and that costs),
  caps at 50,000 displayable records, and its filters are firmographic: company
  domain, employee count, revenue, technologies used. It is a B2B sales database
  of people with work email addresses. 数控操作工 and 养老护理员 do not have
  work email addresses.
- **HeroHunt.ai** is the closest fit mechanically — one call across LinkedIn,
  GitHub and Stack Overflow, an MCP server, contact details included, ~$107/mo
  flat. Its index is developer-heavy and global-North-weighted, and its
  compliance position is "publicly available information, the user is
  responsible for GDPR". For this product's population that is the wrong index
  and the wrong legal basis.

**Two structural blockers, beyond coverage.**

*Legal.* Under PIPL, holding personal information obtained indirectly requires
the source to have taken **separate consent for that sharing** plus a
processor-to-processor contract. "Publicly available" is not that consent. The
entire `Outreach` handshake rests on first-party consent given to us in words the
person read; an imported record arrives with no consent basis at all, and the
two cannot sit in the same result set without the recruiter being unable to tell
them apart.

*Architectural.* PDL, Apollo and HeroHunt all rank, and HeroHunt returns an
explicit relevancy score per profile. Passing that through `candidate_search`
would put a number on a person — which `no_candidate_scoring` blocks by design.
An adapter would have to discard the provider's ordering and re-derive a visible
skill-overlap reason, which discards much of what is being paid for.

### Measured, not inferred

Both adapters have been run against the live vendors via `make talent-smoke`.
What that changed:

| Claim | Before | After measuring |
|---|---|---|
| Apollo is the free option | assumed from "0 credits" | **false** — 403 on the Free plan; any paid plan works |
| PDL China coverage is ~nil | inferred from LinkedIn's 2023 exit | **too strong** — 4,261 CNC-skilled records in China |

PDL production, 2026-08-31:

| Query | Total |
|---|---|
| `skills=cnc`, worldwide | 466,866 |
| `title=engineer`, `country=china` | 266,081 |
| `skills=cnc`, `country=china` | **4,261** |
| `skills=cnc` AND `title="cnc operator"`, `country=china` | **0** |

Then the sample was read, which mattered more than the count. The 4,261 have
titles like `associate`, `chief operating officer`, `co-president` and
`manager customer experience systems apac`, listing CNC beside AutoCAD, ANSYS,
MasterCAM and finite element analysis. They are **engineers and managers who use
CNC, not 数控操作工.** Reporting that number to an employer as "4,261 CNC
operators in China" would be false, so `pdlCaveat` now says what the records
actually are and the agent is required to pass it on.

The last two rows together are the other useful finding. There is real China data, and it is
indexed by *skill* rather than by Chinese-language job title — so match on
skills, and read a zero as "not in this index", never as an absence of workers.
4,261 is also small against a national CNC workforce in the millions, which is
the honest way to report it to an employer.

### The combined view

`external_talent_scan` searches every configured vendor at once and returns a
`by_vendor` breakdown alongside the merged sample. Two decisions in that:

**The combined figure is a range, not a sum.** Adding the vendors' totals
double-counts anyone in both indexes. The bounds hold whatever the overlap is:
the lower bound is the largest single index (the union cannot be smaller), the
upper is the sum (reached only if they share nobody). The first version summed
and reported one number, which was simply wrong. Fenced by
`TestCombinedTotalIsARangeNotASum`.

**A refused vendor is shown, with its reason.** A vendor that found nobody and a
vendor the deployment is not entitled to use both contribute zero, and only the
breakdown tells them apart — an unmentioned zero reads as "nobody like that
exists", which turns a billing problem into a claim about the world. This is the
live case today: PDL answers on its free tier, Apollo 403s on the free plan. A
real run reads:

```
COMBINED: between 4261 and 4261 (2 vendors, 5 sampled)
  Apollo.io          UNAVAILABLE: APOLLO_HTTP_403: … not included in your Free plan …
  People Data Labs   total=4261     sampled=5 floor=false
```

Fenced by `TestCombinedResultNamesEachVendorIncludingTheRefusedOne`.

### End to end, on a real turn

Verified 2026-08-31 against the running server (DeepSeek backend — the provider at that date; both vendor keys
live). One recruiter message — *"I am hiring CNC operators in China. Your pool
looks tiny — how many people with CNC skills are actually out there?"* — routed to
`talent_sourcing` by `only_option`, then in a single turn:

```
candidate_search      skills=[数控]                  -> pool_size 0
external_talent_scan  skills=[cnc] country=china     -> external_vendors 2
                                                        external_at_least 4261
                                                        external_partial true
```

The answer separated the pool from the market, gave PDL's 4,261 **with** its
caveat that these are engineers and managers who list CNC beside CAD tools rather
than shop-floor operators, reported Apollo as unavailable **with the 403 reason**,
called the combined figure a floor, and said the operator market is *unknown*
rather than small. That is every rule in the directive holding at once.

**The first run of this turn found a bug, and it is the one worth remembering.**
`external_talent_scan` returned only Apollo's 403 and discarded PDL entirely,
because the guard was:

```go
if len(found.Leads) == 0 && found.Total == 0 { return Result{}, err }
```

PDL had answered *zero* for an over-narrow query, so "PDL answered none" and "PDL
never answered" collapsed into the same branch — throwing away the per-vendor
breakdown built to separate them. The model then told the recruiter the scan "is
configured to use Apollo.io", because PDL's participation was invisible. This is
the silent-zero failure the whole tool exists to prevent, committed by the tool.
It now fails only when *no* vendor answered, fenced by
`TestPartialScanKeepsTheVendorThatAnsweredZero` and
`TestScanRefusesWhenEveryVendorFailed`.

The same run showed why the query shape matters: the model had passed
`skills=["CNC","CNC machining","machining"]` **and** `title="CNC Operator"`, all
ANDed, which matches almost nobody. The schema now says so in the field
descriptions, and the second run used `skills=["cnc"]` alone and got 4,261.

### What was wired anyway, and what it is for

PDL and Apollo are now implemented, in `internal/talentsource`, behind one
`Provider` seam copied from `internal/livesource`. Both are off unless keyed.

They do **not** feed `candidate_search`. They back a separate tool,
`external_talent_scan`, which answers a different question: *how many people of
this shape exist outside the pool.* A recruiter shown three people cannot tell
whether three is the market or just the pool, and that is what decides whether
they use this service at all. The pool cannot answer that about itself.

What a scan returns is **counts and de-identified profile shapes** — job title,
region, skills, years, and a boolean saying whether the vendor holds a contact
route. It never returns a name, an email, a phone number or a profile URL, from
either vendor, even where the vendor supplies them. PDL returns `work_email`,
`mobile_phone`, `linkedin_url` and `full_name`; the decode struct does not
declare them, so they cannot reach a `Lead` by accident. Apollo hands over
`first_name` and its own person `id`; same treatment.

That is not caution, it is the only coherent position. This service refuses to
give a recruiter the contact details of somebody who **opted in**, until that
person accepts a specific job. Handing over the details of a stranger who was
never asked would be a stricter rule for our own users than for everybody else.
Every `Lead` therefore carries its `ConsentBasis` written out, and for both
vendors that basis is *"none on file"*.

Two consequences worth stating plainly:

- **Nobody found this way can be contacted through this service.**
  `outreach_request` takes a `candidate_ref` from the pool and a lead id does not
  resolve to one — fenced by `TestExternalLeadsCannotBeApproachedThroughThisService`.
  A recruiter who wants those people licenses the vendor and proceeds under their
  own lawful basis, which they would have to do anyway.
- **Vendor ranking is discarded.** PDL, Apollo and HeroHunt all rank, and passing
  an order through would be a ranking of people. `Chain.Find` re-sorts
  deterministically by source, title and region.

Three failure modes are guarded:

| Guard | Catches |
|---|---|
| `EXTERNAL_SCAN_NOT_CONFIGURED` | With no vendor keyed the tool **refuses**. An empty result would read as "nobody like that exists anywhere" — the most misleading answer it could give |
| `UNSOURCED_MARKET_FIGURE` | A headcount larger than `candidate_search` returned, with no scan behind it. Same rule the analyst intent enforces with `UNSOURCED_AGGREGATE` |
| `EXTERNAL_LEADS_AS_CANDIDATES` | Merging the estimate and the pool into one count of "candidates" — claiming reach that does not exist |

### The bug only a live call could find

PDL answers in **two wire formats**. Production returns values
(`"location_name": "chengdu, sichuan, china"`); the **sandbox returns presence
flags** (`"location_name": true`) so it can report which fields are populated
without inventing synthetic personal data. The first version of this adapter
declared those fields as `string`, so the *entire* sandbox response failed to
decode — against the exact endpoint `.env.example` tells people to develop on.

No fixture could have caught it, because the fixture was written from the
production shape. This is the argument for `make talent-smoke` existing at all.
Both formats now have fixtures, and `masked` / `maskedList` decode either.

It also improved the privacy property rather than merely fixing the bug:
`work_email` is now read for **presence only**, so the address is never assigned
to a named field a later edit could pass on.

`UNSOURCED_MARKET_FIGURE` carries a bug worth remembering. The first version used
one regex ending in `\b`, which is an **ASCII** word boundary in Go — so it
matched "241 candidates" and silently missed "241 人", the form this deployment
produces *by default*, since it answers in Chinese. A guard that only works in
the language the deployment does not use is not a guard. Now two patterns, fenced
in both languages by `TestMarketFiguresMustHaveASourceInBothLanguages`.

**So the opt-in pool is still the only source of contactable people**, and it is nationwide by
construction: no city allowlist, no corpus dependency, unlike
`opportunity_search`. Its weakness is honest and structural — it is only as large
as the number of people who opted in, which is why `candidate_search` returns
`pool_size` and the directive orders the agent to state it plainly rather than
let an employer infer a labour market from three cards.

If a source is ever procured, the seam to copy is `internal/livesource` — a
provider interface with the first-party pool as the always-there default. An
external adapter must additionally carry, per record, **the consent basis and
the provenance**, because those are what make the record lawful to hold, and a
card without them cannot be told apart from one that was scraped. The realistic
procurement order for this product's population is: 58同城开放平台 (nationwide
blue-collar, contract required), then BOSS直聘 partner channel, then nothing.

## The scope that was withheld mid-build

Worth recording, because the fence that caught it is the interesting part.

A second session working this repo at the same time saw `ConsentDiscoverable`
declared in `domain.go` before the tools that read it existed. From where it
stood the scope was orphaned — the store could filter on it, nothing reached the
filter — so it withheld the scope from `ConsentScopes()` and put it in a new
`notYetOfferedScopes()` list, with `TestEveryScopeIsOfferedOrExplained` forcing
every declared scope into one list or the other. That judgement was right on the
evidence available.

Once `candidate_search` and `talent_sourcing` landed the premise was gone, and
the line moved back — which the withholding commit had itself said would be the
move. The mechanism stays, and `notYetOfferedScopes()` is now empty; empty is the
correct state for it, not a sign it is unused.

The part worth keeping: **while the scope was withheld, every recruiter test
stayed green.** They all grant consent through `Store.SetConsent`, which bypasses
`domain.IsConsentScope`. On the real path `POST /api/consent` would have rejected
`discoverable_by_employers`, no consent card could be raised, nobody could opt
in, and `candidate_search` would have returned `pool_size: 0` forever — which
reads as "nobody matched", not "this feature is switched off". A storable path
is not a reachable one. `TestTheScopeThePoolNeedsIsOneAPersonCanActuallyGrant`
now fences the reachable one.

## Known gaps

1. **The approval gate runs before availability is checked.** A request to
   somebody who has since withdrawn still raises an approval, which a human then
   approves, and only then is it refused. Nothing unsafe happens — no request is
   created — but a person's approval decision is wasted on a void action. Fixing
   it means a pre-check hook in `Registry.Call`, which is a change to the shared
   path and was not worth making for this alone.
2. **No re-approach limit.** A recruiter who is declined is told not to ask again
   in the intent directive, and nothing enforces it. A structural fix is a check
   in `CreateOutreach` against prior declines for the same `(ref, position)`.
3. **No notification.** A pending request is visible when the person next opens
   the service. There is no email or SMS, so "the person sees your message" means
   "next time they look".
4. **The interface has no recruiter surface yet.** The role, the intent, the
   tools and the consent card all exist and the conversation works; the
   right-hand overview panel does not yet render a pending-request card.
   `Store.PendingOutreachCount` exists for it.

## Fences

Removing any of these should turn something red. All three drills below were run
and confirmed to fail before the change was restored.

| Property | Test |
|---|---|
| No protected attribute reaches a recruiter | `TestCandidateCardNeverCarriesProtectedAttributes` |
| Only opted-in people are searchable | `TestSearchReturnsOnlyPeopleWhoOptedIn` |
| Withdrawal empties the pool and kills the ref | `TestWithdrawingDiscoverabilityEmptiesThePoolAndKillsTheRef` |
| Refs are not correlatable across recruiters | `TestCandidateRefIsNotCorrelatableAcrossRecruiters` |
| Approaching a person needs a human | `TestOutreachRequestIsGatedOnHumanApproval` |
| A channel moves only on acceptance, and comes back | `TestChannelMovesOnlyOnAcceptanceAndComesBack` |
| Nobody answers somebody else's request | `TestOnlyTheAddresseeCanAnswerARequest` |
| Protected filters are not in the schema | `TestSearchSchemaOffersNoProtectedFilters` |
| Roles cannot reach each other's tools | `TestRolesCannotReachEachOthersTools` |
| Ranking blocks; refusing to rank does not | `TestCandidateScoringIsCaught`, `TestDecliningToRankIsNotItselfRanking` |
| No vendor identifier reaches a lead | `TestVendorIdentifiersNeverSurviveIntoALead` |
| An unconfigured scan refuses instead of reading as zero | `TestExternalScanRefusesWhenNoVendorIsConfigured` |
| Vendor leads cannot be approached here | `TestExternalLeadsCannotBeApproachedThroughThisService` |
| Headcounts need a source, in both languages | `TestMarketFiguresMustHaveASourceInBothLanguages` |
| One vendor down does not shrink the market | `TestChainSurvivesOneProviderFailingAndSaysWhichOne` |
| The combined count is a range, not a sum | `TestCombinedTotalIsARangeNotASum` |
| A refused vendor is shown with its reason | `TestCombinedResultNamesEachVendorIncludingTheRefusedOne` |
| Both PDL wire formats decode | `TestPDLSandboxPresenceFlagsDecodeAndCarryNoValues` |
| End to end, through the real loop | `turn-sourcing-reports-matches-not-rankings`, `turn-sourcing-ranking-blocked`, `turn-sourcing-market-figure-must-have-a-source` |
