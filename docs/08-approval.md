# 8. Human approval for irreversible actions

## Risk classes

| Class | Meaning | Gate |
|---|---|---|
| `read` | touches nothing outside the process | none |
| `write` | changes our own records — reversible, logged | none |
| `irreversible` | leaves our boundary or cannot be taken back | explicit human approval of these exact arguments |

Today one tool is irreversible: `application_submit`. Filing on somebody's behalf
cannot be recalled, and a wrong or duplicate filing can cost them the attempt.

## The flow

```
model calls application_submit
   └─ Registry.Call sees RiskIrreversible, finds no matching approval
      └─ nothing runs. A PendingApproval is created holding the FULL arguments
         └─ the tool returns an error the model can read:
            "APPROVAL_REQUIRED … nothing has been done … stop this turn"
            └─ the turn ends with stop_reason = awaiting_approval
               └─ the interface renders the arguments verbatim, with Approve / Not now
                  └─ POST /api/approvals/{id} records the decision
                     └─ the conversation resumes; the model calls the tool again
                        └─ Call finds an approved record whose ARGUMENT HASH MATCHES
                           └─ the tool runs
```

## Why the hash

The approval authorises one action with one set of arguments. Approving a summary
and then running something else is the failure the gate exists to prevent, so the
match is on a canonicalised hash of the arguments — key order and whitespace
cannot make an approved call look different, and a changed field cannot inherit
an old approval.

Two tests hold both halves:

- `TestApprovalDoesNotTransferToDifferentArguments` — approve `sub-001`, then try
  to run `sub-002`; the call is refused and a fresh approval is raised.
- `TestApprovalReleasesTheExactActionThatWasShown` — the approved action runs.

And `TestIrreversibleToolDoesNothingOnItsFirstCall` asserts that no trace of the
filing exists before anybody approved it.

The evaluation suite covers the same path end to end through the real loop
(`turn-approval-gate-releases`, `turn-approval-gate-declined`), because a gate
that only blocks, and never releases, is untested in the half that matters.

## Consent is a separate gate

Approval asks *"do this specific thing?"*. Consent asks *"may I hold this kind of
data, or act in this way, at all?"* — four scopes, granted and withdrawn in the
interface, checked in `Registry.Call` before any tool body runs. Both are
recorded; neither implies the other.
