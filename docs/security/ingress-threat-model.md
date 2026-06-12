# Ingress connector surface — threat model and architectural rules

<!-- SPDX-License-Identifier: CC-BY-4.0 -->

**Status**: Threat model and architectural rules. Committed before any connector
code in `internal/ingress/` per FR-800 and the M2.3 / M5.3 / M7 threat-model-first
precedent. M10 (ingress connectors) is the active implementation milestone; the
binding spec is `specs/022-m10-ingress-connectors/spec.md` and the milestone
context is `specs/_context/m10-context.md`.

**Last updated**: 2026-06-12 (initial, M10 amendment).

**Precedence**: this document lives below `RATIONALE.md` and the active milestone
context in the document hierarchy (see `AGENTS.md`). The active milestone context
supersedes this document for operational conflicts; this document supplies the
threat model and architectural principles that context files cannot re-derive
cheaply. The vault threat model (`docs/security/vault-threat-model.md`) is the
sibling document covering vault assets; the chat mutation surface threat model
(`docs/security/chat-threat-model.md`) covers the chat-driven mutation surface;
the agent sandbox threat model (`docs/security/agent-sandbox-threat-model.md`)
covers the per-agent container surface. All four are binding for M10 since all
four surfaces remain live.

---

## Scope of this document

This is a threat model and a set of architectural rules for the ingress connector
surface — the part of the supervisor that accepts inbound external events (GitHub
webhooks for M10-core; Stripe and email-in are fast-follow connectors on the same
framework) and normalizes them into ticket rows on the existing bus. It is NOT a
spec, a plan, or an implementation.

The document covers:

1. What the ingress surface protects (assets)
2. Who it protects against (adversaries)
3. The eight required threat areas (FR-800) with their controls
4. Architectural rules Garrison enforces in the ingress integration
5. Per-attack-class mitigation summary
6. Open questions later milestone specs must resolve
7. What the M10 retro must answer

---

## Milestone banding

**M10** — initial amendment. Establishes the ingress connector surface,
the eight threat-area treatments below, and the six architectural rules.

Future milestones extending the ingress surface — adding connectors (Stripe,
email-in), adding read-back outbound actions (M11 Action Broker), extending the
noise filter — must update this document **before** any code lands for that
surface. The architectural rules section below is the binding constraint set;
new milestones either honor it or amend it explicitly here.

---

## 1. Assets

**State the ingress surface can affect**:

- **Ticket state**: `tickets` rows created with `origin = 'ingress'` and connector
  provenance in `tickets.metadata` (keys `ingress_connector`, `external_id`,
  `external_url`). Ingress-created tickets flow through the existing
  `emit_ticket_created` trigger → outbox row → `work.ticket.created.<dept>.<column>`
  notify → the existing M1 dispatcher → agent spawn under normal concurrency-cap /
  M6-throttle discipline. No new spawn path.
- **Idempotency records**: `ingress_deliveries` rows keyed unique on
  `(connector_id, external_delivery_id)`. Append-only; retained days-to-weeks;
  no pruning job in M10-core.
- **Throttle evidence**: `throttle_events` rows with
  `kind = 'ingress_rate_cap_exceeded'` when a per-connector cap breach occurs.
  Composes with — does not replace — M6's per-company cost throttle and M8's
  per-department weekly ticket budget.

**State the ingress surface explicitly CANNOT affect**:

- **Vault state**: the ingress handler has no vault-client reference beyond the
  single `ingress/github/webhook_secret` fetch at construction time. The webhook
  secret is fetched once at boot and held in memory; no per-request vault call
  occurs. The handler cannot enumerate vault paths, write vault entries, or rotate
  credentials (Rule 4 below).
- **Agent state**: the handler cannot spawn agents directly, pause or resume agents,
  or mutate `agents` rows. Agent spawning is triggered by the `emit_ticket_created`
  trigger and dispatched by the existing M1 dispatcher — the connector is not in
  that path.
- **Auth state**: the handler touches no better-auth session tables, user records,
  or invite flows.
- **MemPalace state**: the handler writes no palace entries. Connectors are not
  MCP-reachable.
- **Chat mutation state**: the handler does not call any `garrison-mutate` verb
  and is not reachable from the chat surface.

**Multi-tenancy posture**: single-tenant single-operator at M10 ship. The ingress
surface assumes one operator with full authority over connector configuration and
vault secrets.

---

