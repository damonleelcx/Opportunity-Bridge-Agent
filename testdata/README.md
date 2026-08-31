# Test fixtures — NOT shipped, NOT user-facing

`corpus/` is the invented corpus this project used to ship: 26 opportunity
records and 12 procedure guides at organisations that do not exist (合兴物流,
安宁社区日间照料中心, 龙泉驿技工学校, …).

It lives here rather than in `data/` because a person using the product must
never be shown a fabricated employer, while the tests, the evaluation suite and
the offline demo still need a corpus with the shape of a real one — including
records whose criteria, channels and source refs can be asserted against.

The Dockerfile copies `data/` and nothing else, so nothing here reaches a
deployment.

`data/` now ships only what is real: the national schemes, which exist and whose
conditions are published, and the service directory, whose URLs were fetched and
answered on their verified_at date.

See docs/bugfix/2026-08-31-the-invented-corpus-left-the-product.md
