# Protocol capability matrix

This matrix describes the `0.2.0-dev.5` development target with CPA v7.3.15.
It distinguishes protocol translation from capabilities actually hosted by CPA,
CommandCode, or this plugin. Skipping a CPA built-in translator is not evidence
that the plugin provides the skipped hosted service.

| Capability | CPA v7.3.15 built-in OpenAI-compatible/Codex paths | CommandCode plugin Chat route | Native Responses route |
|---|---|---|---|
| Ordinary function tools | CPA-dependent | Supported | Passed to upstream |
| Flat Responses custom tools | CPA Codex translator has its own support | Supported through `{input:string}` transport | Passed to upstream |
| One-level namespace function/custom tools | CPA translator-dependent | Supported as deterministic `namespace__local` transport names and restored from request-local identity | Passed to upstream |
| Nested namespace, flattened collision, ambiguous bare local name, or transport name over 64 bytes | Not assumed | Explicitly rejected before upstream | Upstream-owned |
| `strict` and JSON Schema structured output | Translator-dependent | Fields and raw schema are preserved; this is **not** a guarantee that the selected upstream model enforces the schema | Upstream-owned |
| Custom `format` absent/null/text | Translator-dependent | Supported | Upstream-owned |
| Custom grammar format | CPA Responses→Chat converts it to freeform `{input:string}` | Default `cpa` mode does the same; grammar is not executed. `strict` rejects it | Upstream-owned |
| Hosted `code_interpreter`, `web_search`, `file_search`, `computer` | CPA Responses→Chat skips these declarations | Default `cpa` mode skips them; `strict` rejects. The plugin does not host them | Upstream-owned, not provided by the plugin |
| Messages/custom symmetric conversion | CPA-dependent | P2 not implemented; do not assume symmetry | Not applicable |

## Responses-to-Chat compatibility modes

`responses-compatibility: cpa` is the default. It follows the observed CPA
v7.3.15 converter: state/storage/cache/tier/truncation/include/max-tool fields
that have no Chat mapping are ignored, hosted builtin declarations and builtin
history items are skipped, and custom grammar constraints are discarded while
the custom input transport remains available. Mixed requests retain supported
function/custom tools. If all declarations are skipped, their required/named
tool choice is also omitted; with surviving tools, CPA can preserve even a
builtin-shaped named choice, so this mode does not claim semantic validation.

`responses-compatibility: strict` retains the fail-closed dev.3 policy below.

In strict mode the cross-protocol Chat route accepts `metadata` objects as a no-op and accepts
string cache hints, `user`, and `safety_identifier` as documented no-ops. It
accepts `store:false`, `background:false`, `service_tier` `auto`/`default`,
`truncation:"disabled"`, and an empty `include`. Stateful or unenforceable
options are rejected before upstream: true store/background, non-empty
`previous_response_id` or `conversation`, non-empty include, non-null
`max_tool_calls`, automatic truncation, and non-default service tiers.

The policy validates this explicit matrix only. Unknown future semantic fields
are not comprehensively validated, so this document does not claim that every
Responses field is translated or retained on a cross-protocol request. Native
Responses requests remain upstream-dependent and are not subjected to this
cross-protocol policy.

Credential identity, Management Center display, and native/legacy quota
behavior are independent of these protocol changes and remain as documented in
the main README and release notes.
