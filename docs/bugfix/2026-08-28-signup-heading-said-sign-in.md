# The sign-up form was headed "先登录"

**Found:** 2026-08-28, reviewing the gate after putting the brand on it.
**Area:** `setGateMode` in `web/static/app.js`, the gate markup in
`web/static/index.html`, `gate.*` strings in `web/static/i18n.js`.
**Status:** fixed.

## What it looked like

Tapping 去注册 switched the button to 注册, revealed the 邀请码 field and changed
the switch link — but the heading above all of it still read **先登录**
("Sign in first"), in both languages. So the screen said *sign in* while asking
for an invite code.

## Why

The heading was the only one of the four mode-dependent strings that was not
mode-dependent. It carried `data-i18n="gate.title"`, which binds it to exactly
one string:

```html
<h1 class="gate-title" data-i18n="gate.title">先登录</h1>
```

`setGateMode` set the submit label and the switch label in code, because each is
one of two strings. Nobody added the heading to that list, and the attribute made
the omission invisible — the heading always had *a* correct-looking translation,
just never the one for the mode.

## The fix

`gate.titleSignUp` added in both languages (注册账号 / "Create an account"), and
`setGateMode` now sets the heading alongside the other two. The `data-i18n`
attribute is **removed** rather than left in place: `setLocale`'s sweep over
`[data-i18n]` would otherwise put 先登录 back over 注册账号 on every language
switch, which is the same class of bug wearing the fix as a disguise. The
comment in the markup says so, so the attribute does not get helpfully restored.

## Why it mattered more than a typo

This is the screen where a wrong guess costs something. Somebody who reads
"先登录", assumes they are on the wrong form and switches back loses what they
typed. It is also the first screen for people arriving on a link somebody else
sent them — the audience least able to shrug off an interface that contradicts
itself.

## Guard

Checked by hand against the running binary: sign-in and sign-up mode, zh-CN and
en, and — the case the removed attribute protects — **switching language while in
sign-up mode**, where the heading must stay 注册账号 / "Create an account".

⚠️ There is no browser-side test harness in this repo, so this is a manual
check, not a fence. The Go suite cannot see it. If a DOM harness is ever added,
the language-switch-in-sign-up-mode case is the one to encode first.
