# 6. Retrieval

## Why retrieval rather than a large prompt

The opportunity and policy corpus changes on its own schedule and is far larger
than any context window worth paying for. More importantly, **a retrieved record
carries a `source_ref`, and the `source_ref` is what lets the answer be
checked.** Content pasted into a prompt loses its provenance the moment the model
paraphrases it.

## The rule the product rests on

> The agent may name a programme only if that programme is in the corpus, and it
> must show the record's id when it does.

`no_invented_identifiers` enforces it by extracting every `job-###`, `trn-###`,
`ent-###`, `sub-###`, `kb-###` and `SAMPLE/...` token from the answer and
checking each against the corpus index. A miss **blocks** delivery — this is the
one failure that puts somebody in front of a counter with a programme that does
not exist.

## The scorer

BM25 over a tokenizer that emits Latin words and CJK character unigrams+bigrams,
so a Chinese query matches Chinese text without a word-segmentation dependency.

It is deliberately lexical, not embedded, for one reason: an operator can be
shown exactly why a record ranked where it did. Structured overlaps (skills,
sector, cohort) are additive boosts and **each one is named** in the result, so
the answer can say *why this listing, for you*.

Metadata filters are **hard**: they cut the candidate set before scoring. A
perfect text match in the wrong city must never surface — ranking a good match
above a location filter is how somebody gets sent 400km away
(`TestFiltersAreHardNotSoft`).

City names go through an alias table (`成都` / `成都市` / `chengdu` → `Chengdu`).
An unknown city passes through unchanged, so the answer can say *"I have nothing
for Lhasa"* rather than silently returning zero results — which a person reads as
*"there is nothing for me"*.

## Untrusted content

Retrieved documents are scanned for text written at the model rather than at the
reader, and fenced in `<untrusted_document source="...">` before the model sees
them. The fence is not security on its own — the charter carries the rule — but
it removes the ambiguity injection depends on. Findings appear in the trace and
in the interface. See `TestUntrustedContentIsScannedNotObeyed`.

## The sample corpus

Everything in `data/` is invented. Ids begin with `SAMPLE/`, and that prefix
travels to the user's screen, which is the point: a demo that cannot tell you its
numbers are fake is a demo that will eventually be believed. Replace the files,
or point `OBA_CORPUS_DIR` at a directory built from real feeds; the loader
validates the same fields either way.
