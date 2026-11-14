# Release Notes

## Unreleased — L0 orphan-credential diagnostic

- Reports config-removed credentials at every lifecycle (register /
  reconfigure): CommandCode-pattern auth files whose key digest is no longer
  in the config are surfaced through host.log with their digests. These
  entries are live, not cosmetic — the executor takes the upstream key from
  the selected auth's attributes, so a removed key's file keeps being routed
  (and failing against a rotated/revoked key) until an operator deletes it.
  There is no host.auth.delete ABI yet, so this is diagnostic-only and
  best-effort (an AuthList failure never blocks the lifecycle; digest-only
  log fields, FR-011). Tombstone disable remains pending on verifying the
  host's disabled-auth routing semantics.

## Unreleased — P2 audit-fix batch

- Self-classifies Cloudflare bot/WAF challenges: a 403 whose body carries a
  challenge marker ("just a moment", "challenge-platform", "cf-mitigated",
  "attention required") is re-titled `cloudflare challenge: …`, the phrase
  the host's result path matches to apply its short-backoff cooldown
  instead of the default credential-failure burn. The classifier inspects
  the FULL upstream message (the last boundary that still holds it) — the
  redacted 80-char snippet is deliberately NOT widened (FR-011); plain 403s
  keep the auth-failure treatment.
- Carries structured error detail to the host: upstream `Retry-After`
  (delta-seconds form, capped at 1h) now flows into the executor envelope's
  `retry_after_seconds`, and converter terminal failures close downstream
  streams with `error_detail` (pluginabi.Error — code, redacted message,
  HTTP status) which the host's stream bridge honors over the plain error
  string. Older hosts ignore both fields, so nothing depends on their
  presence.
- Redacts the `api-key` / `api_key` header-name variants (no `x-` prefix,
  hyphen or underscore) previously echoed by upstream bodies into error
  messages; the shared idempotency and digit-required value-class semantics
  are unchanged. The debug-trace sink also redacts every line before
  writing, so raw error values logged during diagnosis can no longer carry
  credential material.
- Hardens predictor persistence for overlapping engines: compaction rewrites
  use a unique temp file + fsync + directory fsync (a fixed `.tmp` name made
  concurrent rewrites truncate each other and fail the loser's rename with
  ENOENT), and appends/rewrites serialize through a flock-protected critical
  section — the retired engine's final flush can no longer interleave with
  the fresh engine's compaction when a reconfigure join gives up. Appends
  also fsync, so a hard kill now loses at most one 5-minute flush window
  (unchanged) but never leaves torn metadata after it.
- Hardens the account-email sync: fsync before the atomic rename (and a
  directory fsync after), plus a per-path in-process critical section so
  overlapping refresh ticks read-merge-write serially. New tests pin the
  field-preservation contract (email is the ONLY field that changes) and
  concurrent-writer atomicity.