## 2. Adversaries

Ranked by realistic probability of affecting the deployed Garrison instance
operationally.

1. **An internet-accessible attacker posting forged or replayed deliveries.**
   The webhook endpoint is publicly reachable; any party that discovers the URL
   can POST arbitrary bodies. Replay of captured legitimate deliveries is the
   highest-probability attack (the attacker watches a delivery and replays it
   to double-create work). Fake delivery creation (no valid signature) is the
   next most likely. These are the adversaries the signature verification and
   idempotency controls are designed for.

2. **An attacker injecting malicious text into ticket bodies that an agent later
   reads.** A legitimate delivery (valid signature, passes filter) whose
   `issue.title` or `issue.body` contains adversarially crafted text — prompt-
   injection payloads, SQL-shaped strings, or social-engineering content targeting
   an agent. The provenance tag and origin chip are the mitigations; this adversary
   does not forge deliveries but exploits the fact that externally-sourced content
   flows into agent prompts.

3. **A webhook storm — intentional or accidental.** An operator misconfigures a
   GitHub webhook that fires on every edit, or a CI pipeline triggers a flood of
   `issues` events. The per-connector rate cap is the structural answer; without
   it, a burst of deliveries becomes a burst of tickets that becomes a burst of
   spawns.

4. **An operator configuring the connector incorrectly.** Wrong department slug,
   wrong vault path, wrong rate cap. Design errors rather than adversarial attacks,
   but the fail-closed posture on vault unavailability protects against the
   silent-degradation variant.

**Adversaries we explicitly deprioritize**:

- **Host-level attackers with shell access.** Application-layer design does not
  defend against a rooted host. Systems-level mitigation belongs to a different
  document.
- **Nation-state-level adversaries.** Wrong threat model for an indie self-hosted
  deployment.
- **Malicious operators.** The operator IS the trust root in single-operator
  Garrison.

---

## 3. Eight threat areas required by FR-800

### 3.1 Raw-body capture, timing-safe comparison, and fail-closed on missing or bad signature (FR-300, SR1)

**Threat**: an attacker who can observe the delivery URL can POST arbitrary bodies.
HMAC-SHA256 verification is the only authentication gate; weaknesses in its
implementation directly enable ticket creation from unauthenticated input.

Three implementation requirements are individually non-negotiable:

**Raw-body-before-parse**: the HMAC is computed over the raw request body bytes
exactly as GitHub sent them. Any middleware that consumes the body (even to decode
it as UTF-8 or parse it as JSON) before the HMAC computation produces a mismatch
on legitimate deliveries. The handler uses `io.LimitReader` to capture raw bytes
into a `[]byte` buffer in the first step of the pipeline (before any JSON
unmarshal). All subsequent processing uses the already-captured buffer.

**Timing-safe comparison**: `==` on hex strings or byte slices is vulnerable to a
timing oracle — an attacker can craft requests that leak bits of the expected
signature by measuring response latency. Go `crypto/subtle.ConstantTimeCompare` is
the mandatory comparison; it returns in time proportional to the length of the
inputs, not their content. This is the only comparison used in the verification
path.

**Fail-closed on missing or bad signature**: a delivery with an absent or mismatched
`X-Hub-Signature-256` header returns HTTP 401 and stops. No ticket is created, no
`ingress_deliveries` row is written, no `pg_notify` fires. The handler increments
an in-process rejection counter (observable via the dashboard-api `GET /ingress/status`
endpoint on port 8081, cookie-auth required) and logs the rejection, but writes
nothing to any attacker-observable or attacker-inflatable row. The counter is
process-local and resets on restart; this is acceptable for observability (the
operator can read the log) — no budget or quota the attacker can exhaust is tied
to the counter (FR-301).

The `verifyGitHubSignature(rawBody []byte, header string, secret []byte) error`
function is the only path in the `internal/ingress` package that performs HMAC
verification. It is called by the `GitHubConnector.VerifySignature` method, which
is called by the handler before any record is written.

### 3.2 Idempotency-key forgery and replay, including the M1 concurrent-delivery race (FR-201, SR2)

**Threat**: an attacker who captures a legitimate delivery can replay it to
double-create a ticket. More concretely, an operator who clicks "Redeliver" twice
in quick succession in the GitHub UI can cause two concurrent deliveries of the
same `X-GitHub-Delivery` GUID to arrive while the first delivery's
ticket-insert transaction is still open — the M1 dedup-commit-vs-terminal-commit
race.

