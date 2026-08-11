# partner — context for Claude

Partner attribution: verifying the `x-partner-jwt` header, resolving it to a
`partners` row, and recording who to credit for a transfer. The mechanics are
readable in the code; this file is the *why*, and the traps.

## The one thing to get right

**`grpc.client.partner.id` on a request log means the credential was accepted — NOT
that anything was attributed.** Accepted is stricter than "signature verified": a JWT
whose signature checks out but carries no `sub` is rejected and stamps nothing.
`ContextWithPartnerInfo` stamps it for every accepted credential, including two cases
that leave `PartnerDBID == uuid.Nil` and so write no row at all:

- the `partners` row could not be created (`partner_create_failed`)
- the request authenticated with Basic Auth, which identifies a partner but no
  label, and records no failure reason — it never had a row to lose

Reading `partner.id` as "attributed" overstates attribution — and so does reading
`partner.id` + `partner.label` that way. Both fields are stamped in the interceptor
before any handler runs, while the row is written later and only on paths that call
`SaveTransferPartner`, so a read-only request with a labelled JWT produces the fully
populated log shape and no row at all. Nothing in the request log proves a row exists:
to count attribution, measure the `transfer_partners` / `preimage_share_partners`
tables. The log tells you what the *credential* did.

## Reading the request log

Only meaningful on the coordinator with the knob on: `KnobGatedInterceptor` runs
the check only when `isCoordinator` and `KnobEnablePartnerJWT > 0`, and it bypasses
`SparkPartnerService` methods unconditionally regardless of both. Anywhere else none
of these fields appear and their absence means nothing, so filter on the operator
index first.

A failure reason is stamped in the interceptor, before any handler runs, so it can land on
any method — including the authn and query RPCs where attribution loss actually
concentrates. Partition it by operator and method.

These are credential outcomes, not write outcomes — no row here proves attribution landed.

| observation | meaning |
|---|---|
| `partner.id` + `partner.label`, no failure field | verified with a label; attributable, but only attributed if the request reached an attribution write |
| `partner.attribution_failure` = `jwt_invalid` | header sent, verification failed — rejected, so no `partner.id` |
| `= no_subject` | signature verified but no `sub` — also rejected, so no `partner.id` |
| `= partner_create_failed` / `db_context_missing` / `write_failed` | verified, but the write did not land |
| `partner.id`, no label, no failure field | Basic Auth on `SparkPartnerService` |
| no `partner.id` and no failure field | no header was sent |

**A missing header is deliberately not counted or stamped anywhere.** The last row above
already identifies it, and a metric cannot do better: at the point the header is absent
there is no partner identity to label the series with, so the count is dominated by
traffic that was never going to be attributed — SDK clients that are not partners at all,
plus health probes and the anonymous query RPCs. An unlabelled count of that population
cannot distinguish a partner going dark from ordinary SDK growth, which is why the request
log, with its client and method dimensions, is the place to ask the question. Resist adding
a counter for it. Revisit only if the knob gating becomes per-request random, or if
attribution is enabled off the coordinator — either would make absence genuinely ambiguous.

## The counter does not cover every attribution loss

`spark_transfer_partner_attribution_failures` means "attribution was available and then
lost" — every reason on it describes a credential that arrived, or a write that was
attempted. It covers the JWT interceptor and `SaveTransferPartner`. The preimage-share
attribution helpers in `so/handler` are not wired to it — they return errors that every
caller only logs, so a lightning-receive attribution loss is invisible in the metric. Treat
the counter as a floor, not a total, and prefer the tables when you need a true rate.

## Attribution never blocks a request

An absent or rejected credential is not an error: `PartnerJWTInterceptor` always
falls through to the handler, `SaveTransferPartner` is a void best-effort helper
that returns early on every failure, and the preimage-share helpers' errors are
logged and dropped by their callers. This is deliberate — attribution is
bookkeeping and must never cost a user their transfer. Keep new failure paths
non-fatal, and record them through `RecordAttributionFailure` so the counter and
the log field cannot drift apart.

## Two levels, asymmetric strictness

`partner_keys` holds the identity, the JWT public key, and the Basic Auth secret
hash, keyed by `partner_id` (the JWT `iss`). `partners` holds one row per
`(partner_key, label)`, where label is the JWT `sub`.

- **`iss` is strict** — an unknown issuer is rejected outright.
- **`sub` is lenient when present** — an unrecognized label is auto-created, so
  onboarding a new affiliate needs no provisioning. An absent `sub` is rejected.

A JWT with no `sub` has no representation at all: `Partner.label` is `NotEmpty()`
and both attribution tables hold required edges to `partners`, so "this partner,
no affiliate" has no row shape. Rather than invent one, such a JWT is rejected — the
request still proceeds unattributed, and `no_subject` records it, so a partner that starts
omitting `sub` stays visible instead of silently occupying a state nothing can store.

Basic Auth stays label-less by design, so an empty `Label` is now an unambiguous marker for
it. Previously it meant either that or a subject-less JWT, and that ambiguity led to Basic
Auth calls on `SparkPartnerService` being read as subject-less JWTs in prod — where the true
count of subject-less JWTs is zero.
