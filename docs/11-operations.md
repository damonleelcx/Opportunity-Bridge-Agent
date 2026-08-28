# 11. Running it, rolling it out, improving it

## Configuration

### `.env`

Every variable below can live in a `.env` file next to the binary instead of
being exported:

```bash
cp .env.example .env && chmod 600 .env    # then fill in the key
make run-deepseek
```

Three rules:

- **A real environment variable always wins.** `DEEPSEEK_API_KEY=other make run-deepseek`
  still overrides the file. Without that rule a stale `.env` silently defeats
  every attempt to run against something else.
- **A missing file is not an error.** Most deployments export real variables and
  never have one.
- **`$OTHER` is not expanded.** Expansion looks helpful and then mangles any
  secret containing a dollar sign.

`.env` is gitignored; `.env.example` is committed. The startup log reports which
**keys** the file set and which were already in the environment — never a value,
because a leaked secret's usual first appearance is a startup log somebody pasted
into an issue. A file readable by other users gets a warning and still loads: in
many containers the mode is not the operator's to choose.

`OBA_ENV_FILE` points somewhere else.

### Variables

Every knob is in `internal/config`. Nothing else in the process reads an
environment variable — which is what makes the budgets, the anonymity floor and
the rollout gate auditable: there is one place to look for what the running
process believes.

| Variable | Default | |
|---|---|---|
| `OBA_ADDR` | `:8787` | |
| `OBA_AGENT_MODEL` | `claude-opus-5` | |
| `OBA_CLASSIFIER_MODEL` | `claude-haiku-4-5` | |
| `OBA_EFFORT` | `high` | clamped per intent |
| `OBA_REPLY_LANGUAGE` | `zh-CN` | `zh-CN`, `en`, or `match` (mirror the person). A session overrides it |
| `OBA_ENV_FILE` | `.env` | where to look for the file above |
| `OBA_MAX_ITERATIONS` | `8` | clamps per-intent caps |
| `OBA_MAX_TOOL_CALLS` | `16` | |
| `OBA_MAX_WALLCLOCK_SEC` | `180` | |
| `OBA_MAX_OUTPUT_TOKENS` | `120000` | |
| `OBA_MAX_RETRIES` | `2` | transient failures only |
| `OBA_K_ANONYMITY` | `5` | refuses to start below 2 |
| `OBA_ENABLED_INTENTS` | all | the rollout gate |
| `OBA_CORPUS_DIR` | `data` | |
| `OBA_STATE_PATH` | *(memory only)* | |
| `OBA_TRANSCRIPT_LOG` | — | JSON-lines mirror of the trace |
| `OBA_BACKEND` | `anthropic` | `anthropic`, `deepseek`, or `scripted` (needs `OBA_SCRIPT`) |
| `ANTHROPIC_API_KEY` | — | an OAuth profile from `ant auth login` also works |
| `DEEPSEEK_API_KEY` | — | required when `OBA_BACKEND=deepseek`; there is no other credential source, so it is refused at startup |
| `OBA_DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | for a proxy or a regional endpoint |

Model ids default from `OBA_BACKEND` and are checked against it at startup — see
[12-deepseek.md](12-deepseek.md).

## Fail fast, but only on what makes it dishonest

An unreadable corpus **stops the process**: an agent with nothing to cite would
spend the conversation improvising. An unreadable state file does **not**:
forgetting last week's session is no reason to refuse today's question.

## Deploying gradually

`OBA_ENABLED_INTENTS=individual_pathway,low_access_support` runs those two and
makes the others refuse **visibly**:

> This kind of request (service_orchestration) is not switched on in this
> deployment yet. Nothing was done. A member of staff can help in the meantime.

A staged rollout that silently answers with something else is worse than one that
says "not yet". The intent chips for disabled intents render greyed rather than
vanishing, so nobody has to guess whether the feature exists.

A sensible order for a real deployment: `low_access_support` at one service
window with staff present, then `individual_pathway`, then
`service_orchestration` once the caseworker consent flow has been watched in
practice, and `supply_demand_insight` last — it is the only intent whose output
shapes decisions about other people, and it is worth nothing until aggregation
consent coverage is high enough to be honest about.

## Improving it

The loop is: a real failure becomes a case, the case fails, the fix makes it
pass, and it stays in the suite.

1. Take the trace of the bad turn — it carries the exact tool calls and findings.
2. Add a case to `evals/turns.jsonl` with the model's behaviour scripted from
   that trace, and the expectation set to what *should* have happened.
3. Watch it fail: `make eval`.
4. Fix the right layer. A model that said something wrong is usually a **missing
   verifier**, not a prompt tweak; a model that called the wrong tool is usually
   a **tool description**; a model that could not recover is usually an **error
   message** written for a machine instead of for a reader.
5. `make check` before merging: gofmt, vet, and every test including the suite.

Prompt changes are the last resort, not the first, because they are the only
change in the list that nothing else can hold in place.
