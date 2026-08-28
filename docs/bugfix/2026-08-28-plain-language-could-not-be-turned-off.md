# Plain language could be switched on, and then not off

**Reported** 2026-08-28, from a screenshot of the records panel showing
`阅读方式 plain_language`.

**One line:** ticking 大白话 switched plain language on and nothing could switch
it off again — while the checkbox itself showed empty, denying the setting was
even in force.

**Severity:** in-flight, not first-encounter. Anyone who ticked the box hit it
immediately. It sat on the one feature built for people who cannot easily read
the screen.

## What the person saw

1. Tick 大白话 → answers get shorter ✓
2. Untick it → **nothing happens**, answers stay short ✗
3. Reload → the box is empty, answers are still short ✗
4. Records panel → `plain_language`, verbatim ✗

The only ways out were to *say* "别用大白话了" (the model does then call
`accessibility_set` correctly — the tool was never broken), or to press 清空记录,
which deletes the entire profile: city, skills, constraints, everything, to
clear one flag. Neither is signposted anywhere in the interface.

## Root cause

**Surface — the toggle had no off.** The change handler was
`if (e.target.checked) send(...)`. Unticking ran no code at all. A control that
only ever sends one of its two states is not a toggle, it is a button that looks
like a toggle.

**Deeper — the source of truth was split.** `accessibility_set` wrote the setting
to *both* the session and the profile; `prompt.go` read it from the *session*;
and `CreateSession` did not copy it from the profile. So the stored record said
"this is how this person wants answers" while every new conversation ignored it.
Two truths, and the panel displayed the one that was not in force.

**Deeper still — the control was write-only.** `.checked` was read on change and
never assigned anywhere in `app.js`. Nothing ever moved a checkbox to match
stored state, so the interface could not have shown the truth even if it had
been single.

**Institutional — the three accessibility controls had no test.** Not one. The
read-aloud work earlier the same day added two fences four lines away from this
handler and did not notice it.

## Fix

| | |
|---|---|
| Delivery settings belong to the person | `CreateSession` copies `AccessNeeds` from the profile |
| The toggle has an off | the handler sends the opposite instruction when unticked |
| The control shows what is in force | `reflectDeliverySettings()` runs on every overview refresh |
| The reader sees words | `need.*` labels for all six values, both languages |

`state.syncingA11y` guards the reflection: assigning `.checked` does not fire
`change` in any current browser, but that is a promise about the DOM rather than
about this code, and being wrong would send a message to the model on every
refresh.

## Regression fences

- `TestPlainLanguageCanBeTurnedOffAndShowsWhatIsInForce` (`web/interface_test.go`)
- `TestEveryDeliverySettingHasAReaderFacingLabel` (`web/interface_test.go`)
- `TestNewConversationInheritsDeliverySettingsFromThePerson`
  (`internal/store/delivery_settings_test.go`)

Mutation-drilled 2026-08-28: reverting the handler to on-only, removing the
reflection call, dropping one label, and disabling inheritance each turn one red.

**One assertion was deliberately NOT written.** That the session holds a *copy*
of the profile's slice is true and is done, but cannot be fenced here:
`cloneSession` copies `AccessNeeds` out of every read and `MutateSession` clones
before mutating, so no path through the store can reach the profile through an
aliased session slice. Replacing the copy with a plain assignment leaves the
suite green. Recorded so nobody adds that check back and mistakes it for cover.

## Known gaps, deliberately not fixed here

- **大字号 and 读给我听 are still client-only.** Nothing sends them to the
  server, so they are not bound to `access_needs` — binding them would clear them
  on every refresh. They also do not survive a reload. Real, milder, and not what
  was reported.
- **清空记录 is still all-or-nothing.** Deleting one field is a separate change
  and was left alone on purpose.