- Fixes the data-only-frame state machine in the Messages adapter: frames
  whose event type lives only in the payload (`data: {"type":"…"}") were
  converted correctly but never observed — Started/FinishSeen/TerminalSeen
  stayed unset, freezing StreamSnapshot.Progress at 0 so the pump's
  first-data timer killed healthy streams, and making FinalizeStream
  contradict Feed's done=true. Payload-resolved types now feed observation
  too.
- Fixes the terminal-frame one-shot contract: a late `Flush()` after a
  normal `message_stop` termination no longer re-emits a second
  `response.completed` (Flush now honors the same TerminalSeen guard as
  FinalizeStream).
- Fixes the premature finish chunk on the chat-completions target: a
  usage-only `message_delta` no longer closes the stream with
  `finish_reason`; the finish chunk waits for an actual stop_reason.
- Lifecycle fixes: the register timeout now bounds the synchronous work as
  one budget (the catalog refresh no longer gets a second independent 10s
  window after auth materialization spent the first), the predictor build
  runs on a fresh context instead of the spent lifecycle deadline (its
  email-label reads silently bailed on an expired ctx), refreshOnce
  tolerates a nil host bridge on the post-refresh diagnostics paths (a
  successful register with unsupported-model warnings used to panic inside
  the bridge and surface as plugin_error), and the auth-record build error
  now wraps its cause.

## Unreleased — P1 audit-fix batch

- Broadens the billing-exhaustion remap to the status+message signature alone
  (`insufficient credits` on a 400), independent of the provider error code:
  the same depleted-account condition has been observed with code
  `BAD_REQUEST`, with no code at all, and as a generic `invalid_request_error`
  body. The host drops provider codes anyway (only HTTP status + message reach
  its `MarkResult`), so the contract no longer depends on any exact code key.
- Prefixes an upstream `unsupported_model` rejection with the host's
  model-support vocabulary (`unsupported model: …`) when missing: the host
  keeps only the message text and routes model-support failures through its
  vocabulary matcher, landing them in the per-model state instead of
  penalizing the credential; the plain-404 branch would cool for 12 hours.
- Carries explicit host-visible HTTP statuses on error constructors that
  previously returned status 0 (mapped to a generic 500 by the host, cooling /
  rotating a credential for request-scoped failures): `Translation`→400,
  `FromNetwork`→502, `UpstreamFallback`→502, `UnsupportedFormat`→422,
  `UnsupportedPartType`→422.
- Trims configured API key values (`TrimSpace` after `${ENV}` expansion):
  quoted YAML values and block scalars can carry trailing whitespace/newlines,
  and the auth-record identity is the sha256 of the raw value — untrimmed
  variants of one key materialize two credentials (two files, two UI cards)
  for the same account. Whitespace-only values now fall out of the existing
  empty-value check; trimmed duplicates hit the existing duplicate check.
- Catalog refresh now fails over across the configured keys instead of
  pinning to `APIKeys[0]`: when the first key died (401), the model catalog
  stayed stale forever even with healthy keys remaining. First success wins;
  per-key failures log a warn. Total failure still errors.
- Auth-record materialization is now per-key best-effort: one failing
  `host.auth.save` call no longer fails the whole registration (which would
  advertise no models at all); the failure is logged and every other key
  still materializes. Registration still fails when every key fails to save.
- Contains stream-pump panics in the executor goroutine and per-loop panics
  in the predictor's refiller/flusher/sampler loops: both run inside the
  host process as .so plugins, where an unrecovered panic aborts the whole
  CPA pod. Panics are logged through host.log; the predictor's final flush
  and `close(done)` still run so `WaitStopped` is never blocked.

## Unreleased — CPA v8 SDK migration

- Maps CommandCode's billing-exhaustion signal to the host's native cooling
  path. The upstream returns "You have insufficient credits..." as HTTP 400 +
  `BAD_REQUEST` + `invalid_request_error`; neither the v7 nor v8 error
  conductor has a cooldown branch for 400 and the executor path treats it as
  a request fault that also stops credential rotation, so a nearly depleted
  account (e.g. $0.08 left) kept being selected and failing forever, pinned
  harder by session affinity. `ClassifyUpstreamError` now re-exposes that
  exact signature as 402 / `insufficient_credits` / `quota_exhaustion`, the
  status both host generations cooldown (30 min) and rotate across. The
  30-minute heartbeat doubles as top-up detection: purchasing credits
  revives the account within one cooldown window, with no manual re-enable.
  The `errclass.FromStatus` quota vocabulary also gains "insufficient
  credits" so a 402/403/429 variant keeps quota semantics.
- Anchors the SDK at `github.com/router-for-me/CLIProxyAPI/v8` `v8.0.67`
  (previously `/v7` `v7.3.6`) and raises the go directive to `1.26.10`.
  No code path changes: the C ABI version (1) and every wire struct the
  plugin exchanges with the host are unchanged between v7.3.x and v8.0.x.
- Pins the registration contract to schema 6 via a new
  `registeredSchemaVersion` constant (`internal/plugin/plugin.go`), instead
  of following `pluginabi.SchemaVersion`, which jumped 6→10 in the v8 SDK.
  The v8 host accepts plugin schemas ≤ its own (10) and degrades gracefully,
  so a schema-6 registration keeps the exact host negotiation semantics the
  plugin was built and tested against on CPA v7 hosts.

## v0.2.9 — Endpoint routing and first-depletion account identification

- Ports upstream v0.1.3's streamed tool-call closure: the Messages route now
  emits `response.function_call_arguments.done` and `response.output_item.done`
  before `response.completed`, and every synthesized function_call item carries
  an `fc_`-prefixed item id (stream announcements, argument deltas/done, and
  final output) while retaining its original `call_id`. The Chat Completions
  route already closed calls; it now uses the same fc_ item ids. This keeps
  clients that finish a call only after its matching done events and keep only
  fc_ ids (e.g. Pi) from leaving streamed tool calls unfinished. The
  `custom_tool_call` variant keeps call_id-only item identity.
- Reads model `supported_endpoints` and prefers native Responses, then Chat
  Completions, then Messages; explicit route overrides still take precedence.
  Legacy entries without endpoint metadata retain the Chat fallback.
- Forwards the documented `x-cmd-zdr: 1` opt-in and retains provider error
  codes; ZDR-unavailable and plan-upgrade errors are non-retryable.
- First depletion now names the corresponding account, marks its prediction
  card, and offers keyboard-accessible scroll/focus/highlight navigation.
  Includes English/Chinese copy and a dark-theme contrast adjustment.
- Fixes quota-auth test helpers' cross-VM prototype comparisons without
  changing production authentication behavior or weakening strict assertions.

Local artifacts are built from the current working tree, including existing
opt-in tool-argument diagnostics. They are not a published/tagged release.

## v0.2.8 — Prediction readability and purchased-credits fixes

Presentation and verdict fixes shared with the ollama plugin's predictor
package; no sampling cadence or estimation changes.

- Prediction page windows now speak the quota page's language: "Monthly
  credits / 5-hour window / Weekly window / Purchased credits" (zh:
  月度额度 / 5 小时窗口 / 每周窗口 / 已购积分). Every window also annotates
  its headline value with a "left"/"剩余" suffix — the numbers are
  remaining values, the complement of the used-percentage shown on the
  quota page, and the raw "95%" read as the opposite until now.
- An empty purchased balance — the steady state for accounts that never
  bought packs — no longer renders as "exhausted · waiting for reset".
  Purchased credits are money without a reset cycle, so the row reads
  neutral ("none"/未使用).
- The account verdict follows the vendor's combined pool: an empty monthly
  window no longer marks the account exhausted while purchased credits
  still hold balance — the account keeps burning purchased credits until
  both sides are empty. The 5-hour and weekly percent gates keep their
  existing precedence.

## v0.2.7 — Usage prediction port (/prediction contract)

- Ports the usage-prediction engine, prediction page, and cross-type tab from
  the ollama-cliproxyapi plugin: `POST /v0/management/plugins/commandcode-go-cliproxyapi/prediction`
  returns store-only snapshots (account cards, the `commandcode` fleet view,
  optional deadline evaluation, and the peers list); `GET /v0/resource/.../prediction`
  serves the page. Account reads never touch the account surface — sampling
  runs on a separate budget (default 10m cadence, 8 calls/min, 7d history,
  48h horizon).
- Mapping: the monthly plan allowance is the Included window
  (remaining / max(allowance, remaining), subscription period end as reset);
  the 5-hour and weekly limits map to percent gates (exceeded reads 0,
  capless gates are omitted rather than invented); purchased credits record
  as the billing channel. The account surface exposes no hourly buckets, so
  the burn profile is flat-rate. Plans outside the built-in table render
  dark instead of showing a ratio without a denominator.
- New `predictor.*` config keys (`enabled`, `sample-interval`,
  `budget-per-minute`, `history`, `horizon`, `persistence`, `data-dir`,
  `peer-plugins`); persistence flushes NDJSON to `data-dir` (default
  `<home>/.cli-proxy-api/usage-predictor`, `off` = memory-only), restores on
  restart, and surfaces degradation states on the page.
- The prediction page cross-type tab aggregates sibling plugins implementing
  the same `/prediction` contract (discovered from the management plugin
  list, or pinned via `predictor.peer-plugins`), degrading to the
  single-type view when none answer.

## v0.2.6 — In development

This version is not published. Development builds report `0.2.6-dev.1`; the
existing v0.2.5 tag and its source remain unchanged.

- Adds manual **Refresh all** to the quota page. Batch refresh and per-card
  refresh share one queue capped at two concurrent requests; first-time cards
  without email metadata use the same limit. Progress reports `done/total` and
  completion reports exact succeeded/failed counts. Failed cards retain cached
  quota values and their cached timestamp. The page does not poll.
- The quota refresh behavior is covered by deterministic VM tests with fake
  host/account calls; this is not a new full-Manager browser validation claim.

- Adds independent stream deadlines: first meaningful data (60s), idle after
  meaningful progress (3m), absolute total duration (30m), and finish grace
  (15s). `request-timeout` no longer bounds streams; it continues to govern
  non-stream requests and quota/account calls as before.
- Only parsed protocol progress refreshes idle time. Comments, ping frames,
  whitespace, partial bytes, duplicate start/item frames, and unchanged usage
  do not keep a stream alive. The total deadline is never extended.
- Finish grace waits briefly for usage/DONE. A complete finish can finalize
  safely; malformed/incomplete tools fail. If grace expires before a usage
  trailer, a valid terminal may necessarily contain the usage observed so far.
- Clean EOF without finish/terminal evidence is a truncated-stream error. This
  does not recover upstream disconnects or manufacture successful output.
- Emits one secret-free `commandcode stream finished` record per started stream
  with route/source, fixed cause, timing milestones, byte/chunk counts, terminal
  evidence, and EOF status. Successful terminal/eof/finish-grace records are
  info; all error/timeout causes are warn. This controller affects only plugin
  streams and does not modify CPA itself.

## v0.2.5 — CPA v7.3.15 / Manager 1.13 compatibility and Responses fixes

v0.2.5 depends on CLIProxyAPI SDK v7.3.6 and is integration-tested against the
CPA v7.3.15 host (native plugin ABI 1, schema 6) and Management Center 1.13.x.

- Fixes file-backed CommandCode auth identity by deferring ID canonicalization
  to CPA. Initial `host.auth.save`, watcher reparses, atomic email updates,
  reconfiguration, and watcher restart now retain one ID containing `.json` and one
  auth index. JSON `id`, API key, email, filename, and storage JSON are retained;
  non-file parser callers keep the prior JSON-id/filename fallback behavior.
- Adds a real CPA v7.3.15 watcher regression with a coexisting fake Codex auth;
  it checks unique Manager 1.13 `name + NUL + auth_index` row keys and verifies
  the unrelated auth's identity and request-history buckets are unchanged.
- Fixes the local `custom_tool_call` 400 on the Chat Completions route. Flat
  Responses custom tools now use an `{input:string}` transport function and
  are restored from request-local provenance in non-stream and stream output;
  call/output replay preserves `call_id`. Ordinary functions are not inferred
  to be custom from argument shape. Flat custom text is supported in both
  modes; default `cpa` mode also downgrades custom grammar to freeform text,
  while `strict` rejects grammar because its constraint cannot be preserved.
- Adds P1 one-level Responses namespace tools, `additional_tools`, namespace
  tool choice/replay, strict/schema preservation, and JSON Schema response
  format mapping on the Chat route. One-level namespaces are supported in both
  modes; nested namespaces, collisions, ambiguous bare names, and names over
  the 64-byte transport limit remain safety errors. Hosted built-ins and custom
  grammar are skipped/downgraded by default and rejected only in `strict` mode.
  Schema/strict retention is a protocol guarantee, not a model-enforcement guarantee. See
  `docs/protocol-capabilities.md` for the precise matrix and option policy.
- Changes the default Responses-to-Chat policy to CPA v7.3.15 compatibility:
  unsupported hosted built-in declarations/items are skipped, builtin-only
  tool choices disappear with the empty tools list, and custom grammar is
  transported as freeform text without claiming grammar enforcement. The
  explicit YAML setting `responses-compatibility: strict` retains dev.3's
  pre-upstream rejection of unsupported/stateful semantics.
- Fixes synthesized Responses streaming for stateful SDK helpers. Text streams
  now emit CPA-compatible created/in-progress, content-part, delta, done, item
  and completed lifecycles with stable response/message IDs and monotonic
  sequence numbers. This was reproduced against openai-node 4.104.0: dev.4's
  raw iterator completed, but `responses.stream(...).finalResponse()` failed on
  the missing created `output`; dev.5 passes both. This does not identify the
  user's unknown LiteLLM version or prove it was the only spinner cause.
- Adds CPA's native read-only QuotaProvider while retaining the legacy quota
  page and API. Both paths share the same CommandCode account fetch logic.
- Native quota validates persisted CommandCode credential identity before using
  an API key. Caller-selected provider fields alone are not trusted; runtime
  auth lookup/physical auth retrieval verifies credential ownership. Reset is
  unsupported.
- Untagged CI builds report `0.2.5-dev`; the `v0.2.5` tag reports `0.2.5`.
  Release archives include a provenance JSON containing version, commit,
  archive name, and archive SHA-256.
- Quota reset, OAuth login, and token counting remain unsupported.
- The legacy quota page supports Management Center's `enc::v1::` and
  `enc::v2::` remembered credentials only on the same origin. Edge 148
  acceptance passes 9/9 iframe fixture cases, including a cross-origin refusal
  that sends no request. The actual CPAMP 1.13.0 release asset (SHA-256
  `5974f172549a5d5f5b34a2c9a9c1e60f413bc963f799e430aacd4dd8e0a4eaac`,
  source `ff361648f0b6d54bb678ae555cd86169142635d0`) passes 3/3 real-frontend
  checks from login/Remember password through v2 storage, plugin menu/resource
  iframe, fake Bearer authorization, and rendered quota percentages.
- Management authentication supports plaintext and `enc::v1::`/`enc::v2::`
  local storage decoding on the same origin, with redacted diagnostics for
  missing, malformed, unauthorized, and forbidden states. No credential is
  sent when the iframe's own storage has no usable credential; this is not an
  explicit cross-origin access-control mechanism.
- Release CI injects version/commit metadata and emits checksums plus provenance
  JSON. The CPA host harness uses owned temporary directories and preserves
  pre-existing source/auth files during cleanup.

The browser acceptance uses mocked CPA HTTP and CommandCode upstream responses.
The independent native test loads the shared library through CPA v7.3.15's real
pluginhost and auth-directory watcher, but does not launch the complete CPA
server. Real CommandCode production requests and user deployment topology
remain release-acceptance requirements.

**Known boundaries**

- The plugin does not execute hosted `code_interpreter`, web/file search, or
  computer tools. Default CPA compatibility may skip those declarations, and
  custom grammar is a freeform-text downgrade rather than grammar enforcement.
- Stateful Responses storage/background/conversation semantics are not
  implemented. Use `responses-compatibility: strict` to reject degradation.
- Management Center's generic credential-quota list is not adapted for this
  plugin. Use the native read-only QuotaProvider or the legacy plugin quota
  page; reset remains unsupported.
- SDK 4.104 compatibility is reproducible for the covered text/function stream
  lifecycle, but the user's production LiteLLM version and long-running
  upstream behavior were not observed, so no single production spinner root
  cause is claimed.

**Upgrade and rollback**

1. Pin CPA to v7.3.15, Management Center to 1.13.x, and plugin v0.2.5, then stop CPA.
2. Move the old plugin binary to a backup location outside all plugin scan
   directories; leaving an old native file in a scanned directory can make it
   selectable.
3. Verify `checksums.txt`, the archive SHA-256, and the adjacent provenance JSON.
4. For manual installation with `store.version: "0.2.5"`, install the extracted
   library as `commandcode-go-cliproxyapi-v0.2.5.<platform extension>` (the store
   installer handles versioned naming automatically),
   restart CPA, and verify registered version `0.2.5`. Existing auth JSON is
   retained; do not recreate credentials during the upgrade.
5. To roll back, stop CPA, remove the new runtime, restore the external backup,
   and restart.

## v0.1.1 — Accept array function_call_output results

Responses `function_call_output.output` may be a string or an array of text
parts (`input_text`, `output_text`, or `text`). The array shape is flattened
into the string Chat Completions and Messages already send upstream. Non-text
parts are still rejected.

**Upgrade notes**

- Replace the plugin binary and restart CLIProxyAPI.

## v0.1.0 — CommandCode Go/GOAT/Pro/Max provider

First release of the plugin as a CommandCode Go/GOAT/Pro/Max provider, derived from
opencode-go-cliproxyapi v0.1.7.

**Adaptations**

- **Reasoning preservation**: upstream `reasoning`, `reasoning_details[].text`
  and `reasoning_content` are read together (mirrored values counted once) and
  emitted as a leading Claude `thinking` block, a leading Responses `reasoning`
  item with summary events, or a backfilled `reasoning_content` on the OpenAI
  passthrough — streaming and non-streaming.
- **Chat-completions-only routing**: every catalog model defaults to
  `{base-url}/chat/completions`, because CommandCode's `/messages` endpoint
  accepts Claude models only and `/responses` is not registered. The family
  prefix table was removed; `route-overrides` remain for other upstreams.
- **Effort passthrough**: models without thinking metadata (all CommandCode
  models) are no longer validated or clamped locally — `xhigh`/`max` reach the
  upstream verbatim, `auto`/`none` omit the field. A model that declares levels
  or bounds opts back into eager validation.
- **Quota page against the real account API**: cards show account email, plan,
  remaining plan credits, and the rolling 5-hour/weekly windows from
  `/alpha/billing/credits`, `/alpha/billing/subscriptions` and `/alpha/whoami`.
- Defaults renamed (`commandcode` prefix, `api.commandcode.ai/provider/v1`
  base URL, `commandcode-go-cliproxyapi` plugin id).

**Upgrade notes**

- The provider namespace changed, so client model ids must be updated
  (for example `commandcode/deepseek/deepseek-v4.1-flash`).
- Replace the plugin binary, restart CLIProxyAPI, and hard-refresh the
  Management Center.
