# Porting Checklist XRouter-Next → Router-Go

Legend: `[x]` ported & tested · `[~]` partial · `[ ]` not yet · `—` not planned.
The "Phase" column refers to [`PRD.md`](./PRD.md) §4. **Last updated: 2026-11-09 (phase 1 + 2a + 2c done).**

## A. Foundation
- [x] Data dir `~/.router-go` / `%USERPROFILE%\.router-go` (+ env override) — F1
- [x] Open SQLite WAL + bootstrap schema identical to XRouter-Next (12 tables) — F1
- [x] Repo: settings (JSON row, default merge) — F1
- [x] Repo: providerNodes — F1
- [x] Repo: providerConnections (priority, reorder, apikey name dedupe, health fields) — F1
- [x] Repo: apiKeys (+ access columns) — F1
- [x] Repo: usageHistory — F1
- [x] Repo: kv (customModels, modelAliases) — F1/F2a (alias & custom model CRUD)
- [x] Repo: combos (+ strategy in `settings.comboStrategies`) — F2a
- [x] Repo: proxyPools / proxyPoolFitness — F2
- [ ] Repo: pricing — F2
- [ ] Repo: requestDetails / usageDaily — F4
- [ ] Versioned migrations + pre-migration backup — F2
- [ ] Legacy JSON import (`db.json`, `usage.json`) — —

## B. Auth & security
- [x] Password login (bcrypt, `settings.password`, `INITIAL_PASSWORD`) — F1
- [x] JWT cookie `auth_token` (HS256, 24 hours, secret file/env) — F1
- [x] Login limiter per IP — F1
- [x] Handling of default password from remote (mustChangePassword) — F1
- [x] `requireLogin` toggle + `/api/*` guard — F1
- [x] Change password (`PATCH /api/settings`) — F1
- [x] Reset password (CLI flag) — F1
- [x] Generate/validate API key `sk-{machine}-{id}-{crc}` — F1
- [x] `requireApiKey` + per-key access (restricted/allow, fail-closed) — F1/F2a
- [x] Trusted-peer / strip `X-Forwarded-For` — F1
- [~] SSRF guard (`assertPublicUrl`) — F1 (scheme validation), F2 (remote mode) _(partial: http(s) scheme validation done; public-only remote mode not yet (private hosts deliberately allowed))_
- [ ] OIDC SSO — F6
- [ ] SAML SSO — F6
- [x] Machine ID (`node-machine-id`) — F1 (hostname/machine-id hash)

