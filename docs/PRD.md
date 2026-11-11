# PRD — Router-Go

Rewrite of **XRouter-Next** (Next.js, `~/9router/XRouter-Next`) in **Go**, with a React dashboard embedded into the Go binary.
Go implementation reference: <https://github.com/luqman-v1/9router-go> (used as a structural reference, not forked).

Document status: **v1 — 2026-11-09 (phase 1 implemented)**. The porting checklist lives in [`PORTING_CHECKLIST.md`](./PORTING_CHECKLIST.md) and is updated each time a feature is finished.

---

## 1. Background & goals

XRouter-Next is a local AI gateway: a single OpenAI-compatible endpoint (`/v1/*`) that routes to 40+ providers, plus a Next.js dashboard. The problem: it needs a Node/Next runtime, uses a lot of memory, and starts slowly.

**Router-Go goals**
1. A single Go binary (no Node at runtime) that serves the API gateway **and** the dashboard.
2. Data compatible with XRouter-Next (same SQLite schema) so migration needs no conversion.
3. A new dashboard that is clean, fast, and consistent (dark/light).
4. Incremental port; phase 1 covers only the **OpenAI-compatible custom provider**.

**Phase 1 non-goals**: OAuth providers (Claude, Codex, Kiro, etc.), format translators (Claude/Gemini), combo, tunnel, MITM, CLI tools, media (image/audio/video), SSO (OIDC/SAML), cloud sync, tray/CLI launcher.

## 2. Technical decisions

| Aspect | Decision | Rationale |
|---|---|---|
| Backend | Go (see go.mod), `net/http` + `chi` v5 | Same as the reference, lightweight router |
| SQLite | `modernc.org/sqlite` (pure Go, no CGO) | Easy Windows/Linux cross-compilation |
| DB schema | **Identical** to `src/lib/db/schema.js` of XRouter-Next (tables `settings`, `providerConnections`, `providerNodes`, `apiKeys`, `usageHistory`, `kv`, etc.) | Compatible with existing data |
| Dashboard auth | bcrypt hash in `settings.password`, JWT HS256 in the `auth_token` cookie (24 hours), per-IP login limiter | Same behavior as Next |
| Frontend | React 19 + TypeScript + Vite + **TanStack Query** + Tailwind v4 + react-router | As requested |
| Embed | `//go:embed web/dist`, SPA fallback to `index.html`, hashed assets `immutable` | Single binary |
| Data location | `~/.router-go` (Linux/macOS), `%USERPROFILE%\.router-go` (Windows). Override: `ROUTER_GO_DATA_DIR` / `DATA_DIR`. DB: `<data>/db/data.sqlite` | As requested |
| Backup | `GET /api/backup` downloads `{format:"router-go-backup",version:1,tables}` (settings, providerConnections, providerNodes, proxyPools, apiKeys, combos, kv; no usage tables). `POST /api/restore` (JSON only) replaces every table present in the file in one transaction and keeps this machine's `port` / `localOnly` | Move a setup between machines; the file holds secrets in clear text |
| Bind | All interfaces by default. **Settings → Local only** (`settings.localOnly`) binds `127.0.0.1` and applies live; `HOSTNAME_BIND` / `ROUTER_GO_ADDR` win over it | Local-only is opt-in |
| Port | Default `20128` (same as XRouter-Next). Priority: `--port` > env `PORT`/`ROUTER_GO_ADDR` > `settings.port` > default. Changing it from Settings moves the listener live (the new port is bound first; port in use → 409, the old listener is closed after 1 s) | Existing clients keep working |

### Repo structure

```
Router-Go/
├── cmd/router-go/main.go        # entrypoint
├── internal/
│   ├── provider/                # built-in providers: provider.go (types + registry), builtin/ (imports all), <name>/ one package per provider
│   ├── config/                  # env, data dir, JWT secret
│   ├── db/                      # open sqlite, schema bootstrap, repos (settings, nodes, connections, keys, usage)
│   ├── auth/                    # bcrypt, JWT cookie, login limiter, API key gen/validate
│   ├── gateway/                 # /v1/* : model resolve, account fallback, upstream proxy, SSE passthrough
│   └── api/                     # /api/* dashboard handlers + router
├── web/                         # React SPA (src/), embed.go, dist/
├── docs/
└── Makefile
```

## 3. Phase 1 — OpenAI-compatible Custom Provider

### 3.1 User story
- As an operator, I log in to the dashboard (default password `123456`, changeable).
- I add an OpenAI-compatible **provider node**: name, prefix, base URL, API type (`chat` / `responses`).
- I add one or more **upstream API keys** (key pool) to that node, test its connection, and view the upstream model list (`GET {baseUrl}/models`).
- I create a **gateway API key** for clients (`sk-{machineId}-{keyId}-{crc8}`).
- A client calls `POST /v1/chat/completions` with `model: "<prefix>/<model>"`; the gateway forwards it (streaming or non-streaming) to the upstream, rotating keys on failure.
- I view usage (requests, tokens, latency, status) on the Usage page.

### 3.2 Functional

