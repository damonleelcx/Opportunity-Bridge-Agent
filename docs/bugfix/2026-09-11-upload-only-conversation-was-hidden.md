# A conversation where the recruiter only uploaded a file vanished from the list

**Reported:** 2026-09-11, on jobs.heros-agent.space: "招聘方会话没有在进行中任务列表显示". The list meant is the left-hand conversation history (`panel.sessions`), not the Open tasks panel.
**Area:** `POST /api/sessions/{id}/graph/imports` (`internal/httpapi/server.go`, `screenshot.go`), `store.summaries`, `stageImport` in `web/static/app.js`.
**Status:** fixed.

**One line:** a recruiter who uploaded a contact list or a mind-map screenshot, and typed nothing, lost that conversation from the conversation history. Reopening it by id showed an empty page. The file itself was staged correctly.

## What the person saw

1. Switch to the employer role, upload a CSV or a screenshot → the import plan card and the graph appear ✓
2. Look at the conversation history on the left → the conversation is not there ✗
3. Reload → still not there, and the staged plan cannot be found again from the interface ✗
4. Type one message in the same conversation → it appears ✓, which is why it looked intermittent

## Why

The conversation list shows a conversation only once it holds a user turn. That rule is deliberate: every page load creates an empty session, and the list hides those shells (docs/bugfix/2026-08-28-session-list.md).

Until this fix the agent was the only thing that ever wrote a turn (`internal/agent/agent.go`, two places). The upload route does not go through the agent: it stages the file and answers with a plan. So a conversation whose only activity was an upload had no turn at all, and was indistinguishable from an empty shell.

| Layer | Finding |
|---|---|
| Surface | The list recognises one kind of activity, a user turn. |
| Design (rule conflict) | The 08-28 list rule silently assumed every conversation starts with typing. The import entry, added later, bypasses the conversation and writes no history. Each side's tests were green on their own. |
| Institutional | Adding a session-scoped entry point had no check against the assumption that only the conversation writes history. |

### Timeline

| Date | Commit | Event | State |
|---|---|---|---|
| 2026-08-28 | `3402415` | The list hides sessions with no user turn | 💤 every conversation began with a typed message |
| 2026-09-10 | `8b72f38` | The server-side import route, which writes no history | 💤 no way to reach it from the interface |
| 2026-09-10 | **`24cd87e`** | The 批量导入 button in the interface | 💥 **owner**: upload-only conversations become possible |
| 2026-09-11 | `2d94212` | Screenshot import ships | 🔴 hit in production |

### Evidence (read-only, production)

- Running image `opportunity-bridge:2d94212-050252`, which is the head of main: not stale code.
- No `OBA_ENABLED_INTENTS` in the ConfigMap: the recruiter's messages were not being refused by the rollout gate.
- `sessions` by role, counts only: recruiter 16, of which 5 have no user turn; resident 60, of which 38 have no user turn (mostly page-load shells). The database cannot tell an upload-only conversation from a shell, because the upload left nothing in the session. The reporter confirmed they only uploaded.

Ruled out: stale deployment, the intent rollout gate, a Postgres constraint on `role` (it is plain TEXT), and a recruiter code path that skips the user turn.

## Fix

| | |
|---|---|
| An upload is recorded in its conversation | On a **successful** stage, `recordUpload` (`internal/httpapi/upload_turns.go`) appends the person's turn ("上传了导入文件：contacts.csv") and a reply carrying the plan as an `import_summary` card. Both the spreadsheet path and the screenshot path call it. |
| The words have one owner per table and cannot drift | The server's two sentences sit in `internal/agent/messages.go` beside every other system sentence, in the conversation's reply language (`replyLanguage`). They must equal `import.uploaded` / `import.staged` in `web/static/i18n.js` word for word. |
| The live page matches the reopened one | `stageImport` draws the upload as the person's turn, and refreshes the conversation list once the file is staged. |
| The card name has one spelling | `leadgraph.ToolImportSummary`, used by the tool table and by the route. |

**Why a user turn, and not a change to the list rule.** Listing sessions with no user turn would bring every empty shell back and reverse `TestSessionSummariesIgnoresAssistantOnlySessions`. An upload is something the person did, so recording it as their turn is accurate, and every existing rule about the list still holds.

**Why a failure to record does not fail the upload.** The file is already staged when the turn is written. Failing the request would lose the import, which is what the person came to do. A failure is logged as `UPLOAD_TURN_NOT_RECORDED` (WARN) and the upload stands.

**What changed in meaning.** A user turn can now be an action rather than typed text. History has a second writer besides the agent (the upload route). The `Turn` structure is unchanged.

## Not covered, deliberately

- **P2: a staged plan lives only in memory** (`leadgraph.Store.imports`). After a restart, a reopened upload still shows its plan card, but committing it fails because the staged import is gone. The card is a record of what was read, not a live handle.
- A refused upload still leaves nothing. The conversation stays an empty shell and stays hidden, as before.

## Regression fences

| Test | Fence |
|---|---|
| `TestAnUploadOnlyConversationIsListed` (`internal/httpapi/upload_turns_test.go`) | a spreadsheet-only conversation is listed and titled by the file |
| `TestAnUploadOnlyScreenshotConversationIsListed` | the screenshot path records too |
| `TestAReopenedUploadRedrawsItsPlan` | the reopened conversation holds the upload and an `import_summary` card with the plan |
| `TestAFailedUploadLeavesTheConversationHidden` | a refused upload writes nothing |
| `TestTheUploadTurnsSayWhatTheInterfaceSays` (`upload_turns_text_test.go`) | server sentences equal the interface strings, both languages |
| `TestAnUploadIsThePersonsTurnAndReachesTheConversationList` (`web/upload_turn_test.go`) | the page draws the person's turn and refreshes the list, without letting a refresh failure repaint success |

The first three failed on the unfixed code (reproduction, 2026-09-11). The existing `internal/store/session_list_test.go` fences are unchanged and still pass.

```
GOWORK=off go test ./internal/httpapi/ ./internal/store/ ./internal/agent/ ./web/ -count=1
```