## C. Gateway API (`/v1`)
- [x] `POST /v1/chat/completions` (OpenAI-compatible node, stream + non-stream) — F1
- [x] `POST /v1/responses` (node `responses`) — F1
- [x] `POST /v1/embeddings` (passthrough) — F1
- [x] `GET /v1/models` — F1
- [x] `GET /v1/models/info?id=`, `GET /v1/models/{prefix/model}` (id, owned_by, capabilities vision/reasoning/audioInput, contextWindow from `modelcaps`; honors the key's access list; slugs of kind `image|tts|stt|embedding|image-to-text|web` → empty list) — F2b
- [x] `POST /v1/messages/count_tokens` (**local estimate** ~4 characters/token, images fixed at 1000; not a real tokenizer) — F2b
- [x] `POST /v1/responses/compact` (node `responses` is called at `/responses/compact`; chat/Anthropic nodes answer like `/v1/responses`, same as XRouter — in XRouter the `_compact` flag is only used by the Codex executor) — F2b
- [x] `POST /v1/messages` (Anthropic) — F2a
- [x] `/v1beta/models/{model}:generateContent`, `:streamGenerateContent` (SSE; model from the URL, stream from the action), `:countTokens` (estimate), `GET /v1beta/models[/{model}]`; key via Bearer, `x-goog-api-key` or `?key=`; error `{error:{code,message,status}}` — F2b _(not yet: `?alt=sse` is treated as always on, `fileData`/`cachedContent`, forwarding native TTS to Google, snake_case `system_instruction`)_
- [x] `POST /v1/api/chat` (Ollama chat: `stream` defaults to true, NDJSON response; Ollama `options`/`format`/`images` not yet mapped) — F2b
- [ ] `/v1/images`, `/v1/audio/*` — F5
- [-] `/v1/search`, `/v1/web/fetch` — deliberately removed (unused)
- [x] Parse `prefix/model`, node resolution — F1
- [x] Model alias (read) — F1
- [x] Account fallback + cooldown + exponential backoff — F1
- [~] Error rules (`ERROR_RULES`, permanent-account quarantine) — F1 (core) / F4 (complete) _(partial: generic rules, CodeBuddy rules, permanent-account quarantine + stop after 5 consecutive permanent keys done; other per-provider rules not yet)_
- [~] Account semaphore (parallel limit per key, queue of 20, 30 s timeout) — F2c _(round-robin/sticky across connections not yet)_
- [x] Combo (fallback, round-robin + sticky limit) — F2a  _(kind/smart/fusion not yet)_
- [~] Translator chat↔responses↔messages (request, response, SSE) — F2a
- [x] Client-side Gemini translator (F2b): request gemini→chat (systemInstruction, roles user/model, text, `inlineData` image/audio, functionCall/functionResponse, tools + `toolConfig`, `generationConfig` → max_tokens/temperature/top_p/stop; schema type `OBJECT`→`object`), response chat/messages/responses→Gemini (non-stream + SSE: text, thought, whole functionCall in the closing chunk, `finishReason`, `usageMetadata`). _Not an upstream format: there is no Gemini node (gemini→upstream and a Gemini response parser do not exist yet)._
- [ ] `json_schema → json_object` fallback for node compat — F2
- [x] `stream_options` strip on non-stream — F1
- [x] Model capabilities (vision, reasoning, audio, context window): detection from the name + manual override — F2c
- [x] Capability-aware routing: orders combo members (vision/audio required, reasoning optional, context must fit) — F2c
- [~] Strip unsupported params — F3 _(partial: groq/mistral/cerebras/nvidia/openai by host; other per-provider rules not yet)_
- [x] Reasoning content injector (deepseek: all assistant turns, kimi: tool-call turns) — F3
- [x] Token saver: Ponytail (lite/full/ultra) + Watermarks Remover + custom system prompt — F2c
- [ ] Token saver: RTK / caveman / headroom / pxpipe — — _(deliberately not ported; only ponytail & watermarks remover were requested)_
- [~] Capacity adapter (model pool per capability vision/audio, round-robin; pdf/video not yet) — F2c
- [x] Loop guard (tool-call & text loop, chat/messages/responses) — F2c
- [x] Circuit breaker per provider (5 failures/60 s → open, probe 30 s, escalation) — F2c

## D. Provider
- [x] `openai-compatible` node (chat/responses) — F1
- [x] Node validation (`/models` then chat probe) — F1
- [x] Connection test per connection — F1
- [x] Fetch upstream models per connection — F1
- [x] `anthropic-compatible` node — F2a
- [ ] `custom-embedding` node — F5
- [x] Built-in provider framework (`internal/provider/<name>`, static registry, key pool, translation, combo, usage) — F2c
- [x] Cline (API key; forced stream + `{success,data}` envelope) — F2c
- [x] CodeBuddy (API key; `Native` hook: `X-Conversation-ID` derived from the body (v5 UUID from the session key / first user text), `X-Request-ID` = `X-Conversation-Request-ID`, neutralization of rival CLI identities in system/assistant, tools >64KB compacted) — F2c _(experimental; tested with mocks, not yet against a real account)_
- [x] OpenCode Free (no key; `Native` + `Transport` hooks: per-model path — `/inference/openai/v1/chat/completions`, `/inference/openai/v1/responses`, `/inference/anthropic/v1/messages` — with the CLI client fingerprint: Bun TLS ClientHello (utls, spec derived per dial), `ses_` session with a descending-time prefix that is stable per process, `<env>` marker, `read`/`shell` tools verbatim + `terminal`→`run_command` alias restored in the response, Responses path normalization) — F2c _(experimental; chat & responses tested against the real service, the messages path only with mocks because the current free catalog contains no model on that path; the free quota is counted per session so it can 429 when busy; through a proxy pool the custom TLS dialer is turned off so the TLS fingerprint does not carry over, purely the standard CONNECT path)_
- [x] Freebuff (no key; `Native.Local` hook: runs the official freebuff binary in a private tmux session with `FREEBUFF_CONFIG_DIR` + `--cwd`, selects the model via `freebuffModel` in the CLI's settings.json — or via `freebuffModelKey` when the model is a catalog row without a wire id, because an id the account cannot reach is **not rejected but silently coerced** to the fallback (so the "Slow" rows can only be reached through their key `m-61c26317cc`/`m-8db1ccbba6`) —, types the prompt into the input box and reads the answer from the CLI's own `chat-messages.json` (`textType: "reasoning"` blocks are skipped), one process per model because `/model` takes no arguments — **and reads the CLI's admission box**: when a turn does not start (hours used up, session must be taken over, cost must be confirmed) the CLI does not hang but shows a box with the footer `Esc: back to draft` along with the reason, so the driver recognizes that box and fails the turn **right then with the CLI's reason**, instead of waiting for the deadline and blaming the provider for "not responding"; the same box is used as a progress line when a new session starts (`Starting your model session…`), so only a box offering an `Enter:` action counts as a rejection — there is no automatic answer, because confirming means spending the operator's hours) — F2c _(experimental; **requires the official binary**: the backend rejects the freebuff protocol from non-official clients with 403 `third_party_client_warning` and records a strike per account (3 strikes = suspend), so this port never talks to the backend itself. Needs the CLI installed + logged in and `tmux` present; no quota from the API (the session endpoint belongs to the CLI itself); the registered model list is deliberately the ones proven to work on this account — see §D; the test probe per model on `Native.Local` providers uses a **180 s** budget, not 20 s, because a single freebuff turn (CLI start + session + generation) once took 2 minutes — with 20 s the test always failed and blamed a provider that was actually healthy; even a "Slow" row can answer "Not enough hours" when the account's hours are used up, and that now surfaces as the real reason)_
- [~] Kiro (`Native` hook: CodeWhisperer protocol + AWS event stream → chat; Builder ID device-code auth, import of social/IDC/external_idp refresh tokens, per-method refresh, profileArn; chat/tools/thinking/agentic) — F4 _(experimental, tested with mocks only; no IDE identity marker, session replay, repair retry, live models, Google/GitHub browser login, Kiro API key)_
- [x] Qoder (`Native` hook: `agent_chat_generation` protocol — COSY headers (AES-128-CBC identity + RSA wrap + MD5 signature over payload/key/ts/body/path), obfuscated base64 body + `&Encode=1`, SSE envelope `{statusCodeValue, body}` unwrapped with usage merged into the finish frame, exchange of PAT `pt-` → job token `jt-` at openapi, per-account `model_config` catalog + static fallback, stable machine ID per user) — F3 _(experimental; tested with mocks, not yet against a real account; not yet: model queue/wait-replay (a model that queues is answered with 429 so the router falls through to another connection), file image upload (inline data URIs still work), context tier upgrade 200K/400K/1M; no Usage hook yet; the job token is served by api2.qoder.sh before api3)_
- [ ] Other built-in apikey providers (OpenAI, DeepSeek, Groq, …) via registry — F3
- [~] Browser OAuth login per provider: Cline (code via callback + refresh), CodeBuddy (polling) — F2c _(not yet tested against the real service; Freebuff has no sign-in flow here — login is handled by its own CLI, and its OAuth route answers `no browser sign-in`)_
- [ ] Qoder OAuth login — F3 (the COSY protocol has not been ported for the browser login flow; PT via the API key dialog already works through the `Native` hook)
- [x] Local Quota Tracker (request/token limits per provider or key; hour/day/week/month windows, calendar or rolling; option to block when exhausted) — F2c
- [~] Quota from the provider API (OAuth account/plan limits): `GET /api/usage/{connectionId}` (same shape as XRouter: `plan`, `message`, `quotas`), 30 s cache per connection (`?force=1` bypasses it), through the connection's proxy pool; `Provider.Usage` hook for Kiro and CodeBuddy intl; `usage` flag in `/api/builtin-providers`; other providers answer `Usage API not implemented for <id>` (Freebuff included: its quota only exists on its CLI's own session endpoint) — F4 _(tested with mocks only; not yet: Qoder, GitHub, Codex, Claude, Gemini, Antigravity, GLM, MiniMax, Kimi, etc., retry after a force-refresh of the token, Kiro IDE disguise headers)_
- [~] Quota Tracker UI same as XRouter (`/dashboard/quota-tracker`) — F4: card per connection with a quota table (remaining % bar, used/total, reset countdown; green >70, yellow 30-70, red <30), provider filter (synced with `?provider=`), active/disabled account filter, "Expiring first" and "% quota" sort (when a single provider is selected), "Turn off Empty"/"Turn on Available", 60 s auto-refresh + countdown + "Updated", refresh per connection and for all, pagination (10/20/50/100/custom), quota cache in localStorage, hide quota rows (saved per browser), toggle/edit/delete connection; the "Available free tiers" list was removed, and the local limits ("Custom limits") moved to the Model Budgeting page (`/dashboard/model-budgeting`, under Model Masking). Not carried over: Claude/Codex reset-credit, auto-ping schedule, Antigravity/Gemini specifics, provider logos (replaced by initials). _(not yet viewed in a browser)_
- [x] Free tier catalog (`GET /api/free-tiers`) — built in F4, then removed along with its UI.
- [x] Custom headers per built-in provider (defaults from XRouter for Cline & CodeBuddy; override, reset to default) — F2c
- [x] Round robin + sticky for the key pool per provider — F2c
- [x] Built-in provider models must be enabled first (Enable/Disable All, catalog automatically enabled on the first key) — F2c
- [x] Proxy pool + Vercel/Cloudflare relay deploy — F2b
- [ ] Dedicated executors (azure, bedrock, vertex, ollama-local, …) — F3
- [ ] OAuth providers + token refresh (claude, codex, gemini, kiro, cursor, github, …) — F4
- [ ] Web-cookie providers (grok-web, perplexity-web, …) — F4
- [x] Per-connection proxy / proxy pool — F2
- [ ] Provider overrides (header per provider) — F3