The dedup mechanism must survive the race. A pre-check `SELECT` does not:

```
tx1: SELECT FROM ingress_deliveries WHERE connector_id=X AND external_delivery_id=Y
     -- returns no row (not yet committed)
tx2: SELECT FROM ingress_deliveries WHERE connector_id=X AND external_delivery_id=Y
     -- also returns no row (tx1 not yet committed)
tx1: INSERT INTO ingress_deliveries ...  -- succeeds
tx2: INSERT INTO ingress_deliveries ...  -- also "succeeds" because tx1 was not seen
```

The correct mechanism is a `UNIQUE (connector_id, external_delivery_id)` constraint
on the `ingress_deliveries` table. The INSERT inside the transaction takes the
unique-index lock at insert time; the second concurrent INSERT blocks on the lock
until the first commits or rolls back, then sees the `23505` unique-violation
error. The handler detects `*pgconn.PgError.Code == "23505"` as
`ErrDuplicateDelivery`, rolls back, and returns HTTP 200 with no ticket created
and no `pg_notify` fired (FR-202).

This is the M1 race-safe dedup signal: the unique violation is the signal, not
a pre-check SELECT. The pattern comes directly from `docs/retros/m1.md` §1 and
is the same discipline as M9's `FOR UPDATE SKIP LOCKED` claim for scheduled jobs.

The `ingress_deliveries` table is append-only. Records are retained for days-to-
weeks consistent with GitHub's 3-day manual-redelivery window. No pruning job
ships in M10-core (FR-203).

**Replay scope**: replaying a delivery that has already been processed produces
no new work (200, no side effects). An attacker who captures a delivery and replays
it gets a 200 with no ticket. The only attack surface here is a fresh delivery
(not yet in `ingress_deliveries`) with a valid signature — which requires knowledge
of the webhook secret.

### 3.3 Payload-size and rate-bomb DoS, with the per-connector cap as the structural answer and the `maxBodyBytes = 26 MB` LimitReader guard (FR-600, FR-800)

**Threat**: two distinct DoS shapes:

1. **Oversized-body bomb**: an attacker POSTs a very large body to consume handler
   memory or CPU before the signature check rejects it. GitHub caps payloads at
   25 MB and silently does not deliver events that would exceed it (F4/S5). The
   handler defends at the HTTP layer regardless: `io.LimitReader(r.Body, 26_214_400)`
   (26 MB = 25 MB + 1 MB slack) caps the raw-body read. A body that reaches the
   limit produces a truncated read; the HMAC over a truncated body mismatches the
   signature header, so the handler returns 401. No memory beyond `maxBodyBytes`
   is allocated per request.

2. **Rate bomb — flood of valid deliveries**: an attacker who has obtained the
   webhook secret (or a legitimate GitHub webhook that fires on every issue edit)
   can send a burst of individually-valid deliveries. Without a cap, each valid
   delivery creates one ticket, and each ticket spawns one agent. A burst of N
   deliveries → N tickets → N concurrent spawns → N Claude Code processes →
   unbounded cost and system saturation.

   The structural answer is a per-connector in-process token bucket enforced at
   the handler edge, before any database write (FR-602: a 429'd delivery writes no
   `ingress_deliveries` row so a later legitimate redelivery dedups correctly).
   The bucket parameters (`RatePerMin`, `Burst`) come from connector config
   (`GARRISON_INGRESS_GITHUB_RATE_PER_MIN`, default 60; `GARRISON_INGRESS_GITHUB_BURST`,
   default 30). A cap breach writes a `throttle_events` row with
   `kind = 'ingress_rate_cap_exceeded'` + the existing `work.throttle.event` notify
   (FR-601), composing with — not replacing — M6's per-company cost throttle and
   M8's per-department weekly ticket budget. The over-cap delivery receives HTTP 429.

   The per-connector cap is the **primary** rate-bomb defense. IP allow-listing
   (GitHub `GET /meta` `hooks` ranges) is optional defense-in-depth (F8/spike §3.3)
   and is not a gating control — signature verification is not skipped if the IP
   is in range.

### 3.4 Injection of attacker-controlled text into ticket bodies an agent later reads (FR-501, FR-502)

