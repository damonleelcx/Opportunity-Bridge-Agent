# 7. Guardrails

Two directions, one package (`internal/guardrail`), because they share
primitives.

**Input guards** run before the model sees content: escalation detection on what
the person said, injection scanning on what was retrieved.

**Output guards — the verifiers** run after the model has drafted an answer and
before the person sees it. Each intent names its own set in the registry.

Every guard returns a **finding** rather than editing the answer. A guard that
silently rewrote the answer would be indistinguishable, in the trace, from a
model that got it right — and the trace is the only place an operator can audit
what happened.

| Severity | Effect |
|---|---|
| `advisory` | recorded, shown in the trace, turn continues |
| `repair` | one redraft, with the remedy fed back |
| `block` | after the redraft, the answer is replaced by the guard's own message |

## The verifiers

| Name | Sev | What it checks |
|---|---|---|
| `citations_present` | repair | If retrieval returned results, the answer names the record ids. Fires only when `result_count > 0` — demanding a citation for an empty search is how a model gets pushed into inventing one |
| `no_eligibility_verdict` | repair | No statement that the person does or does not qualify. A nearby hedge does not excuse it: the sentence repeated at the counter is the unhedged one |
| `no_invented_identifiers` | **block** | Every id in the answer exists in the corpus |
| `actionable_next_step` | repair | A link, a phone number, a window with hours, or a task created |
| `plain_language` | repair | Sentence length and a jargon table with plain replacements. Thresholds are **per script** — 22 words or 30 characters — because twenty English words and twenty Chinese characters are very different sentences, and one threshold flagged every readable Chinese answer |
| `offline_route_present` | repair | Not only a link |
| `no_cohort_downranking` | **block** | No sentence uses the person's situation as a reason not to try something |
| `consent_on_file` | **block** | Fires only when the turn actually touched the record — a turn that explains what permission is needed, and offers to ask, is correct behaviour |
| `task_has_owner_and_channel` | repair | Read from tool `Meta`, not from prose |
| `no_silent_closure` | **block** | No task marked done without evidence |
| `k_anonymity` | repair / **block** | Figures come from `gap_analysis`; suppression is disclosed; unsourced figures block |
| `no_identifiers` | **block** | No personal or internal record id in an aggregate answer |
| `coverage_stated` | repair | The consent coverage percentage sits next to the figures |
| `no_causal_overreach` | repair | Counts reported as association, with a confound named |
| `reply_language` | repair | The answer is written in the language the person was promised. Measured crudely and explainably — which script carries more of the answer — so a Chinese answer quoting an English address and a programme id passes comfortably. On all four intents |
| `no_false_reassurance` | repair | No comfort that is not backed by a fact — "don't worry", 别担心, "it'll be fine". The persona forbids these; this is what holds the line where a prompt cannot. See [13-name-and-voice.md](13-name-and-voice.md) |

An **unregistered verifier name is itself a finding**
(`TestUnknownVerifierIsItselfAFinding`). A typo in the intent registry must not
silently switch a check off.

## Escalation

A table, not a classifier, covering self-harm, wage arrears and workplace injury,
coercion and trafficking, and discrimination complaints. It runs before routing,
because which intent the message belongs to stops mattering once one of these
fires. It is deliberately over-inclusive: a handoff nobody needed costs a
sentence; a missed one costs a person something real.

## PII

Redacted from anything that leaves the session — handoff summaries, logs, demand
signals, insight answers. **Not** from what a person is shown about themselves;
hiding somebody's own ID number from them is not privacy, it is breakage.
