# Zendesk correlation fixture

Collected for the dispatch-correlation gaps that block removing the zendesk-kog wrapper plugin.
**Nothing reads these files yet** — they are committed ahead of the implementation so the shapes are
reviewable and durable rather than living in a chat message.

## Provenance, which matters more than the content

**Synthetic. Built from Zendesk's documented Request and cursor-pagination shapes by
`acmp`/zendesk-kog — NOT captured from a live Zendesk.** Nobody in this work had, or wanted, an
authenticated end-user token. The field ids are the `krateo.zendesk.com` defaults from the chart
(`REF_FIELD` 31750115749148, `KIND_FIELD` 31750395634972).

So: treat the item *shapes* as documented and the *behaviour of a real tenant* as unmeasured. If a
real list response ever disagrees with these files, the real one wins and these should be corrected.

## Why there are THREE final pages

Zendesk's pagination reference documents exactly one end signal — repeat *"until the `meta[has_more]`
property is false"*. It does **not** document whether `after_cursor` is present, `null` or absent on
the last page, and its only example is a middle page.

That is the whole reason the `continuationToken` strategy cannot express this API today:
`continuation_token.go` treats an absent-or-empty token as `Exhausted`, so if Zendesk keeps sending a
cursor on the last page the walk never terminates and runs to the page cap every time.

`page2-final-cursor-{absent,null,present}.json` are that unknown, enumerated. They differ in
`after_cursor` **and nothing else** — same items, same `has_more` — so a test using them varies one
axis. A single final page would have silently encoded one of the three as fact, which is exactly the
guess the fixture's author declined to make.

## What the fixture is built to catch

| property | where |
|---|---|
| the token is **not** on the first page | `krateo-ref-7f3a` is on page 2 |
| the token is **not** in the first item of its page | item 105, after 104 |
| the reference lives under `fields` on some items and `custom_fields` on others | 105 vs 101/103 |
| a decoy item carries a *different* token in the same place | 101, `krateo-ref-0001` |
| a `null` value must not match an empty token | 102 |
| an item with an empty array must not panic or match | 104, `custom_fields: []` |

## Expected outcomes, per the author

- token `krateo-ref-7f3a` → **exactly** item 105, found on page 2
- token `krateo-ref-0003` → item 103
- a token in no item → **not-found only after reading page 2**, i.e. after `has_more: false`
- the same token on two items → the ambiguity error (verified present today:
  `findCorrelatedItem` errors when `matches > 1`, and its comment names `apiLookup`'s `items[0]` as
  the defect that made it a hard error)
- 102's `null` must not match an empty token (verified: `echoedToken` returns `false` when the
  resolved value is nil)
- a cap of 1 page → **error, not absence**. For a dispatch kind, "absent" means *dispatch again*, so a
  walk that stops early and reports not-found re-runs the invocation
