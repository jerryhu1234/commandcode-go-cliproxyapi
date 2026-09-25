# Release Notes

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
