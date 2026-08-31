# 2. The four intents

The brief named four audiences. Each one is an **intent**: a first-class record
in `internal/intent/intent.go` carrying its own goal, success criteria,
boundaries, workflow, required facts, tool allowlist, verifiers and budgets.

Why a table rather than four branches in a prompt: routing, permissions, prompt
assembly, the interface's intent chips, the evaluation suite and these docs all
read the same registry. A fifth audience is a new row, not a new branch in five
files — and a typo is caught by `TestAllowedToolsExist` and `TestVerifiersExist`
rather than by a user noticing the agent behaving oddly.

## Who may reach what

Role gates routing; the tool allowlist gates action; consent gates data.

| Intent | Audience | Roles |
|---|---|---|
| `individual_pathway` | One person sorting out their own work, training or benefits | resident, caseworker |
| `low_access_support` | Graduates, workers changing trade, gig workers, migrant workers, caregiving families | resident, caseworker |
| `service_orchestration` | Frontline staff stitching siloed procedures into one tracked list | caseworker |
| `supply_demand_insight` | Planners deciding where to put capacity | analyst |

`unknown` is not a fifth audience. It is the state before routing has succeeded,
and it may call nothing at all (`TestUnroutedIntentCanCallNothing`).

## 為个人 — `individual_pathway`

Diagnose → recommend → file → follow up → human fallback, for one person.

- **Records** what they say about skills, experience, city and constraints —
  never an inference.
- **Searches** jobs, training, entrepreneurship support and subsidies, and shows
  why each result ranked.
- **Reads out** published criteria as met / unmet / unknown, with the document
  that proves each one. Never a verdict.
- **Drafts** application material and pre-fills from the profile.
- **Tracks** the follow-up and explains the procedure.

Checked for: `citations_present`, `no_eligibility_verdict`,
`actionable_next_step`, `no_invented_identifiers`.

## 為弱势或高摩擦人群 — `low_access_support`

The obstacle here is usually not information. It is time, distance, language, a
missing document, or the cost of one more failed attempt.

- **Solves the friction before the topic.** Plain language, larger text, read
  aloud, an answer in the person's own variety of Chinese, assisted-at-a-window mode, low-bandwidth
  answers — set through `accessibility_set`, which is the same path the person's
  own toggle in the interface uses.
- **Always offers an offline route.** A phone number, or an address with hours.
- **Names the cohort's known blockers** — residence registration, missing
  insurance months, no employment record, care hours — without the person having
  to know to ask.
- **Hands off early**, with the context already written down so nobody has to
  retell their story.

Cohort tags are self-declared and only ever *add* support. Using one to
discourage somebody blocks the answer.

Checked for: `plain_language`, `offline_route_present`, `no_cohort_downranking`,
`citations_present`.

## 為服务机构 — `service_orchestration`

Stops the resident from being the integration layer between counters.

- Creates and moves tasks across employment, training, social insurance, medical
  insurance, childcare, eldercare and housing.
- Makes dependencies explicit — which task blocks which, and why.
- Refuses to close a task without evidence of the underlying step. A task closed
  on a report alone is indistinguishable, six weeks later, from one that really
  happened.
- Cannot touch a resident's record at all without their
  `share_with_caseworker` consent — enforced per role in `tools.Registry.Call`,
  so the read never happens rather than being apologised for afterwards.

Checked for: `consent_on_file`, `task_has_owner_and_channel`,
`no_silent_closure`, `citations_present`.

## 為社会 — `supply_demand_insight`

Surfaces the two gaps that individual conversations make visible and nothing
else does: *the jobs are here but people cannot reach them*, and *the support
exists but nobody claims it*.

- Aggregates de-identified demand signals against published capacity.
- Counts only records whose subject granted aggregation consent, and reports the
  coverage rate next to every figure. A gap computed over 12% of a population is
  a hypothesis, not a finding.
- Suppresses any cell below the k-anonymity floor and says how many it withheld.
  Re-slicing to get under the floor is explicitly forbidden.
- Reports association, not cause, and names a confound.

No subject ids leave this intent, and it cannot reach any tool that touches an
individual record (`TestInsightCannotReachIndividualTools`).

Checked for: `k_anonymity`, `no_identifiers`, `coverage_stated`,
`no_causal_overreach`.

## One workflow, four specialisations

Every intent runs the same five stages, and differs only in what each stage
means. Keeping the stage names identical is what makes the trace viewer, the
evaluation report and the budget accounting comparable across intents.

```
understand → plan → act → verify → respond
```
