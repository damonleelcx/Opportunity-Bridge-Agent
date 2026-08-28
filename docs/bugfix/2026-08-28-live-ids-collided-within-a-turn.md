# live-003 was two different schools

**Found:** 2026-08-28, in the same production screenshot that reported the
streaming and Markdown faults. Not reported — spotted while reading it.
**Area:** `internal/livesource/livesource.go`, `internal/tools/builtin.go`,
`internal/agent/agent.go`.
**Status:** fixed.

## What was wrong

One answer, listing training leads for two trades:

```
- 龙诚职业培训学校（live-003）：  焊工职业技能培训
- 深圳新东方烹饪职业培训学校（live-003）：  中式烹调师
```

`live-003` named a welding school and a cookery school in the same answer.
`live-004` did the same for two others.

## Why it happened

`Chain.LookupAll` numbered its results `live-001`, `live-002`, … **starting from
one on every call**. Its comment claimed the ids were "stable within a turn",
and that was true of one lookup and false of a turn: the agent searches more than
once as a matter of course — once per trade when somebody names two, once per
intent when it wants both work and courses. Two searches, two independent
numberings, one collision per position.

## Why it matters more than it looks

The id is the **reader's only handle** on an unverified lead. It is what they say
to ask about one of them, and it is what the answer uses to attribute a warning.
Two leads sharing an id means neither can be referred to.

It is also invisible to the checks that exist. The invented-identifier guard
accepts any id produced this turn, and both `live-003`s genuinely were. Nothing
in the pipeline could object.

A second, quieter fault sat next to it: the non-Chain path
(`env.Live.Lookup`, used when a single provider is wired rather than a chain)
assigned **no ids at all**, so its results could not be cited by the answer.

## The fix

Numbering moved out of `LookupAll` and into a `livesource.Sequence` held on
`tools.Env`, which is built once per `Run` and reused for every tool call in the
turn — the only scope that actually *is* the turn. `LookupAll` no longer assigns
anything, and its comment no longer claims something it cannot deliver.

The sequence also gives **one lead one id**. The city's own service directory
answers every lookup, so a turn that searches twice gets it twice; numbering
strictly by arrival handed one office two ids and invited the answer to cite
both, as though they were two places to go. Leads are matched by URL, falling
back to region and title for the hotline-only directory entry that has no URL.

A nil sequence numbers from one, so a caller that has not been given a turn to
count within still gets usable ids rather than none — which also closes the
non-Chain hole above.

## Verified

A real turn through the running product, "我在佛山，想学电焊，也想学做菜": the
agent issued **two** searches, six live results came back across them, and they
carried **three** ids — the directory and both courses keeping the same id in
both lookups, and no id naming two different things.

## Regression tests

| Test | Fence | Drilled |
| --- | --- | --- |
| `TestLiveIDsAreUniqueAcrossOneTurn` (tools) | the sequence is **wired**: two `opportunity_search` calls sharing one `Env` never repeat an id | ✅ |
| `TestSequenceNumbersAcrossAWholeTurn` | the counter continues across lookups | ✅ |
| `TestSequenceGivesOneLeadOneID` | the same page keeps one id, and a new page gets a new one | ✅ |
| `TestSequenceMatchesURLlessLeadsByTitle` | the hotline-only entry does not spend an id per lookup | ✅ |
| `TestNilSequenceStillNumbersFromOne` | a caller with no sequence gets ids, not blanks | — |
| `TestChainKeepsGoingWhenOneProviderFails` | updated: `LookupAll` must NOT assign ids | — |

The tools-layer fence is the one that matters. The unit tests prove the counter
counts; only that one proves it is shared across the turn, which is the part that
was missing.