## E. Dashboard API (`/api`)
- [x] `auth/login`, `auth/logout`, `auth/status` — F1
- [ ] `auth/oidc`, `auth/saml`, `auth/reset-password` — F6
- [x] `keys`, `keys/{id}` — F1
- [x] `provider-nodes`, `/{id}`, `/validate` — F1
- [x] `providers`, `/{id}`, `/{id}/test`, `/{id}/models` — F1
- [ ] `providers/{bulk,test-batch,validate,client,suggested-models,kilo}` — F3
- [x] `settings` (GET/PATCH), `settings/require-login` — F1
- [ ] `settings/{database,proxy-test}` — F2
- [x] `usage/stats`, `usage/history` — F1
- [~] `usage/summary` (period, series, breakdown, live 5m, recent, 24h provider activity) + `usage/active` (running requests) + `usage/reset` — F2c _(chart/stream/recalculate/pricing not yet)_
- [ ] `usage/request-details` — F4
- [x] `health`, `ready`, `version` — F1
- [x] `health/routing` (card with provider count per circuit state + avg response), `models/test`, `providers/test-batch` (per-key result log), `system` (CPU/RAM) — F2c
- [x] Provider list simplified XRouter-style: 4 sections (Custom/OAuth/Free/API Key), title on the left + "Test All" on the right, 4-column grid of compact cards (icon + name + 1 status line); `test-batch` accepts `providers: [id...]` — F2d
- [x] Proxy Pools (F3): `proxyPools` table, types http/vercel/cloudflare, `strictProxy`, `noProxy`, Proxy Pools page, pick a pool per key (Edit key dialog, `providerSpecificData.proxyPoolId`), Vercel (Edge function) and Cloudflare (Worker) relay deploy via token (not stored); the relay is protected by a secret key per deploy (unlike XRouter, whose `/log` is open). Not yet: pool fitness/quarantine, Deno/Netlify.
- [ ] `init`, `shutdown`, `autostart` — F6
- [~] `models/{alias,custom}` — F2a _(disabled/availability/test/catalog-sync not yet)_
- [~] `combos` CRUD — F2a _(presets/suggest not yet)_
- [ ] `proxy-pools` — F2
- [ ] `pricing` — F2
- [ ] `translator` (translator playground page) — F2b
- [ ] `oauth/*` — F4
- [ ] `media-providers` — F5
- [ ] `tunnel`, `cli-tools`, `mitm`, `headroom`, `pxpipe`, `plugins`, `skills`, `mcp`, `tags`, `locale` — F6