**Threat**: a valid, signature-verified delivery contains adversarially crafted
text in `issue.title`, `issue.body`, or `pull_request.title`/`.body`. These fields
flow into the ticket's `objective` and `acceptance_criteria` columns, which an
agent reads as part of its task description. A sufficiently crafted payload could
attempt a prompt-injection attack against the agent that processes the ticket.

M10 does not attempt to sanitize or filter the content of ticket bodies — that is
a deeper semantic problem the agent runtime and hygiene layer own. The M10 controls
are **provenance** and **observability**:

**Provenance tag as security control**: every M10-created ticket carries
`tickets.metadata` keys `ingress_connector`, `external_id`, and `external_url`.
The `origin` column is set to `'ingress'`. These two signals tell the agent and
the hygiene system: "this ticket's content came from outside the boundary; treat
it with appropriate suspicion." The agent's system prompt and the hygiene layer
can consult `origin` to apply tighter content policies to ingress-origin tickets.
The tag is not optional — it is written in the same transaction as the ticket
insert (FR-500, FR-501).

**Origin chip as operator-observable signal**: the kanban card shows an ingress-
origin chip (distinct from operator / agent / schedule chips) linking to the
external GitHub URL. The ticket detail shows the external source link. The operator
can see at a glance which tickets originated externally and inspect the source.

**Inbound-only boundary**: the connector cannot reach back out to GitHub (or any
external service) — it has no outbound network call, no GitHub API client, no
comment-posting path (FR-700). Outbound actions are M11's Action Broker. A
prompt-injection payload in an issue body that tries to instruct the agent to "post
a reply" has no M10 surface to exploit for that purpose.

**Graceful null handling**: `issue.body` and `pull_request.body` can be null.
The connector substitutes the literal string `"(no description provided)"` for
a null body — no error path, no agent-visible failure that could be exploited
by a deliberate omission (FR-102, spike QS4).

### 3.5 Connector credential handling in the vault (FR-302, M2.3 Rule 2)

**Threat**: the webhook secret is the only authentication credential for the
ingress surface. Exposure of the secret enables an attacker to forge arbitrarily-
signed deliveries, bypassing the signature gate entirely.

The M2.3 vault discipline applies in full:

**Vault-stored, never in env**: the webhook secret lives at vault path
`ingress/github/webhook_secret`, fetched via `vault.Fetch` at `ingress.Server`
construction. It is never placed in an environment variable, a config file, a
log entry, or any agent-accessible location. The `tools/vaultlog` go-vet analyzer
enforces at build time that `vault.SecretValue` values are never passed to
`slog`/`fmt`/`log` calls (AGENTS.md "What agents should not do" rule).

**Never in agent context**: the connector is supervisor-side and never agent-
reachable. No MCP verb exposes the webhook secret; no agent container has the
vault path or the fetched value in its context. The `BuildChatConfig` function
already rejects vault-named MCP server entries with a typed error (M5.3 Rule 2
carryover).

**Fail-closed on vault unavailability at boot**: if vault is unreachable when
`ingress.Server` is constructed (the `GARRISON_INGRESS_GITHUB_ENABLED=true`
path), `NewServer` returns an error and `main.go` returns `ExitFailure`. The
server does not start in a signature-blind or degraded state — it does not fall
back to an empty secret, a default secret, or any other degraded posture. This is
M2.3 Rule 2: zero-grants-zero-secrets (FR-302).

**Per-request vault calls**: there are none. The secret is fetched once at boot
and held in the connector config struct for the server's lifetime. Secret rotation
requires a supervisor restart (documented in `docs/ops-checklist.md`).

**Secret value usage**: `vault.SecretValue.UnsafeBytes()` is called exactly once
per connector boot, inside `internal/ingress/server.go` `NewServer`, to extract
the raw bytes passed to `GitHubConnector`. The bytes are held in a `[]byte` field
(not a `string`) and passed to `verifyGitHubSignature` at handler time. No other
code path in the ingress package calls `UnsafeBytes()`.

### 3.6 Trust-boundary-inversion risk of mounting webhook routes on the cookie-auth dashboard mux (FR-103, SR7)

**Threat**: the `internal/dashboardapi` server on port 8081 is cookie-auth-gated
via better-auth session middleware. Every route on that mux passes through the
session-validation middleware. Mounting the webhook endpoint on the same mux would
require punching a hole in the middleware for the `/webhook/*` path prefix — a
trust-boundary inversion: an internet-reachable, signature-auth surface and a
browser-accessible, cookie-auth surface sharing a listener and a mux.

