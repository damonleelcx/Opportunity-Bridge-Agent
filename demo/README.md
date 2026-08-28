# Offline demo

`scripted-turns.json` replays a fixed conversation so the interface, the tools,
the guardrails, the approval gate and the trace can all be exercised with no API
key and no network.

```bash
make demo        # OBA_BACKEND=scripted, http://localhost:8787
```

The script is consumed one turn per model call, in order. The first turn answers
the router, so the demo works without pinning an intent in the sidebar. Once the
script runs out, further messages return `SCRIPT_EXHAUSTED` — that is the
scripted backend telling you the truth rather than improvising, which is the
whole point of having it.

The trace panel shows `backend: scripted` on every run, so a replayed
conversation can never be mistaken for a live one.