## F. Dashboard UI
- [x] Shell: sidebar, topbar, dark/light theme, toast, responsive — F1
- [x] Login — F1
- [x] Token Saver page (Ponytail, Watermarks Remover, Guards & Shields, custom prompt) — F2c
- [x] Usage page like XRouter (period, Overview/Request Logs tabs, cards, charts, grouped table) — F2c _(Detailed Analysis tab & costs not yet)_
- [x] Dashboard: CPU & RAM cards for the Router-Go process only (2-minute sparkline) — F2c
- [x] Dashboard (request/input/output/cache cards, request chart, token, model) — F1/F2a
- [x] A provider's models must be added first before they can be used (the upstream list is only a catalog) — F2c
- [x] Providers like XRouter: cards + filter + routing health, detail page (connections, priority, test, models, upstream models) — F1/F2c
- [x] API Keys (base URL, API keys + access list, usage examples) — F1/F2a
- [—] Playground / basic-chat — deliberately removed (owner's request)
- [x] Usage (history) — F1
- [x] Settings / Profile — F1
- [x] Combos — F2a
- [x] Models (aliases & custom models) — F2a
- [ ] Proxy pools / proxy fitness — F2
- [ ] Pricing — F2
- [ ] Translator — F2
- [x] Quota (local tracker) — F2c
- [x] Console log (live SSE, level/text filter, follow, clear; request/upstream/combo/breaker/guard/test logs) — F2c
- [ ] Media providers — F5
- [ ] CLI tools, MITM, Token saver, PXPipe, Skills, next-feature — F6
- [ ] i18n (`i18n/`, `public/i18n`) — F6
- [ ] Landing, setup wizard, OAuth `callback` — F4

## G. Operations & distribution
- [x] Single binary with embedded SPA + precompress — F1
- [x] Makefile (`build`, `dev`, `test`) — F1
- [x] Go live reload with Air (`.air.toml`, `make dev`) — F2c
- [x] Graceful shutdown — F1
- [x] Configurable port: `--port`, env, Settings (switches immediately), default 20128 — F2c
- [ ] Dockerfile / docker-compose — F2
- [ ] CLI launcher (`start/stop/status`, daemon) — F6
- [ ] Tray, autostart, updater — F6
- [ ] Tunnel (Cloudflare/Tailscale) — F6
- [ ] Cloud sync — —

## H. Testing
- [~] Unit: auth (bcrypt/JWT/limiter/API key) — F1 _(partial: login/lockout/masking tested through integration; no separate unit tests yet)_
- [x] Unit: resolve model + fallback/cooldown — F1
- [x] Integration: mock upstream stream/non-stream/429 → fallback — F1
- [x] Unit/integration: 3-format translator (request/response/SSE) + gateway translation, combo, access list, dashboard CRUD — F2a
- [~] DB schema compatibility with XRouter-Next — F1 _(partial: identical schema & `data` JSON round-trip; opening the DB with a real XRouter-Next not yet tested)_
- [x] Frontend: `tsc --noEmit`, build — F1
- [ ] Playwright dashboard E2E — F2

---
**Summary:** 99 done · 16 partial · 40 not yet (phase 2b–6).

## Supporting services (src/sse/services)
- [x] Rationed tests: a test of the same key/model within 10 s is answered from memory; "Test all" skips experimental providers (CodeBuddy, Freebuff, OpenCode, ...) and they can only be tested one at a time — F2d _(the Freebuff test runs the real CLI, so testing one at a time is the only sensible way in)_
- [x] Background OAuth token refresh (every 5 minutes, for those expiring in < 30 minutes) — F4
- [x] Empty-response failover in combo (200 with no text/tool-call → next member; content_filter/length are not retried; stream held until there is content, max 128 KB / 20 s) — F4
- [x] Media passthrough endpoints: `/v1/images/generations`, `/v1/audio/speech`, `/v1/audio/transcriptions` (multipart), `/v1/videos/{generations,edits,extensions}` + polling `GET /v1/videos/{id}[/content]?model=` through an OpenAI-compatible node — F5 _(provider-specific adapters: ElevenLabs, fal, Stability, etc. are not ported)_
- [x] CodeBuddy: system message first + typed user content + neutralization of rival CLI identities (system/assistant only, user content untouched) + compaction of tools >64KB (`Prepare` in the registry) — F2c _(identity neutralization is now done; see the note in §D)_