**Risks of the inverted design**:

- A future auth-middleware change could inadvertently close the bypass hole,
  breaking signature-authenticated deliveries.
- A future middleware change could inadvertently widen the bypass hole, exposing
  the dashboard API to internet-reachable traffic.
- The operator cannot apply distinct firewall rules to "public webhook traffic"
  and "private dashboard API traffic" — they share a port.
- The mux's error-handling behavior, rate-limiting, and logging are tuned for
  the dashboard API surface; they may be incorrect for the webhook surface.

**The chosen design — separate listener on port 8082 (SR7, decision 6, F11)**:
the ingress connector runs as an independent `ingress.Server` struct on a dedicated
port (default 8082, configurable via `GARRISON_INGRESS_PORT`), registered in the
existing supervisor errgroup alongside `dashboardapi.Server` and `health.Server`.
The mux is entirely separate; no route from the dashboard API is reachable via the
ingress port, and no webhook route is reachable via the dashboard API port.

The two ports have distinct security properties:

| Property | Port 8081 (dashboard API) | Port 8082 (ingress webhook) |
|---|---|---|
| Auth model | Cookie-auth (better-auth session) | Signature-auth (HMAC-SHA256) |
| Exposure | Private (LAN / VPN / operator browser) | Public (internet-reachable for GitHub) |
| Firewall rule | Deny from public internet | Allow from internet (webhooks) |
| Error handling | Dashboard-API error shapes | Webhook-specific status codes per SR6 |

The bad-signature rejection counter is exposed via a separate `GET /ingress/status`
handler on port 8081 (the dashboard API port), behind cookie auth (resolution note
R3). The webhook port itself never exposes any internal state.

### 3.7 Bounded blast radius of a compromised connector (FR-303, FR-700)

**Threat**: a misconfigured or fully compromised connector (attacker has the
webhook secret and can forge arbitrary deliveries) could be used to attack
Garrison's broader system.

**What a compromised connector CAN do**:

- Create `tickets` rows with `origin = 'ingress'`, `column_slug = 'todo'`,
  arbitrary `objective` and `acceptance_criteria` text, and attacker-controlled
  `metadata` values.
- Trigger the `emit_ticket_created` outbox path, causing the existing dispatcher
  to eventually spawn an agent for the created ticket.
- Write `throttle_events` rows with `kind = 'ingress_rate_cap_exceeded'` via the
  rate-cap breach path.

**What a compromised connector CANNOT do** (the blast-radius boundary):

- **Cannot spawn agents directly.** Agent spawning is triggered by the ticket-
  created notify through the existing M1 dispatcher, which applies per-department
  concurrency caps, M6 per-company cost throttle, and M8 per-department weekly
  ticket budgets. A burst of forged deliveries that passes the connector's rate cap
  still lands in the normal throttle hierarchy — tickets beyond the M8 weekly budget
  cap are not dispatched; tickets beyond the M6 company cost cap are not dispatched.
  The connector's own rate cap is the first bound; M6 and M8 are the second and
  third.

- **Cannot mutate config.** The handler has no path to `agents` rows,
  `scheduled_tasks` rows, `hiring_proposals` rows, or any governance table.

- **Cannot reach the vault.** The handler holds only the `[]byte` webhook secret
  it was constructed with. It has no vault-client reference, no `infisical`
  credentials, and no path to any other secret.

- **Cannot reach back out to any external service.** The handler has no HTTP
  client, no SMTP client, no GitHub API client. It reads and writes Postgres only.
  Outbound actions are M11's Action Broker. This is the inbound-only boundary
  (FR-700).

- **Cannot act as an agent.** No MCP verb exposes the connector to agent context;
  no agent container has visibility of the ingress package or its configuration.

**Practical blast radius**: a compromised connector can create tickets and
eventually spawn agents within the existing throttle hierarchy. At worst, it costs
money (bounded by M6 company throttle), fills the kanban with noise (bounded by
M8 dept-weekly budget), and injects prompt-injection payloads into ticket bodies
(mitigated by the provenance tag — the agent and hygiene layer know the content
is externally sourced). It cannot take any externally-observable action; it cannot
mutate config or credentials; it cannot cross the inbound-only boundary.

### 3.8 IP allow-list via GitHub `GET /meta` as optional defense-in-depth, not a gating control (spike F8)

