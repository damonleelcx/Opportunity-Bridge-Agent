# 1. What this is for, and what it must never do

## The problem, stated narrowly enough to build against

The wide version — that people's rising expectations of a good life outrun
uneven, insufficient development — is a description of a country, not a
specification. Software cannot answer it.

The narrow version it contains, and the one this repository addresses, is:

> An ordinary person's ability to reach stable income, a way up, and public
> support is separated from where those things actually are, by distance,
> language, paperwork, and not knowing what exists.

That gap is made of information asymmetry and transactional friction. Both are
tractable. Neither is the same thing as the underlying shortage.

## Goal

Turn *"I don't know what I can do, where to go, or whether I qualify"* into one
executable path: **diagnose → recommend → file → follow up → human fallback.**

## What success looks like

Measured per intent (`SuccessCriteria` in `internal/intent/intent.go`), but the
four that cut across all of them:

1. The person leaves with something named and real they can act on today.
2. Every claim carries a source the person can check.
3. Every next step has a channel attached — a link, a phone number, or an
   address with opening hours.
4. A human is one step away, and is offered before the person gives up.

## The boundary that matters most

**This system does not decide eligibility, and it does not score people.**

That is not a disclaimer. It is the difference between a product that reduces
imbalance and one that automates it. An agent that quietly ranks people, and
uses that rank to decide what they are shown, has rebuilt the gatekeeping it was
supposed to route around — with less accountability than the counter it replaced,
because nobody can see the reasoning.

So the rules are enforced in code, not in prose:

| Rule | Enforced by |
|---|---|
| No eligibility verdicts | `criteria_explain` cannot return one; the `no_eligibility_verdict` verifier blocks answers that state one |
| No opaque ranking that removes options | `no_cohort_downranking` blocks it; retrieval boosts are additive only and each one is named |
| No invented programmes | `no_invented_identifiers` checks every id in the answer against the corpus and blocks delivery |
| Nothing irreversible without a person | `RiskIrreversible` tools raise an approval keyed to a hash of their exact arguments |
| Nothing about a person without their permission | Consent scopes checked in `tools.Registry.Call`, before the tool body runs |
| Nothing identifying in an aggregate | k-anonymity floor inside `gap_analysis`; `no_identifiers` blocks the answer |

## What it cannot do, and says so

- It cannot create a job, change a benefit level, or move public money.
- It cannot make a criterion easier.
- It cannot tell you the answer when the published rule is ambiguous. It says
  which document would settle it, and offers a person.
- It cannot speak a dialect it cannot write. It adjusts register and says so.

## Where it stops and fetches a human

Immediately, before anything else: unpaid wages, workplace injury, withheld
documents, coercion, discrimination, or a person in distress. These are
enforcement or safety matters with their own channels and their own deadlines.
Detection is a table in `internal/guardrail/guardrail.go` — deliberately a
table and deliberately over-inclusive, because a handoff nobody needed costs a
sentence, and a missed one costs a person something real.
