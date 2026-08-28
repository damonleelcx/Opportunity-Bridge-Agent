# 4. Tools, and validating every action

Fourteen tools, in `internal/tools/builtin.go`. Each carries a JSON Schema used
three times: sent to the model as `input_schema`, enforced locally before the
tool body runs, and read by the docs. One schema, three consumers, so a tool
cannot drift from its own contract.

## The surface

| Tool | Risk | Consent | Notes |
|---|---|---|---|
| `profile_upsert` | write | `store_profile` (+ `share_with_caseworker` for staff) | Records only what was said; each field keeps the quote that asserted it |
| `opportunity_search` | read | — | Jobs, training, entrepreneurship support, subsidies. Returns why each result ranked |
| `knowledge_search` | read | — | Procedure and policy explainers, fenced as untrusted content |
| `criteria_explain` | read | — | met / unmet / unknown per criterion. **Cannot** return a verdict |
| `document_prepare` | write | staff: `share_with_caseworker` | Drafts and lists what is still missing. Sends nothing |
| `case_task_create` | write | staff: `share_with_caseworker` | Requires an owner; flags a missing channel |
| `case_task_update` | write | staff: `share_with_caseworker` | Refuses `done` without evidence |
| `case_task_list` | read | staff: `share_with_caseworker` | |
| `application_submit` | **irreversible** | `submit_on_behalf` | Never acts on its first call |
| `handoff_to_human` | write | — | Redacts the summary before storing it |
| `accessibility_set` | write | — | Plain language, large text, voice, dialect, assisted, low bandwidth |
| `consent_request` | read | — | Asks in plain words; grants nothing |
| `consent_check` | read | — | |
| `gap_analysis` | read | analyst role only | k-anonymity floor, consent-filtered, declares suppression |

## Everything a tool call must survive

`tools.Registry.Call` is the single entry point, and the order is fixed:

```
exists → allowed for this intent → allowed for this role → arguments valid
       → consent held → irreversible ones approved → Run
```

The model is never shown a tool it may not call. A refused call it can see is a
refusal it will retry.

## Validation errors are written for the model

Validation collects every problem at once, names the field, and says what would
fix it:

```
ARGUMENT_INVALID: REQUIRED_MISSING at $.query: this field is required;
  English keywords describing the work, course or support wanted;
  ENUM_MISMATCH at $.kind: got "internship"; expected one of: job, training,
  entrepreneurship, subsidy; ABOVE_MAXIMUM at $.limit: must be at most 12
```

"Invalid input" costs a round trip and often a second wrong guess. This does not.
The eval case `turn-bad-arguments-recover` holds that a bad call recovers in one
round trip.

## Per-role consent

A resident reading their own tracked tasks needs nobody's permission. The same
read by staff needs the resident's `share_with_caseworker` consent. A flat
consent list on the tool cannot express that difference, so tools carry
`RoleConsent` as well — and the check runs in `Call`, before the tool body, so
the read never happens (`TestPerRoleConsentOnRecordTools`).

## Meta, and why verifiers do not read prose

Every tool declares facts about its own execution in `Result.Meta` —
`result_count`, `suppressed_cells`, `missing_channel`, `closed_without_evidence`.
Verifiers read those. Parsing a tool's prose output with regexes is how a check
quietly stops working the day somebody rewords a message.

## Strict mode is deliberately off

The API's `strict: true` guarantees schema validation on the server. It is not
used here, because these tools have genuinely optional fields and a strict
rejection arrives as a 400 the model cannot learn from. Local validation returns
remediation text it can act on immediately. `additionalProperties: false` still
goes out on every tool (`TestEveryToolDeclaresAClosedSchema`), so the model is
not invited to invent fields.