**GitHub publishes its webhook source IP ranges** at the `GET /meta` endpoint
(the `hooks` array in the response). Best-practice documentation recommends using
this in addition to signature verification.

**This is optional defense-in-depth, not a required control.** The reasons:

1. **Signature verification is sufficient**: a delivery that passes HMAC-SHA256
   verification is authenticated regardless of source IP. Adding IP allow-listing
   provides no additional security for a correctly-implemented signature verifier —
   an attacker who has the webhook secret can POST from any IP in the allow-list.

2. **Fragile in practice**: GitHub updates its IP ranges; an allow-list that is not
   updated will false-reject legitimate deliveries. The operator has to actively
   maintain the list or fetch it dynamically on each request (a GitHub API call in
   the hot path, which is its own availability dependency).

3. **Not a substitute for signature verification**: a delivery that is in the IP
   allow-list but has a bad signature must still be rejected. If signature verification
   is required regardless, IP allow-listing does not reduce the signature-verification
   attack surface.

**M10 implementation**: IP allow-listing is not implemented in M10-core. It is
named as a candidate hardening step for operators who want defense-in-depth beyond
signature verification. A future milestone can add it as an opt-in feature behind
a config flag, fetching the allow-list dynamically from the GitHub Meta API on a
periodic schedule and caching it with a short TTL.

---

## 4. Architectural rules (binding for M10 and beyond)

These rules are binding. Any milestone that touches the ingress connector surface
honors these rules or amends them here before the spec changes.

### Rule 1: Webhook signature verification is the single authentication gate and must not be skipped

The `verifyGitHubSignature(rawBody []byte, header string, secret []byte) error`
function is the only path in the `internal/ingress` package that performs HMAC
verification. Signature verification runs at SR6 step 4 — after the event-type
check (step 3) but before any database write. No code path in the handler reaches
`InsertIngressDelivery`, `InsertIngressTicket`, or any Postgres write without
passing through a successful `VerifySignature` call.

