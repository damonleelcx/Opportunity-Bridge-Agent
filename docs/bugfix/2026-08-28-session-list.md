# The conversation list showed page loads, not conversations

**Reported:** 2026-08-28, "the left side nav session history list is very buggy".
**Area:** sidebar conversation picker — `GET /api/sessions`, `store.SessionSummaries`, `refreshSessions()` in `web/static/app.js`.
**Status:** fixed.

## What it looked like

The sidebar showed a run of rows labelled `ses_0006`, `ses_0008`, `ses_0010`,
`ses_0012`, `ses_0014`, `ses_0018` — raw internal ids — interleaved with two rows
that had real titles. The ids climbed in steps of two, and no amount of use made
them go away.

## Why

Four separate causes, which is why it read as "very buggy" rather than as one
defect.

**1. Every page load minted a conversation.** `boot()` calls `newSession()`,
which `POST`s to `/api/sessions` before the person has said anything. So a reload
created a session, a role change created a session, and each one was persisted
and then listed for ever. The step of two in the ids is the tell:
`CreateSession` draws twice from one sequence, once for `ses_` and once for
`sub_`, so each shell burned two numbers.

**2. There was no title to show, so it showed the id.** The client derived the
label from the first user turn, and a shell has none, so it fell back to
`s.id`. `ses_0018` is an internal identifier; it tells the reader nothing about
which conversation it was.

**3. Ordering was by creation, not by activity.** `Sessions()` sorted on
`CreatedAt`. Going back to an older conversation and carrying it on left it
exactly where it was, underneath newer but idle ones.

**4. The list was truncated silently** at 14 rows, with nothing to say that
older conversations existed.

Two more defects were found while verifying the fix:

**5. Clicking a conversation mid-answer discarded the answer.** `openSession()`
replaced `#transcript` while `send()` was still streaming into nodes belonging to
the old transcript. Those nodes were already detached, so the rest of the answer
was written to nothing and the reader saw it stop dead. The turn was still being
persisted server-side, so the answer existed — it just wasn't reachable from
anywhere on screen until the next reload.

**6. Switching language left the list in the old language.** Introduced by this
fix: the list now contains localised strings, and the language handler did not
repaint it.

## What changed

Server (`internal/store/store.go`, `internal/httpapi/server.go`):

- `Sessions()` is replaced by `SessionSummaries()`, and `GET /api/sessions` now
  returns summaries rather than whole sessions. The rules live on the server so
  every client gets the same answer, not just this one.
- A session with no user turn is not listed. It is still stored and still
  readable by id — this hides shells, it does not delete anything.
- Ordering is by `UpdatedAt` descending, ties broken by id so the order is
  stable.
- Each row carries a `title` (the first user turn, whitespace collapsed, clipped
  to 80 **runes** — clipping by byte splits a Chinese character) and a `turns`
  count.
- The summary carries no transcript. The client refetches this list after every
  turn; it was previously shipping every message of every conversation each
  time.

Client (`web/static/app.js`, `i18n.js`, `styles.css`):

- No raw ids as labels. A conversation whose opening line is only whitespace is
  labelled in the reader's language instead.
- The full opening line is in the row's `title`, so a truncated row can be read.
  This also gives the row an accessible name, which it did not have before.
- The cap is 50 rows and it is stated: "还有 N 个更早的对话未显示".
- The list is only rebuilt when its contents, the active conversation, or the
  language actually changed, so keyboard focus and scroll position survive a
  turn.
- `openSession()` and `newSession()` abort the turn in flight. The server
  finishes and persists it regardless, so returning to that conversation shows
  the answer. An abort is not reported as a failure.

## Not changed — still open

- **The shells still accumulate on disk.** Hiding them fixed what the reader
  sees; the state file still grows by one session per page load. Collecting them
  means deciding to delete data, and creating the session lazily instead means
  reworking the three panels that need a session id before the first message
  (overview, consent, forget). Neither was in scope here.
- **There is no way to delete a conversation.** Adding one is a feature, not
  this fix.
- **Changing role discards the current conversation without asking.**
- **Reopening a conversation replays plain text only** — the greeting is
  prepended, and tool results, cards and citations are not replayed, because
  only the turn text is persisted.

## Regression tests

`internal/store/session_list_test.go`:

| Test | Fence |
| --- | --- |
| `TestSessionSummariesHidesSessionsWithNoUserTurn` | shells are not listed, and are not deleted either |
| `TestSessionSummariesIgnoresAssistantOnlySessions` | a greeting does not promote a shell |
| `TestSessionSummariesOrdersByLastActivity` | continuing an old conversation moves it to the top |
| `TestSessionSummaryTitleIsTheFirstUserTurn` | title is the opening line, on one line |
| `TestSessionSummaryTitleClipsByRuneNotByte` | no broken character mid-title |
| `TestSessionSummaryTitleEmptyRatherThanRawID` | never fall back to an internal id |
| `TestSessionSummaryCarriesNoTranscript` | the picker payload has no history |

All seven were mutation-drilled: reverting each rule turns the matching test red.

Run them with:

```
go test ./internal/store/ -count=1
```
