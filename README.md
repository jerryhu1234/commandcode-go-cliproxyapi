# CommandCode Go/GOAT/Pro/Max CLIProxyAPI Plugin

**English** | [简体中文](README.zh-CN.md)

A native dynamic Go plugin for [CLIProxyAPI](https://help.router-for.me/plugin/development) that exposes a **CommandCode Go/GOAT/Pro/Max** plan as a single provider (`commandcode`; published model ids carry the `commandcode/` prefix).

The plugin owns model discovery, protocol translation, execution, key scheduling, and a quota page for the CommandCode account surface, so one API-key pool serves OpenAI, Anthropic, and Responses clients through CLIProxyAPI.

## What the upstream actually serves

CommandCode Go/GOAT/Pro/Max is reached through its OpenAI-compatible provider surface:

| Upstream route | Status |
|---|---|
| `POST {base-url}/chat/completions` | Serves the OSS models (`deepseek/*`, `z-ai/*`, `Qwen/*`, `MiniMaxAI/*`, `moonshotai/*`, …) |
| `GET {base-url}/models` | Model catalog |
| `POST {base-url}/messages` | Claude models only — OSS ids are rejected with *"Model … is not supported on this endpoint"* |
| `POST {base-url}/responses` | Not a registered route |

The default upstream base URL is `https://api.commandcode.ai/provider/v1`.

Because there is exactly one usable route for the catalog, **every model discovered from `/models` is routed to chat-completions by default**, whatever its family or vendor prefix. `route-overrides` exist for upstreams that really do serve another protocol on another endpoint.

## The Problem

Without this plugin, using a CommandCode Go/GOAT/Pro/Max plan in CLIProxyAPI requires hand-written provider blocks per protocol family, and the pieces that CLI clients depend on are missing or wrong:

- **Duplicated configuration & keys**: the same keys must be configured in several provider blocks.
- **Fragmented scheduling**: rotation, rate limits, and cooldowns cannot be shared, and the Management Center cannot show per-key usage.
- **Client protocol burden**: clients must know which upstream endpoint a model requires.
- **Lost reasoning**: CommandCode returns thinking text under `reasoning` / `reasoning_details[].text` (never the standard `reasoning_content`), so a plain OpenAI-compatible provider silently drops every chain-of-thought for Anthropic and Responses clients.
- **Rejected reasoning effort**: CommandCode's `/models` carries no thinking metadata, so a provider that validates `reasoning_effort` locally rejects `xhigh`/`max` even though the upstream accepts them.

## The Solution

- **Unified auth pool**: the plugin registers each configured key as a CLIProxyAPI auth record, so the host's scheduler handles rotation, retries, error cooldowns, session affinity, and per-key statistics.
- **Transparent translation & routing**: clients ask for `commandcode/<upstream-id>` and never need to know the upstream protocol.
- **Reasoning fidelity**: upstream thinking text is preserved for all three client formats, and `reasoning_effort` is forwarded verbatim when the model declares no capability.
- **One model catalog**: everything published by `{base-url}/models` is discoverable under the `commandcode/` prefix in `/v1/models`.

## Features

- **Single Provider Namespace**: models appear as `commandcode/deepseek/deepseek-v4.1-flash`, `commandcode/z-ai/glm-5.3-flash`, … (prefix configurable, can be disabled for bare upstream ids).
- **Multi-Protocol Client Translation**: OpenAI Chat Completions, Anthropic Messages, and OpenAI Responses requests all become upstream chat-completions calls, and responses are converted back — including streaming.
- **Reasoning Preservation**: upstream `reasoning`, `reasoning_details[].text`, and `reasoning_content` are normalized across targets:
  - Claude clients get a leading `thinking` block with `thinking_delta` events;
  - Responses clients get a leading `reasoning` output item with `response.reasoning_summary_*` events;
  - OpenAI clients get `reasoning_content` backfilled onto each chunk while the vendor fields stay intact.
- **Capability-aware Reasoning Controls**: an explicit `route-overrides` declaration (or catalog thinking metadata) re-enables eager validation and clamping; with nothing declared, the client's `reasoning_effort` is forwarded verbatim (`auto`/`none` omit the field) and the upstream is the authority.
- **Dynamic Catalog Discovery**: remote catalog with local fallback, deduplication, and diagnostics for what was excluded.
- **Native and legacy quota**: CPA 7.3.15's native QuotaProvider and the legacy Management Center page share the same account fetch and normalization logic. Reset is not supported.
- **Multi-Key Auth Scheduling**: one key pool shared across all protocols through CLIProxyAPI's native scheduler.

## Requirements

- **CLIProxyAPI**: `v7.3.15` is the validated host for v0.2.5 (plugin ABI 1, schema 6); the plugin SDK dependency remains v7.3.6
- **Management Center**: `1.13.x`
- **Go Toolchain**: Go 1.26.7 (CGO enabled for `-buildmode=c-shared`)

## Build

```bash
# Linux (AMD64)
go build -buildmode=c-shared -o plugins/linux/amd64/commandcode-go-cliproxyapi.so .

# macOS (ARM64)
go build -buildmode=c-shared -o plugins/darwin/arm64/commandcode-go-cliproxyapi.dylib .

# Windows (AMD64)
go build -buildmode=c-shared -o plugins/windows/amd64/commandcode-go-cliproxyapi.dll .
```

Place the artifact into the host's plugin directory (e.g. `<cliproxyapi_root>/plugins/<os>/<arch>/`).

## Configuration

```yaml
plugins:
  enabled: true
  dir: /data/plugins
  configs:
    commandcode-go-cliproxyapi:
      # Upstream base URL (default: "https://api.commandcode.ai/provider/v1")
      base-url: "https://api.commandcode.ai/provider/v1"

      # Optional catalog override (default: "{base-url}/models")
      # catalog-url: "https://api.commandcode.ai/provider/v1/models"

      # Client-facing model id prefix
      model-prefix:
        enabled: true              # true -> "commandcode/<model>" (default: true)
        value: "commandcode"

      # CommandCode API keys (at least one required); supports ${ENV_VAR}
      api-keys:
        - value: "user_xxx"
        - value: "${COMMANDCODE_API_KEY}"

      # Catalog discovery
      catalog:
        refresh-interval: "15m"           # minimum "1m"
        stale-while-unavailable: true

      # Protocol switches: disabling one removes every model routed to it
      protocols:
        chat-completions: true
        messages: true
        responses: true

      # Responses -> Chat policy: CPA-compatible degradation by default;
      # use "strict" to reject stateful/hosted semantics that cannot be preserved.
      responses-compatibility: "cpa"     # cpa | strict

      # Per-model route pins; only needed when the upstream serves another
      # endpoint (CommandCode itself serves OSS models on chat-completions)
      route-overrides:
        "claude-sonnet-5":
          protocol: "messages"            # chat-completions | messages | responses
          endpoint: "/v1/messages"        # required, must start with "/"

      request-timeout: "5m"
      max-response-bytes: 67108864        # 64 MiB
      allow-http: false                   # http:// base-url for local testing
```

### Configuration Options

| Option | Type | Default | Description |
|---|---|---|---|
| `api-keys` | `[]object` | *(Required)* | List of API keys (`- value: "..."`). Supports `${ENV_VAR}` expansion. Duplicates and empty values are rejected. |
| `base-url` | `string` | `https://api.commandcode.ai/provider/v1` | Upstream provider base URL. Valid HTTPS (or HTTP with `allow-http: true`), no query, fragment, or userinfo. |
| `catalog-url` | `string` | `{base-url}/models` | Catalog discovery URL. |
| `model-prefix.enabled` | `bool` | `true` | Client-facing ids use `<prefix>/<model>`; `false` publishes bare upstream ids. |
| `model-prefix.value` | `string` | `commandcode` | Provider prefix. |
| `catalog.refresh-interval` | `duration` | `15m` | Catalog polling cadence (minimum `1m`). |
| `catalog.stale-while-unavailable` | `bool` | `true` | Keep serving the last good snapshot when a refresh fails. |
| `protocols.*` | `bool` | `true` | Route kill switches. A disabled protocol excludes its models with a diagnostic. |
| `responses-compatibility` | `string` | `cpa` | `cpa` follows CPA v7.3.15 degradation rules; `strict` rejects unsupported/stateful Responses semantics before upstream. |
| `route-overrides` | `map` | `{}` | `{ model: { protocol, endpoint } }` pins a model onto another upstream route. `endpoint` is required. |
| `request-timeout` | `duration` | `5m` | Upstream HTTP timeout (also bounds account/quota calls to 30s). |
| `max-response-bytes` | `int64` | `67108864` | Maximum non-streaming response body size. |
| `allow-http` | `bool` | `false` | Permit `http://` upstreams for local testing. |

### Quota page
The `CommandCode Quota` page (Management Center → plugins) reads the account surface on the same authority as `base-url`:

| Endpoint | Purpose |
|---|---|
| `GET {authority}/alpha/billing/credits` | Remaining plan credits and the 5-hour/weekly windows |
| `GET {authority}/alpha/billing/subscriptions` | Plan id/status |
| `GET {authority}/alpha/whoami?limits=1` | Account email for the card label |

`{authority}` is derived from `base-url` by trimming its provider path (`/provider/v1`). Each card is refreshed manually and independently; the page never polls, and quota values never influence routing.

The legacy page must share Management Center's credential storage origin. It reads remembered credential formats `enc::v1::` and `enc::v2::`; when credentials are unavailable in the iframe's own storage, no quota request is sent. This is a storage requirement, not an explicit cross-origin access-control mechanism. Quota queries do not reset provider limits; the legacy page may synchronize account email metadata. Quota reset, OAuth login, and token counting remain unsupported.

## Upgrade to v0.2.5

v0.2.5 targets the CPA v7.3.15 host and Management Center 1.13.x validation matrix. Pin the installed plugin version to `0.2.5`. See [the protocol capability matrix](docs/protocol-capabilities.md) for the exact custom tool, namespace, structured-output, and option-policy boundaries.

The Chat Completions route now preserves flat Responses custom tools such as
`apply_patch` across request, streaming/non-streaming call, and subsequent
`custom_tool_call_output` replay. Custom identity comes only from the original
request; an ordinary function is never guessed to be custom from its arguments.
By default (`responses-compatibility: cpa`) Responses-to-Chat conversion follows
CPA v7.3.15: unsupported hosted built-ins are skipped, and custom grammar is
downgraded to freeform text rather than executed. Set
`responses-compatibility: strict` in the plugin YAML to retain the dev.3
fail-closed policy for unsupported/stateful options. Neither mode makes this
plugin a hosted code interpreter, search service, file store, or computer tool.

1. Stop CLIProxyAPI. Do not replace a loaded native library in place.
2. Back up the existing plugin file outside every configured plugin scan directory. A backup left under `plugins/`, including an old versioned `.so`/`.dll`/`.dylib`, may still be discovered.
3. Verify the archive checksum, extract it, and confirm the runtime filename is `commandcode-go-cliproxyapi.<platform extension>`.
4. Compare the adjacent `*.provenance.json` version, commit, archive name, and SHA-256 with the selected release artifact.
5. For a manual installation pinned with `store.version: "0.2.5"`, install the extracted library as `commandcode-go-cliproxyapi-v0.2.5.<platform extension>`; the store installer handles this naming automatically. Replace the old runtime while CPA is stopped, then restart CPA and verify the registered plugin reports version `0.2.5`. Existing auth JSON is retained; do not recreate credentials as part of the upgrade.
6. If rollback is needed, stop CPA, remove the new file, restore the backed-up runtime from outside the scan directory, and restart.

### Reasoning effort

CommandCode publishes **no capability API**: `{base-url}/models` returns only `id`, `object`, `created`, `owned_by`, `name` and `context_length`, and its `/alpha/*` account surface has no models or capabilities route. The per-model effort lists that exist live inside the vendor's own clients (the `command-code` CLI and the web app both ship a static table), and the upstream gateway itself accepts every value in the union `low | medium | high | xhigh | max` regardless of the per-model list.

Consequences for this plugin:

- with nothing declared in the catalog, `reasoning_effort` is forwarded verbatim (`auto`/`none` omit the field) so `xhigh` and `max` work;
- if a catalog entry ever carries a `thinking` object (`levels`, `min`, `max`, `zero_allowed`, `dynamic_allowed`), that declaration takes over and eager validation plus budget clamping are re-enabled;
- the vendor's per-model table, the probes behind these statements and the evidence for the missing capability API are recorded in [`docs/model-capabilities.md`](docs/model-capabilities.md).

## Testing

```bash
go test ./...
go test ./.github/scripts
go test -race ./internal/plugin
go vet ./...

# Real CPA v7.3.15 dynamic-loader and watcher integration. All paths are caller supplied;
# HTTP(S)_PROXY may be set on this command if the CPA checkout needs downloads.
CPA_SOURCE=/path/to/CLIProxyAPI-v7.3.15 \
GO_BIN=/path/to/go1.26.7/bin/go \
CPA_HOST_WORK=/tmp \
GO_WORK_ROOT=/tmp \
bash tests/cpa_host_integration.sh
```

`CPA_HOST_WORK` and `GO_WORK_ROOT` are parent directories, not disposable
workspace paths. The script creates uniquely named child directories with
`mktemp` and removes only those children. It also creates an exclusive temporary
subdirectory inside `CPA_SOURCE` for the Go harness so internal CPA packages are
importable; pre-existing checkout files are never overwritten or removed.

### Browser acceptance

The quota page has two real-browser acceptance layers. Both use only fake
management credentials and mocked CPA HTTP/upstream responses; neither sends a
real CommandCode request.

1. `tests/quota_page_browser_harness.js` was run in Microsoft Edge 148 with an
   isolated browser profile. Its iframe fixture passes 9/9 cases: plaintext,
   `enc::v1::`, and `enc::v2::` auth; missing and malformed storage; redacted
   401/403 errors; cross-origin refusal with no request; and recovery after
   sign-in plus iframe refresh.
2. `tests/quota_page_full_manager_browser_harness.js` runs the actual CPAMP
   1.13.0 release `management.html`, SHA-256
   `5974f172549a5d5f5b34a2c9a9c1e60f413bc963f799e430aacd4dd8e0a4eaac`
   (source revision `ff361648f0b6d54bb678ae555cd86169142635d0`). It passes
   3/3 checks through the real login form and Remember password checkbox:
   persisted v2 storage, plugin discovery/menu and the real plugin resource
   iframe route, then fake Bearer authorization and rendered 25%/50% quota
   values.

Install `playwright-core` outside this repository, launch a Chromium-compatible
browser with remote debugging and an isolated profile, then pass its CDP URL:

```bash
NODE_PATH=/path/to/external/playwright-core/node_modules \
node tests/quota_page_browser_harness.js \
  --cdp http://127.0.0.1:PORT

NODE_PATH=/path/to/external/playwright-core/node_modules \
node tests/quota_page_full_manager_browser_harness.js \
  --cdp http://127.0.0.1:PORT \
  --manager /path/to/verified/cpamp-1.13.0/management.html
```

The full-Manager harness is a real frontend release acceptance test, but its CPA
HTTP endpoints and CommandCode upstream are mocked. Separately,
`tests/cpa_host_integration.sh` loads the built shared library through the real
CPA v7.3.15 pluginhost and auth-directory watcher. It validates canonical
file-backed identity across initial save, email atomic replacement,
reconfiguration, and watcher restart, plus registration, execution, quota, management
routes, and shutdown; it is not a complete CPA server process. Real CommandCode
production traffic and a user's deployed CPA/Manager topology still require
deployment acceptance testing.

## Provenance

This plugin was built with reference to two prior CommandCode plugins:

- [opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi) (v0.1.7) is the direct ancestor: the adapter kernel, catalog, config, auth, and quota scaffolding come from there, and the CommandCode-specific behaviour — reasoning preservation, unattributed-capability effort passthrough, the chat-completions-only route default, and the account API used by the quota page — was adapted on top.
- [cpa-plugin-commandcode](https://github.com/ahoo/cpa-plugin-commandcode) (ahoo) is an independent earlier CommandCode plugin whose write-up established the vendor behaviours this plugin had to reproduce: thinking text arriving under `reasoning` / `reasoning_details[].text` and never `reasoning_content`, the resulting need to backfill `reasoning_content` before CLIProxyAPI's openai→claude translator sees it, the SSE `data: ` prefix that streaming `/v1/messages` requires, and the fact that only fully-qualified vendor names (`deepseek/deepseek-v4.1-flash`) are accepted upstream.

Repository: <https://github.com/mczhoucn/commandcode-go-cliproxyapi>

## License

[MIT](LICENSE)
