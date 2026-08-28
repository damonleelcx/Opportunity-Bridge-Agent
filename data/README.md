# Sample corpus — synthetic, not authoritative

Everything in this directory is **invented sample data**. It is shaped like real
listings and real policy text so the retrieval, matching and citation machinery
can be exercised end to end, but no program, employer, address, phone number or
subsidy amount here is real.

Every record carries a `source_ref` beginning with `SAMPLE/`. The agent's
`no_invented_identifiers` verifier requires that any program the answer names is
present in the corpus, and the system prompt requires the citation to be shown.
The `SAMPLE/` prefix therefore travels all the way to the user's screen — which
is the point. A demo that cannot tell you its numbers are fake is a demo that
will eventually be believed.

**Connecting real sources.** Replace these files, or point `OBA_CORPUS_DIR` at a
directory built from real feeds. The loader validates the same fields either
way; the only thing that changes is that `source_ref` starts naming a real
document. See `docs/07-tools.md` for the retrieval contract.

| File | What it holds |
|---|---|
| `opportunities.json` | Jobs, training courses, entrepreneurship support, subsidies — one record type, four kinds. |
| `knowledge.json` | Procedure and policy explainers retrieved by `knowledge_search`. |
| `signals.json` | Seed de-identified demand signals so gap analysis has something to aggregate on a fresh install. |