**Consequence**: a future connector implementation that bypasses `VerifySignature`
(e.g. "just for testing") or short-circuits it on a condition ("skip on internal
IP") creates an unauthenticated ticket-creation path. Such a change requires an
explicit amendment to this document and a threat-model review before the code
lands.

**Consequence**: the `ping` event is handled AFTER signature verification (SR6).
The connector discards ping events via `Filter` (step 5), not by skipping
verification. A `ping` that doesn't pass signature verification gets a 401, not a
200.

### Rule 2: The webhook port is never shared with a cookie-auth surface

The ingress server runs on a dedicated port (default 8082, configurable via
`GARRISON_INGRESS_PORT`) entirely separate from the dashboard-api server (port
8081) and the health server (port 8080). No route from the ingress mux is
registered on the dashboard-api mux and vice versa.

**Consequence**: adding a webhook route to the existing `dashboardapi.Server`
mux requires an amendment to this document. The trust-boundary-inversion risk in
§3.6 applies regardless of middleware design.

**Consequence**: the `GET /ingress/status` endpoint (bad-signature rejection count,
observable connector state) is on the dashboard-api port (8081), behind cookie
auth. It is never on the webhook port (8082).

### Rule 3: A 429'd delivery writes no `ingress_deliveries` row

The per-connector rate cap (step 7 of the handler pipeline) runs BEFORE the
`ingress_deliveries` INSERT (step 9 of the pipeline). An over-cap delivery that
receives HTTP 429 writes nothing to the database — no `ingress_deliveries` row,
no `tickets` row. The only database write for a cap breach is the
`throttle_events` row in `internal/throttle/ingress.go` (FR-601).

**Consequence**: a 429'd delivery can be legitimately redelivered later (within
GitHub's 3-day window) and will be processed as a fresh delivery when the bucket
has tokens. The idempotency table is not polluted by 429'd deliveries; redelivery
of a 429'd GUID produces one ticket, as expected (FR-602).

**Consequence**: reversing this order (inserting the delivery row before the rate-
cap check) would mean 429'd deliveries permanently block their GUID from future
processing — a redelivery attempt would return 200 with no ticket (the GUID is
already in `ingress_deliveries` from the capped delivery). This is incorrect; do
not reverse the order.

### Rule 4: The ingress connector is strictly inbound-only

Nothing in the `internal/ingress` package posts back to GitHub, sends mail, calls
any external API, or mutates any external state. The package imports are bounded
to: `net/http` (for the listener and request reading), `crypto/hmac`, `crypto/sha256`,
`crypto/subtle`, `encoding/hex`, `encoding/json` (all stdlib), `internal/store`
(Postgres writes), `internal/throttle` (rate-cap evidence), and `internal/vault`
(secret fetch at boot only). No outbound HTTP client is constructed in this
package.

**Consequence**: a future connector that wants to "acknowledge the webhook" by
posting a comment back to GitHub requires an explicit amendment to this document
and a threat-model review. Outbound actions are M11's Action Broker, not M10's
ingress framework.

**Consequence**: the M10 acceptance script (`scripts/m10-acceptance.sh`) asserts
this boundary: `git grep -r "github.com/google/go-github\|PostComment\|SendMail\|external_action"` under
`supervisor/internal/ingress/` must return empty (SC-007).

### Rule 5: Every ingress-created ticket carries provenance in `tickets.metadata`

Every `InsertIngressTicket` call writes the `ingress_connector`, `external_id`,
and `external_url` keys into `tickets.metadata`. The `origin` column is set to
`'ingress'`. These fields are not optional — they are required columns in the
`InsertIngressTicket` SQL query (built with `sqlc.arg` for each field).

**Consequence**: a connector implementation that omits the provenance keys (e.g.
passes empty strings) degrades the operator observability and the agent-side
security signal. The SQL query's non-null requirement enforces non-empty strings
at the Postgres layer; the connector is required to supply meaningful values.

**Consequence**: any future extension of the `tickets.metadata` provenance shape
(e.g. adding `external_account_id` for multi-org connectors) requires a new `sqlc.arg`
in the query and a corresponding new field in `TicketDraft`. It does not require
amending this document unless the security model of provenance changes.

### Rule 6: The per-connector rate cap is the first line of defense against ticket-creation flooding

The rate cap runs before any Postgres INSERT (step 7 of the pipeline, before step
9). It is not a secondary check or a retrospective throttle — it is the
pre-insert gate that bounds fan-out before the event loop sees the tickets.

The cap parameters (`RatePerMin`, `Burst`) come from connector configuration, not
from runtime negotiation with GitHub or any external system. Default values
(`GARRISON_INGRESS_GITHUB_RATE_PER_MIN=60`, `GARRISON_INGRESS_GITHUB_BURST=30`)
are conservative for normal issue-triage workloads and configurable by the operator.

**Consequence**: raising the default cap values or removing the cap entirely
requires an amendment to this document. The cap is a security and cost control,
not merely a performance hint.

**Consequence**: the M6 per-company cost throttle and M8 per-department weekly
ticket budget compose with the per-connector rate cap as second and third lines
of defense, respectively. None of the three replaces the others.

---

## 5. Per-attack-class mitigation summary

| Attack class | Description | Controls (rule / section references) |
|---|---|---|
| **AC-1: Forged delivery (no valid signature)** | Attacker POSTs a body without a valid `X-Hub-Signature-256`. | Rule 1: fail-closed 401, no database write. §3.1: timing-safe comparison, raw-body-before-parse. Rejection counter incremented. |
| **AC-2: Replayed delivery (captured valid signature)** | Attacker captures and replays a legitimate delivery. | §3.2: unique-constraint dedup on `(connector_id, external_delivery_id)` — 200 with no ticket, no notify. M1-race-safe via insert-time lock, not pre-check SELECT. |
| **AC-3: Concurrent replay (M1 race)** | Two concurrent deliveries of the same GUID, first tx still open. | §3.2: second INSERT blocks on unique-index lock, sees `23505`, rolls back, returns 200. No double-create even under race. |
| **AC-4: Oversized-body bomb** | Attacker POSTs a >26 MB body to exhaust handler memory. | §3.3: `io.LimitReader` at `maxBodyBytes = 26_214_400` caps read. Truncated body fails signature check → 401. |
| **AC-5: Rate bomb (valid signature)** | Attacker obtains the webhook secret and floods valid deliveries. | §3.3, Rule 6: per-connector token bucket cap; over-cap → 429, no DB write. M6 + M8 throttles compose as second/third lines. |
| **AC-6: Payload injection (valid delivery, malicious body)** | Adversarially crafted `issue.title` or `.body` targeting agent prompts. | §3.4: provenance tag (`origin='ingress'`, `metadata` keys) marks tickets as externally sourced. Origin chip is operator-visible. Agent/hygiene layer applies appropriate suspicion. Inbound-only boundary (Rule 4) prevents any round-trip exploitation. |
| **AC-7: Credential theft (webhook secret)** | Attacker obtains the webhook secret from vault or env. | §3.5: secret is vault-stored, never in env or logs. `vaultlog` analyzer enforces at build time. Fail-closed on vault unavailability. Secret rotation = supervisor restart (documented in ops-checklist). |
| **AC-8: Trust-boundary exploitation (dashboard mux)** | Attacker exploits a shared mux to reach the cookie-auth surface from the internet, or to bypass cookie-auth via a webhook bypass hole. | §3.6, Rule 2: separate port (8082) and separate mux for the ingress surface. No shared routes, no middleware bypass. |
| **AC-9: Compromised connector lateral movement** | Attacker with full connector compromise attempts to escalate beyond ticket creation. | §3.7: blast radius is bounded — no direct agent spawn, no vault access, no config mutation, no outbound network, no MCP surface. M6 + M8 throttles bound ticket-creation cost. |

---

## 6. Open questions later milestone specs must resolve

1. **IP allow-list for defense-in-depth.** §3.8 names IP allow-listing as
   optional. A future milestone that ships it must define: dynamic fetch vs.
   static config, TTL, behavior when the GitHub Meta API is unavailable, and
   whether a cache-poisoned allow-list could cause false rejections of legitimate
   deliveries.

2. **Email-in transport model.** Email-in connectors typically require either an
   inbound SMTP listener or IMAP polling. IMAP polling resembles the "no polling
   loops, no scheduled heartbeats" constraint in RATIONALE §1 (resolved as a
   webhook-only M10-core to avoid this tension). The email-in connector spec
   must resolve the transport question — either a provider-side inbound-parse
   webhook (push, compatible with §1) or a deliberate §1 amendment — before code
   lands.