**Gateway (`/v1`)**
- `POST /v1/chat/completions` — passthrough to `{baseUrl}/chat/completions`; SSE is streamed as-is (flush per chunk, cancel when the client disconnects).
- `POST /v1/responses` — passthrough to `{baseUrl}/responses` for nodes with `apiType=responses`.
- `POST /v1/embeddings` — passthrough to `{baseUrl}/embeddings`.
- `GET /v1/models` — each node's models (`prefix/model`) from the upstream `GET {baseUrl}/models` (5-minute cache). `customModels` (kv) is deferred to phase 2.
- Model resolution: `prefix/model` → the node with that `prefix`; model aliases (`kv.modelAliases`) are supported read-only.
- Mismatched format pairs (e.g. a `/chat/completions` client to a `responses` node) → `400` with a clear message (translator = phase 2).
- Client auth: `Authorization: Bearer <key>` or `x-api-key`; required when `settings.requireApiKey != false`.
- **Account fallback**: pick active connections in `priority` order; skip those whose `rateLimitedUntil` has not yet passed; on 401/402/403/404/429/5xx mark a cooldown (429/rate-limit = exponential backoff 2s→30s; 401/402/403/404 and an expired sign-in that cannot be renewed = 30 seconds; transient error/5xx = 30 seconds; suspended-account pattern = 24 hours; no cooldown but the last one exceeds 30 seconds, unlike XRouter-Next which backs off to 5 minutes and benches a rejected key for 2) then try the next connection. Request-scoped errors (other 4xx) are returned immediately. Success → reset status.
- Usage is recorded to `usageHistory` (provider, model, connectionId, apiKey, endpoint, tokens, latency, status). Dashboard tests are recorded too: a key test (`POST /api/providers/{id}/test`, also the batch test) and a model test (`POST /api/models/test`) each write one row with `endpoint` `/dashboard/test-key` or `/dashboard/test-model`, the tested connection, latency and `ok`/`error`; a key test that only lists models (`GET /models`) uses the model label `<prefix>/(list models)`. Tokens are what the probe answer reports (the chat probe asks for `stream_options.include_usage`, and is repeated without it if the upstream rejects that option; messages and responses shapes are read too); a key test that only lists models, or an upstream that sends no usage, records 0. A test answered from the 10 s cache makes no call and no row. Consequences: tests appear under Usage and in the live panel, and they count toward request budgets and the routing-health success rate.
- Secret headers (the client's `Authorization`) are never forwarded upstream; the upstream only gets the connection's key.
- SSRF: the node base URL is validated (http/https scheme). Private/loopback is **allowed** (self-hosted, e.g. Ollama) because only a logged-in admin can add nodes.

**Dashboard API (`/api`)** — same paths as XRouter-Next where they exist:
`auth/{login,logout,status}`, `keys[/{id}]`, `provider-nodes[/{id}]`, `provider-nodes/validate`, `providers[/{id}]`, `providers/{id}/test`, `providers/{id}/models`, `settings`, `usage/stats`, `usage/history`, `health`, `version`.

**Dashboard UI (React)**
| Page | Contents |
|---|---|
| Login | Password form, lockout message |
| Dashboard | Range picker (24h/7d/30d); cards for total requests, total input, total output, total cache; request & token (input/output/cache) charts per hour/day; most-used models chart |
| Providers | Node list + status, add/edit/delete node, manage the key pool per node, connection test, view models |
| API Keys | Gateway base URL, list & create gateway API keys (+ access list), `curl`/SDK examples |
| Usage | Request history table (filter by provider/model/status), total tokens |
| Settings | Change password, toggle `requireLogin` / `requireApiKey`, data dir & version info |

Design: collapsible sidebar, dark/light theme following the OS + toggle, neutral palette with a single accent, Inter + JetBrains Mono typography, skeleton loading, toast, empty state, responsive (mobile drawer), keyboard-accessible.

### 3.3 Non-functional
- Startup < 300 ms, idle memory < 60 MB.
- Streaming without full buffering; no leaked goroutines when the client disconnects.
- SQLite WAL, `busy_timeout=5000`, `foreign_keys=ON`.
- Secret: `JWT_SECRET` env or the file `<data>/jwt-secret` (0600). Cookie `HttpOnly`, `SameSite=Lax`, `Secure` when `X-Forwarded-Proto=https` or `AUTH_COOKIE_SECURE=true`.
- Login limiter: 5 failures → progressive lock per IP; the IP is taken from the TCP connection (the `X-Forwarded-For` header is trusted only from loopback).
- The upstream `apiKey` is never sent back to the UI in full (masked).
- The default password `123456` (or `INITIAL_PASSWORD`) is accepted from any machine; this deliberately differs from XRouter-Next, which refuses it remotely. Exposure is limited by the bind address instead: all interfaces by default, 127.0.0.1 with the **Local only** setting.

### 3.4 Data model (compatible)

- `providerNodes(id, type, name, data{prefix,apiType,baseUrl}, createdAt, updatedAt)` — `id = "openai-compatible-{apiType}-{uuid}"`, `type="openai-compatible"`.
- `providerConnections(id, provider=<node id>, authType="apikey", name, priority, isActive, data{apiKey, providerSpecificData{prefix,apiType,baseUrl,nodeName}, testStatus, lastError, lastErrorAt, errorCode, rateLimitedUntil, backoffLevel,…})`.
- `apiKeys(id, key, name, machineId, isActive, createdAt, accessRestricted, accessAllow)` plus two Router-Go columns added by an idempotent `ALTER` in `db.Open`: `maxTokens`, `usedTokens` (XRouter-Next ignores them).
- `settings(id=1, data JSON)` — `password`, `requireLogin`, `requireApiKey`, etc.
- `usageHistory`, `kv`.
- Other tables from the XRouter-Next schema are created empty so the DB can be opened back by Next.

### 3.5 Acceptance criteria
1. `make build` produces a single binary; `./router-go` opens the dashboard on `:20128`.
2. Login → add node `https://api.openai.com/v1` (or a local mock) → add key → test succeeds.
3. `curl /v1/chat/completions` stream & non-stream via `prefix/model` succeeds; with 2 keys and the first key returning 429, the request falls through to the second key.
4. Usage appears in the dashboard.
5. A DB created by Router-Go can be read by XRouter-Next and vice versa (same tables/columns) for the 5 phase 1 tables.
6. `go vet`, `go test ./...`, `tsc --noEmit` are green; there are tests for fallback, auth, model resolve, streaming passthrough.

## 3b. Phase 2a — Translator, combo, anthropic-compatible, custom models, key access

Continuation of phase 1 (requested right after phase 1). The specification here serves as the backend ↔ frontend contract.

### Format & translator
- Three client/upstream formats: `chat` (`/v1/chat/completions`), `responses` (`/v1/responses`), `messages` (Anthropic `/v1/messages`, **new endpoint**).
- A node's upstream format: openai-compatible `apiType` = `chat` | `responses`; anthropic-compatible = `messages`.
- If the client format ≠ the upstream format, the gateway translates through the `chat` hub: request `client→chat→upstream`, response `upstream→chat→client`, including SSE streaming (event by event, without full buffering), tool calls, system prompt, base64 images, usage. Package: `internal/translate`.
- Errors to the client use the client format's error shape (OpenAI `{error:{message,type,code}}`, Anthropic `{type:"error",error:{type,message}}`).

### Anthropic-compatible node
- `providerNodes.type="anthropic-compatible"`, id `anthropic-compatible-{uuid}`, `data{prefix, baseUrl}` (without `apiType`).
- Upstream: `POST {baseUrl}/messages`, header `x-api-key` + `anthropic-version: 2023-06-01`; model list `GET {baseUrl}/models`.
- Key pool, fallback, cooldown identical to the openai-compatible node. `POST /api/provider-nodes` accepts `type` (`openai-compatible` default | `anthropic-compatible`).

### Combo
- Table `combos` (already exists). Combo = a name (without `/`) + an ordered list of models (`prefix/model` or alias). Clients use `model: "<combo name>"`.
- Strategies: `fallback` (in order; continue to the next member if a member fails/is cooling/rejects the request) and `round-robin` (rotates the starting point on each request; sticky limit = `settings.comboStickyRoundRobinLimit`, default 1). The per-combo strategy is stored in `settings.comboStrategies[name].fallbackStrategy`, defaulting to `settings.comboStrategy` (`fallback`), compatible with XRouter-Next.
- A request-scoped error (other 4xx) from one member → continue to the next member without a cooldown; if all fail, return the first request-scoped member's response; otherwise a summary 503 + `Retry-After` if there is a cooldown.
- API: `GET /api/combos` → `{combos:[{id,name,kind,models[],strategy,context_length,createdAt,updatedAt}]}`; `POST /api/combos {name,models[],strategy?,context_length?}`; `PUT /api/combos/{id}` (same fields, partial); `DELETE /api/combos/{id}`. Unique name (409), `^[A-Za-z0-9_.-]{1,64}$`, models ≥1 string.
- `GET /v1/models` also lists combos (`owned_by:"combo"`).

### Model alias & custom model
- `kv(scope='modelAliases')` key=alias value=`prefix/model`; `kv(scope='customModels')` key=`{prefix}|{id}|llm` value=`{providerAlias,id,type:"llm",name}` (XRouter-Next format).
- API: `GET /api/models/alias` → `{aliases:{alias:model}}`; `PUT /api/models/alias {alias,model}`; `DELETE /api/models/alias?alias=`. `GET /api/models/custom` → `{models:[{providerAlias,id,type,name}]}`; `POST /api/models/custom {providerAlias,id,name?}`; `DELETE /api/models/custom?providerAlias=&id=`.
- Custom models are used for upstreams that do not expose `/models`; they appear in `/v1/models` and the dashboard model list (merged, without duplicates).

### Per-key access list
- **Token limit**: `access.maxTokens` (total prompt+completion tokens, `0` = unlimited) is checked on `/v1` chat-style requests (`proxy.go`) after the model-access check; when `usedTokens >= maxTokens` the key gets `429` `token_limit_exceeded`. `usedTokens` is a counter bumped after each request. Limits: the check runs before the call, so in-flight requests can overshoot; there is no reset (raise the limit to continue); other endpoints (embeddings, videos, ...) are not counted or checked yet.
- Columns `apiKeys.accessRestricted` / `accessAllow` (JSON array). Unrestricted by default. A *restricted* key may only call the combos/models in the allow list (exact match, case-insensitive, on the **resolved** identity `nodeId/model`; an alias and a prefix that resolve the same are treated as one target; an allowed combo grants its members only when reached through that combo).
- Fail-closed: a corrupt allow list → nothing is allowed. Rejection = `403` `This API key is not allowed to use model "<requested>"` before any upstream credentials are touched. `/v1/models` is filtered according to the allow list.
- API: key items in `GET/POST /api/keys` include `access:{restricted,allow[],maxTokens}` and `usedTokens`; `POST /api/keys` may accept `access`; `PATCH /api/keys/{id}` accepts `access` (validation: object `{restricted:bool,allow:string[],maxTokens:int>=0}`, ≤200 entries, ≤256 characters/entry; invalid → 400).

### Outside phase 2a (still phase 2b+)
pricing, Gemini `/v1beta`, `/v1/messages/count_tokens`, round-robin across *connections* (account semaphore), capability auto-switch, smart/fusion combo, Dockerfile.

## 3c. Phase 2c — Token Saver, Guards & Shields, Usage page

- **Ponytail** (`settings.ponytailEnabled`, `ponytailLevel` lite|full|ultra) and **Watermarks Remover** (`watermarksRemoverEnabled`): inject instructions into the upstream body's system prompt (after translation; the prompt text is the same as XRouter-Next). Idempotent; does not alter the client body used by other combo members. Watermarks Remover works only through the prompt and does not filter responses. **Custom system prompt** (`customPromptEnabled`, `customPrompt` ≤8000 characters). Order: ponytail → watermarks → custom → loop guard. Other token savers (RTK, caveman, headroom, pxpipe) are not ported.
- **Loop Guard** (`loopGuardEnabled`, enabled by default): detects an identical tool call ≥3×, a repeated tool-call sequence ≥2×, or repeated assistant text/sentences ≥3× in the request history; then injects a termination prompt and `[ROUTER NOTE: …]` in the last message.
- **Circuit Breaker** (`circuitBreakerEnabled`, enabled by default): per node; 5 provider-side failures (408/500/502/503/504/520/524 or network error) within 60 s → open; requests fail fast (503 + `Retry-After`) so the combo moves on to the next member; after 30 s a single probe (half-open), failure → open again with the wait ×2 for every 3 failed probes (max ×16). Differs from the original: failures are counted in a sliding window and cleared by a success.
- **Semaphore** (`semaphoreEnabled`, enabled by default): max 3 parallel requests per connection (`providerSpecificData.maxConcurrency`, 0 = unlimited), FIFO queue ≤20, wait ≤30 s; a connection that still has a free slot is chosen first.
- API: `PATCH /api/settings` accepts the keys above (level/length validation). UI: **Token Saver** page.
- **Model capabilities** (`kv customModels[].caps`): vision, reasoning, audioInput, contextWindow. Detected from the model id (`internal/modelcaps`: model-family patterns, sizes in the name such as `-32k`/`-1m`, a context-window table) and can be overridden per model (`PATCH /api/models/custom`); an override always wins, detection only turns things on. An unknown context (0) never filters a model out.
- **Capability-aware routing**: needs are read from the request: image/audio in the current user turn = required (hard), reasoning/thinking parameter = optional (soft), token estimate (≈4 characters/token, 1000 per image/file, + `max_tokens`) against the context window. Combo members are stably sorted: satisfies everything → satisfies the required ones → the rest; those that do not fit the context or lack a required capability go to the back (not dropped, so fallback still works).
- **Capacity Adapter** (`settings.capacityAdapter.{vision,audioInput} = {enabled, roundRobin, models[]}`): when no candidate satisfies the required capability, the adapter pool is inserted at the front (the original model stays as a fallback); a restricted key is routed only to the adapter models it is allowed. No history trimming for the adapter's context window (not yet).
- **Built-in providers** (`internal/provider/<name>`): cline, codebuddy, qoder, freebuff, opencode-free, kiro (see "Kiro" below), tokenharbor (a plain API-key provider: data only, no native hook). Each package registers through `provider.Register` in `init()`; `provider/builtin` imports them all. Built-in providers are treated like nodes (prefix = alias, e.g. `cl/<model>`; key pool = connections with `provider = <registry id>`), so fallback, combo, translation, usage, and access list apply immediately. The registry's model catalog can be used right away (the "must be added" rule applies only to custom providers). Per-provider quirks: forced stream + assembly of the non-stream response, `{success,data}` envelope, per-model format (responses/messages), static headers. A used alias cannot be used by a custom node. `GET /api/builtin-providers` lists the registry. Auth: API key/token, and browser OAuth login for providers that support it. Impersonation of the official client's identity (UA/CLI version, TLS fingerprint) is **deliberately not** ported, so CodeBuddy and OpenCode Free may be rejected upstream; Qoder (its own signed protocol) is registered but cannot be routed yet. Freebuff deviates from that pattern: its protocol itself rejects non-official clients while recording a strike per account, so this port does not talk to its backend at all and instead runs the official freebuff binary in a private tmux session and reads the answer from the CLI's own chat log (`Native.Local` hook, no connection/API key in the router).
- **OAuth login** (`POST /api/oauth/{provider}/start|exchange|poll`): the flow differs per provider and is written in its package (`internal/provider/<name>/oauth.go`). `code` (Cline): login URL → browser returns to `/oauth/callback?code=…` (the token is inside the code, base64) → `exchange`. `poll` (CodeBuddy: state + `authUrl`): the dashboard calls `poll` until the token arrives. Freebuff has no sign-in flow in the router: its CLI holds its own login, and its OAuth route answers 400 `this provider has no browser sign-in`. The token is stored in the `authType=oauth` connection (never sent to the UI), refreshed automatically 2 minutes before expiry (one refresh per connection), and sent as the key (Cline: `workos:` prefix). Logging in again to the same account updates the existing connection; the first login enables the model catalog. Per-request dynamic headers (CodeBuddy's request id) and the registry's default headers (Cline, CodeBuddy: XRouter data) are also sent on login calls; all of them can be viewed and changed in the Custom Headers card.
- **Built-in provider settings**: catalog models must be enabled first (kv `customModels`, same as custom providers; the first key automatically enables the whole catalog; `POST /api/builtin-providers/{id}/models {enable-all|disable-all}`). Per-provider **custom headers** (`PUT/DELETE /api/builtin-providers/{id}/headers`, kv `providerHeaders`): override on top of the registry's default headers; `Authorization`/`x-api-key`/`Host`/etc. are rejected (credentials belong to the API key). Per-provider **key pool** (`PUT /api/provider-config/{id}`, kv `providerConfig`): `roundRobin` + `sticky` rotate the starting key on every request.
- **Model Budgeting** (`/dashboard/model-budgeting`, under Model Masking) and **Quota Tracker** (`/dashboard/quota-tracker`; `/dashboard/quota` redirects there). Quota Tracker shows live quota per connection; Model Budgeting holds self-made limits as described below: a usage tracker per provider and per connection (today / 7 days / this month, last used) from `GET /api/quota` (`connections`, `usage`, `limits`); limits are an optional layer that can be attached directly to a provider or key. Limits are stored in kv `quotaLimits`: a `requests` or `tokens` limit per `provider` or `connection`, window `hour|day|week|month`, `calendar` (server local time boundaries; week starts on Monday) or `rolling`. Usage is computed from `usageHistory` (cancelled is not counted). With `enforce`, the gateway skips targets whose quota is exhausted (the next key / combo member takes over; all exhausted → 429 + `Retry-After`); without `enforce` it only tracks. `GET/POST /api/quota`, `PUT/DELETE /api/quota/{id}`.
- **Models must be added first**: a model can only be used by clients after it is added to its provider (`kv customModels`, via Providers → Models). The upstream `/models` list is only a catalog to pick from; a model that has not been added is rejected with `404 model_not_added` (aliases and combos follow the same rule; a *restricted* key gets 403 without leaking the name). `/v1/models` shows only models that have been added. `GET /api/provider-nodes/{id}/models` → `{models (added), upstream, error?}`.
- **Console log** (`/dashboard/console-log`): a 1000-line in-memory buffer (`internal/consolelog`); `GET /api/console-logs/stream` (SSE: `init`, `lines`, `clear`), `GET`/`DELETE /api/console-logs`. The gateway logs one line per request, upstream failures/cooldowns, combo fallback, circuit breaker, loop guard, semaphore queue, and model tests.
- **Dashboard CPU/RAM**: `GET /api/system` covers the Router-Go process only (CPU % of machine capacity, RSS, Go heap, goroutines; 2-minute history).
- **Usage**: `GET /api/usage/summary?period=month|today|24h|7d|30d|60d|all` (totals, per-hour/day series, breakdown per model/provider/account/API key/endpoint, 5-minute live, recent) and `POST /api/usage/reset`. Cache tokens are recorded in the `tokens` column (`cached_tokens`).

### Additional endpoints & Gemini client format (done, F2b)
- **New client format `gemini`** (`/v1beta/models/{model}:generateContent|streamGenerateContent|countTokens`, `GET /v1beta/models[/{model}]`). Design: `serve("gemini")` converts the Gemini body into a `chat` request at the start (`geminiRequest`: model from the URL, `stream` from the `:streamGenerateContent` action, not from the body), so routing, combo, capability, key pool, usage and token saver are unchanged. Only the response side knows the `gemini` format: `relayConverted`/`relayAggregated` render hub → Gemini (JSON or SSE), and errors use `{error:{code,message,status}}` (`INVALID_ARGUMENT`, `UNAUTHENTICATED`, `NOT_FOUND`, `RESOURCE_EXHAUSTED`, …). The upstream remains `chat`/`responses`/`messages` (there is no Gemini node). Code: `internal/translate/gemini.go`, `internal/gateway/gemini.go`.
- Gemini API key: `Authorization: Bearer`, `x-goog-api-key`, or `?key=` (query only for `/v1beta`).
- `GET /v1/models/info?id=<prefix/model>` and `GET /v1/models/{prefix/model}`: info for one model (`id`, `owned_by`, `capabilities{vision,reasoning,audioInput}`, `contextWindow` when known) from the `/v1/models` list that the key may use; 404 `model_not_found` if it does not exist/is not allowed.
- `POST /v1/messages/count_tokens` → `{input_tokens}` and Gemini `:countTokens` → `{totalTokens}`: **local estimate** (~4 characters/token, image 1000), without calling the upstream.
- `POST /v1/responses/compact`: like `/v1/responses`, but `responses` nodes are called at `/responses/compact`.
- `POST /v1/api/chat` (Ollama): uses the chat pipeline; `stream` defaults to true; NDJSON answer `{model,created_at,message,done}` (tool calls are sent whole in the `done` object). Built as a `ResponseWriter` wrapper on top of `serve("chat")` (`internal/gateway/endpoints.go`).


### Kiro — a provider with its own protocol (done, experimental)
- **`Native` hook** (`provider.Provider.Native`, `internal/provider/native.go`): for upstreams that do not speak OpenAI. The provider supplies `Endpoints(base, psd)` (an ordered list of URLs), `Request(ctx, NativeCall)` (token, the connection's `providerSpecificData`, upstream model id, **chat** body, stream flag) and `Response(resp, model, stream)` which turns a 2xx upstream response into a plain chat-completions response (SSE + `usage` if streaming, JSON if not; in Kiro via `io.Pipe` + a decoder goroutine, and it closes the upstream body if the client goes away). In `attempt` (`proxy.go`) a provider with `Native` is invoked through `nativeDo` (`internal/gateway/native.go`) with `g.Client` and `ConnCtx` (the proxy pool still applies); the upstream format is always `chat`, so chat/responses/messages/gemini clients, combo, key rotation, quota, circuit breaker, usage and empty-response peek work unchanged. Status >= 400 is returned as-is (old classification/cooldown); 401/403/404 move to the next endpoint, other statuses do not. The key test (`ProbeModel`/`ValidateBuiltin`) goes through the same path with a small real request (`probeNative`; the connection's `providerSpecificData` is carried in the context by `ConnCtx`).
- **Protocol** (`internal/provider/kiro`): `POST /generateAssistantResponse` with a `conversationState` body (alternating user/assistant turns, tool definitions on the last turn, tool calls paired with their results; those that are unpaired or lack a definition are flattened into text), the response is a binary AWS event stream (frames + CRC32 verified, a truncated frame = error, not a short answer). Ordered endpoints: `q.<region>.amazonaws.com`, `codewhisperer.us-east-1.amazonaws.com` (+ `X-Amz-Target` header), `runtime.us-east-1.kiro.dev`; only the regions `us-east-1`/`eu-central-1` may enter the host name. Router-made model suffixes: `-thinking` (adds a `<thinking_mode>` block, budget from `reasoning_effort`/`thinking.budget_tokens`, default 16000, an explicit request beats the suffix) and `-agentic` (incremental-write prompt); the model id is normalized to Kiro's spelling (`claude-sonnet-4-5-2025…` → `claude-sonnet-4.5`). Text inside `<thinking>` becomes `reasoning_content`. Tool calls are collected until the end of the stream; usage from `metricsEvent`, or an estimate from `contextUsageEvent` if absent.
- **Auth methods** (`providerSpecificData.authMethod`): `builder-id` — OAuth button, AWS device-code flow (`poll`: register public OIDC client → `device_authorization` → poll `/token`); refresh to AWS OIDC with the clientId+clientSecret from registration. `google`/`github`/`imported` (social) — paste a refresh token in the Add key dialog; refresh to `prod.us-east-1.auth.desktop.kiro.dev/refreshToken`. `idc` (IAM Identity Center) — paste JSON `{"refreshToken","clientId","clientSecret","region"}`; refresh to `oidc.<region>.amazonaws.com/token`. `external_idp` (Entra) — paste JSON `{"authMethod":"external_idp","refreshToken","clientId","tokenEndpoint","scope","profileArn"}`; refresh to the IdP token endpoint (Microsoft login hosts only, via https) and the request carries the header `TokenType: EXTERNAL_IDP`. Import (`OAuth.Import`) validates with a single refresh before saving; what is pasted is not an API key, so it is stored as an OAuth connection. Not ported: Google/GitHub browser login (`kiro://` redirect) and IDC device-code with its own start URL (both can go through import), as well as Kiro API-key auth.
- **Refresh** (`OAuth.RefreshConn`): a refresh variant that also receives the connection's `providerSpecificData` and may return `Tokens.Extra`, which is merged into the connection data (`UpdateTokens`); used for a newly learned `profileArn` (from the refresh answer, or `ListAvailableProfiles`). `secret()` updates the connection data map currently used by the request, so the new ARN is included right away in the request that triggered the refresh. Without an ARN: the public default ARN (Builder ID / social) is used, except for `idc`/`external_idp` accounts (another account's default ARN → 403). `clientSecret` is never included in the connection list in the API.
- **Headers**: only the registry's static headers (`User-Agent`, `X-Amz-User-Agent`; changeable in the Custom Headers card) plus protocol headers (event-stream `Accept`, `Authorization`, `TokenType`, profile ARN, `X-Amz-Target`). Not ported: IDE identity markers (`x-amzn-kiro-agent-mode`, `machine-id`, SDK headers, per-account fingerprint in `kiroModels`), session replay, time context in the prompt, integrity gate/fix retry, live model list (`ListAvailableModels`) and the native effort field (`additionalModelRequestFields`). Kiro quota: see "Quota from the provider API".
- **Risk**: Kiro is built for the Kiro IDE/CLI; using its account through a router is not licensed and the account may be restricted or suspended (XRouter-Next marks it deprecated with the same RISK_NOTICE). Recorded in `Provider.Note` (shown in the dashboard). Status `experimental`: everything is tested only against mocks, not yet against the real service.

### Quota from the provider API & free tier catalog (done, experimental)
Unlike `/api/quota` (user-made limits, unchanged): this is the quota the provider itself reports per connection, and it is the data for the Quota Tracker page.
- **`GET /api/usage/{connectionId}[?force=1]`** (dashboard login; static routes `/usage/stats` etc. still take precedence in chi). Same shape as XRouter: `{"plan"?, "message"?, "quotas": {"<name>": {"used","total","remaining"?,"resetAt"(ISO|null),"unlimited"?,"displayName"?,"recurring"?}}, "raw"?}`; `quotas` is always present (`{}` when empty). Flow (`internal/api/api_usage_conn.go`): connection → built-in node → `Gateway.Secret` (refreshes the OAuth token if nearly expired) → `Provider.Usage` with `ConnCtx` (the connection's proxy pool), `OAuthClient` (the provider's static headers + custom headers) and a 15 s timeout. Sign-in/upstream problems always become a `message` with HTTP 200 (not 5xx); unknown connection 404; a provider without the hook → `{"message":"Usage API not implemented for <id>","quotas":{}}`. The token is never in the response.
- **Cache** 30 s per connection in memory, only for answers that contain quotas; an entry is not used if the key/token changes (including after a refresh); `?force=1` bypasses it. Answers containing a message are not cached.
- **Hook** `Provider.Usage func(ctx, Doer, UsageCall) (Usage, error)` (`internal/provider/usage.go`; `UsageCall` = `Token`, `Data` (providerSpecificData), `BaseURL` which can be `Override`n in tests). Handlers live in each provider's own package: **Kiro** (`kiro/usage.go`, port of `getKiroUsage`: three endpoint attempts codewhisperer GET → POST (`X-Amz-Target`) → Q GET, headers `tokentype: API_KEY`/`TokenType: EXTERNAL_IDP`, special messages for idc / google-github / others; `*_freetrial` rows), **CodeBuddy intl** (`codebuddy/usage.go`: `POST /billing/meter/get-user-resource`, refill packs (named Daily/Weekly/Monthly, `recurring: true`) separated from bonus packs (`Bonus Pack N`, `recurring: false`, `resetAt` = expiry)). Cline, OpenCode and Qoder: XRouter has no handler (Cline/OpenCode) or its handler needs a PAT exchange and Qoder cannot be routed here yet, so they answer "not implemented". Freebuff also answers "not implemented": its quota can only be read from the session endpoint of its own CLI, and calling it from outside the CLI actually burns the account's strikes. Only the static headers declared by the provider are sent; Kiro's IDE impersonation headers (`KiroIDE`) are not ported.
- **Adding a key** (`POST /api/providers {provider, name, apiKey, priority?, proxyPoolId?}`): for a built-in API-key provider (`CategoryAPIKey`, today Token Harbor) the key is validated with `GET {BaseURL}/models` (`ValidateBuiltin`, no billable call); 401/403 answers `400 Key rejected by <provider>` and nothing is stored, a pass stores it already `active`, any other failure (outage) stores it as untested. A custom node's key is checked right after it is stored and the outcome recorded. `POST /api/builtin-providers/{id}/validate {apiKey}` runs the same check for the dialog's Check button and answers 400 for providers whose "key" is a sign-in token.
- **The dashboard knows which providers support it** through the boolean field `usage` on each item of `GET /api/builtin-providers` (`builtinView`).

## 4. Roadmap for next phases (summary)

| Phase | Contents |
|---|---|
| 2a | **(§3b, done)** translator chat↔responses↔messages, `/v1/messages`, anthropic-compatible node, combo, alias/custom models, per-key access |
| 2b | Gemini translator (client, done), pricing/cost, Dockerfile |
| 3 | Built-in API-key providers (OpenAI, Anthropic, Gemini, DeepSeek, …) via a data-driven registry |
| 4 | OAuth providers + token refresh, quota, request details/observability |
| 5 | Media (image/TTS/STT/video), embeddings custom node, MCP |
| 6 | Tunnel, MITM, CLI tools, skills, SSO (OIDC/SAML), launcher/tray, updater |

## 5. Risks
| Risk | Mitigation |
|---|---|
| The schema/JSON format of `data` differs subtly from Next | Go structs use `map[string]any` for unknown fields so a round-trip does not lose data |
| XRouter-Next is very large (37 executors, 40+ translators) | Incremental port, explicit checklist, each phase can be released |
| SSE streaming leaks/buffers | Integration tests with a mock upstream, `http.Flusher`, context cancel |
| Lockout/login limit → locked out | Reset via `router-go --reset-password` (deletes `settings.password`) |
| Local only cut off a remote session | `router-go --remove-localhost-only` (sets `settings.localOnly=false`; run it on the machine, then start again) |

### Single instance and tray (done, Windows tray untested)
- `internal/instance`: at startup (after the `--reset-password` / `--remove-localhost-only` / `--version` exits, which must not kill a running server) the process reads `<data dir>/router-go.pid`, and if that pid is still a program with this executable's name it is terminated (SIGTERM, then a kill after 8 s; on Windows `TerminateProcess`), then the new pid is written. The name check stops a reused pid from killing an unrelated program. The pid file is removed on a clean exit.
- `internal/tray` (Windows only, `fyne.io/systray`, no cgo): the dashboard's `apple-touch-icon.png` wrapped as an ICO; menu **Hide to tray** / **Show console** (hides the console window, or its Windows Terminal owner) and **Exit** (same graceful shutdown as Ctrl+C). Other platforms get a no-op.

- `--background` (`cmd/router-go/background*.go`): after the early-exit flags, the process re-executes itself with the same arguments minus `--background`, detached (`Setsid` on Unix; `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP` on Windows), stdout/stderr appended to `<data dir>/logs/background.log` (not rotated), and exits. It waits 1.5 s for the copy: if it died by then, it prints the failure and exits 1. The copy goes through `instance.Replace` like any start. Without a console the Windows tray has no Hide item.

### Proxy pools (done)
- A pool = a regular proxy (`http(s)://`, `socks5://`) or a relay that the user deploys to Vercel (Edge function) / Cloudflare (Worker) using an API token (used once, not stored).
- A key selects a pool through `providerSpecificData.proxyPoolId` (Edit key dialog). All upstream calls (chat, key test, model test) go through a single `http.RoundTripper` that reads the pool from the context.
- `strictProxy`: pool disabled → the request fails, instead of falling back to the direct IP. Pool deleted → the key goes direct again.
- The relay uses a per-deploy secret (`x-relay-key`) and stores no logs/headers at all.
- A Vercel/Cloudflare pool can be flagged **keyless** (`keyless`, set in the pool dialog): for a relay deployed elsewhere that does not check a key, such as XRouter-Next's. No `x-relay-key` is sent, and Test forwards a real request (`x-relay-target: https://example.com`) instead of `GET /health`, which such a relay answers with 400. Not allowed for fingerprint relays. Such a relay logs headers (including `Authorization`) at an open `/log`; prefer redeploying from the dashboard.
- `Provider.DirectOnly` refuses a **plain relay** pool (`vercel`, `cloudflare`): the request fails with "this provider refuses relays — set its proxy to none (a plain http proxy works)" instead of being forwarded. Measured 2026-11-10 on the live edge: `opencode.ai` answers a relayed call with `429 Client IP address is required for free usage` through both the `fetch` relay and the sockets tunnel, and a Worker subrequest already carries `Cf-Connecting-Ip`, so no header the relay adds can satisfy the gate. Forwarding it produced a 429 that reads like a spent quota and benched a healthy key; OpenCode Free is therefore `DirectOnly`. A plain `http(s)://` proxy pool is allowed and is not affected.
- `Provider.ProxyDialer` + `ProxyRun` hand the pool's proxy URL to the provider's own dialer instead of wrapping the transport in `url.Proxy`. Needed by a provider that signs its own TLS (OpenCode Free): with `url.Proxy`, net/http passes the **proxy's** address to `DialTLSContext`, so the client's TLS identity would be handed to the proxy instead of the upstream and the tunnel would carry a different handshake — the gate rejects that. The provider dials the proxy, opens a `CONNECT host:port` tunnel and performs its own handshake inside it. `socks5://` pools are refused by name for such a provider. Proven 2026-11-10: three sequential `POST /v1/chat/completions` for `oc/big-pickle` through an `http://` pool answered 200, and `GET /inference/v1/models` through the same pool answered 200.
- A plain relay pool **cannot** substitute for a different egress IP on this upstream: a Worker subrequest to a Cloudflare-fronted origin arrives without `CF-Connecting-IP` and without any `X-Forwarded-For` (measured to httpbin and postman-echo); the target sees the Worker egress (`172.68.82.136` / `2a06:98c0:3600::103`). opencode.ai distinguishes the two failures itself: direct answers `429 FreeUsageLimitError "Free usage limit reached"`, relayed answers `429 "Client IP address is required for free usage"` — i.e. the relay path has no client IP at all, not a spent quota.
- Key/model tests always stream (CodeBuddy rejects non-stream; some upstreams answer streams much faster).