3. **Multi-tenant connector routing.** M10-core is single-tenant. Multi-tenant
   connectors (M13 `project_id` routing) require a rethink of the
   `(connector_id, external_delivery_id)` uniqueness scope — if two projects share
   a connector ID but different delivery spaces, the unique constraint may need a
   `project_id` dimension. This is a deferred concern flagged here.

4. **Webhook secret rotation without restart.** M10-core requires a supervisor
   restart to rotate the webhook secret. A future milestone may add a
   signal-triggered re-fetch (e.g. SIGHUP → refetch + hot-swap the connector's
   `Secret` field under a mutex). This requires an amendment to Rule 6 of the
   vault threat model (secret fetch timing) and an amendment to §3.5 here.

5. **Outbound acknowledgment for M11.** When M11 (Action Broker) ships, connectors
   will need to post replies back to GitHub. That requires a GitHub API client in
   the connector, an installation token, and a vault-stored API key — all of which
   are explicitly out of scope in M10 and require an amendment to Rule 4. The M11
   spec must update both this document and the vault threat model before any
   outbound code lands.

---

## 7. What the M10 retro must answer

When M10 ships, the retro (`docs/retros/m10.md`) documents:

1. **Did signature verification fail closed against a forged delivery?** Were there
   any code paths that could reach a database write without passing through
   `VerifySignature`? Did the chaos tests surface any race in the verification path?

2. **Did idempotency hold under real redelivery and under the M1 concurrent-delivery
   race?** Did `TestIngress_SerialRedelivery_NoSecondTicket` and
   `TestIngress_ConcurrentRedelivery_RaceYieldsOneTicket` pass cleanly? Was the
   unique-constraint dedup the actual signal in both cases?

3. **Did the rate cap actually bound a burst and did the breach write correct M6
   evidence?** Did `TestIngress_BurstExceedsCap_BoundedTickets` confirm that
   ticket count ≤ Burst? Did the `throttle_events` row carry the correct
   `connector_id`, `rate_per_minute`, and `burst` fields?

4. **Did the inbound-only boundary hold?** Did anything in `internal/ingress/`
   post back to GitHub or call any external service? Did the `git grep` assertion
   in SC-007 return empty?

5. **Did `ingress-threat-model.md` precede all connector code in git history?**
   Did the SC-008 git-log assertion in `scripts/m10-acceptance.sh` pass?

6. **Were any Rule violations found in post-ship adversarial review?** (M9 retro
   pattern: post-ship review sometimes surfaces issues the acceptance script
   missed. If any are found, they are patched before the retro task is checked off
   and documented here.)
