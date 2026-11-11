# Router-Go

An AI gateway with a web dashboard, shipped as a single Go binary (the React dashboard is embedded). One endpoint for many providers: clients speak OpenAI, Anthropic, Gemini or Ollama format, and Router-Go translates, picks a key, and falls back when one fails.

Rewritten from XRouter-Next in Go. The database schema is the same as XRouter-Next.

More docs: [`docs/PRD.md`](docs/PRD.md) · [`docs/PORTING_CHECKLIST.md`](docs/PORTING_CHECKLIST.md) · contributor and porting workflow: [`CLAUDE.md`](CLAUDE.md)

## Features

- **Many formats, one gateway.** Requests and responses are translated between `chat/completions`, `responses`, `messages` (Anthropic) and Gemini.
- **Providers.** Built-in ones (see the table below) plus custom OpenAI-compatible or Anthropic-compatible nodes with your own prefix.
- **Key pool and fallback.** Several keys per provider with priority, round-robin or sticky selection. Keys that are rate-limited or rejected are skipped automatically, with cooldown and a circuit breaker.
- **Combos.** One model name that wraps several models, tried in order.
- **Model masking.** A name clients send (`fast`) maps to a real model or combo, so you can swap what is behind it without touching clients.
- **Custom models.** Register models by hand for upstreams that have no `/models` endpoint. Capabilities (vision, reasoning, audio, context window) can be overridden.
- **Gateway API keys** with a per-key access list and an optional total token limit.
- **Proxy pools.** Route a key's traffic through a proxy (http/https/socks5), or through a relay you deploy to your own Vercel or Cloudflare account. Providers that refuse plain relays (`DirectOnly`, today OpenCode Free) fail with a clear error instead of a misleading 429; a plain http proxy pool still works for them (socks5 does not: it cannot carry their TLS fingerprint).
- **Usage, quota, console log and token saver** in the dashboard.

## Built-in providers

| Provider | Prefix | Sign-in | Status |
|---|---|---|---|
| OpenCode Free | `oc` | no key | live-verified |
| Freebuff | `fb` | no key, through the vendor's official CLI | live-verified |
| Cline | `cl` | OAuth | ready |
| CodeBuddy | `cbai` | OAuth | tested against mocks only |
| Qoder | `qd` | Personal Access Token (`pt-…`) | tested against mocks only |
| Kiro | `kr` | OAuth (AWS Builder ID), or paste a refresh token / JSON | experimental |
| Token Harbor | `th` | API key (`thk_live_…`) | ready; key check proven against the real service, a valid key not yet |

*Experimental* providers may be rejected by the upstream at any time. Each provider lives in `internal/provider/<name>` and registers itself with `provider.Register`.

## Quick start

Requires Go and [Bun](https://bun.sh).

```bash
./build.sh            # builds the dashboard (web/dist), then the ./router-go binary
./router-go           # http://localhost:20128
```

`make build` does the same. To cross-compile, set the target: `GOOS=windows GOARCH=amd64 ./build.sh` (produces `router-go.exe`).

First login: the initial password is `123456`, accepted from any machine. The server listens on all interfaces by default, so change the password in Settings (or set `INITIAL_PASSWORD`) before exposing it; **Settings → Local only** limits it to 127.0.0.1.

```bash
./router-go --port 8080          # another port
./router-go --reset-password     # forgot the password
./router-go --remove-localhost-only  # Local only locked you out: listen on all interfaces again
```

**Settings → Backup & restore** downloads providers, keys, combos, models, proxy pools and settings as one JSON file (secrets in clear text, keep it private) and restores it, replacing the current configuration. Usage history and this machine's port / Local only setting are not included.

**One instance per data directory.** Starting `router-go` again with the same data directory stops the copy that is already running (its pid is kept in `<data dir>/router-go.pid`) and takes over; copies with a different data directory are left alone. On Windows a tray icon offers **Hide to tray** (hides the console window; the item then reads **Show console**) and **Exit**. There is no tray on Linux or macOS.

**`--background`** starts a detached copy without a terminal (Linux/macOS: its own session, so closing the terminal does not stop it; Windows: no console window, so no Hide item in the tray) and exits; the copy logs to `<data dir>/logs/background.log`. It reports a failure to start (for example the port is taken) and exits with status 1. Stop it with `kill <pid>` (the pid is printed and kept in `router-go.pid`), the tray's Exit on Windows, or by starting `router-go` again.

Port priority: `--port` > env `PORT` / `ROUTER_GO_ADDR` > Settings > `20128`. The port can also be changed in **Settings → Port**; the server moves immediately, no restart needed.

## Usage

1. Open **Providers**, pick a built-in provider or create a custom node (give it a prefix, e.g. `mygw`).
2. Add a key (**API Key**) or sign in with **OAuth** on the provider page.
3. Create a gateway API key under **API Keys**.
4. Call the gateway:

```bash
curl localhost:20128/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"mygw/<model>","messages":[{"role":"user","content":"hello"}]}'
```

A model name is `prefix/model`, a combo name, or a mask name. List what is available with `GET /v1/models`.

## Gateway endpoints

Authentication: `Authorization: Bearer <api-key>` header. Cookies are never used on `/v1`.

| Endpoint | Purpose |
|---|---|
| `POST /v1/chat/completions` | Chat (OpenAI format) |
| `POST /v1/responses`, `/v1/responses/compact` | Responses API |
| `POST /v1/messages`, `/v1/messages/count_tokens` | Anthropic format |
| `POST /v1/embeddings` | Embeddings |
| `POST /v1/images/generations` | Images |
| `POST /v1/audio/speech`, `/v1/audio/transcriptions` | Audio |
| `POST /v1/videos/generations`, `/edits`, `/extensions`; `GET /v1/videos/{id}` | Video |
| `GET /v1/models`, `/v1/models/info`, `/v1/models/*` | Model list and details |
| `POST /v1/api/chat` | Ollama format |
| `GET/POST /v1beta/models/*` | Gemini format |

## Dashboard

Served at `/dashboard`: Providers, Combos, Model Masking, Token Saver, API Keys, Console Log, Usage, Proxy Pools, Quota, Settings.

**Security:** a single password (no username) stored as a bcrypt hash. A successful login issues a JWT (HS256, valid for 24 hours) in an `HttpOnly` `auth_token` cookie. Login attempts are rate-limited. A session token cannot be revoked before it expires.

## Configuration

Data lives in `~/.router-go` (Windows: `%USERPROFILE%\.router-go`); the database is `db/data.sqlite`. The JWT secret is generated on first run and saved in a `jwt-secret` file in that folder, so sessions survive restarts.

| Env | Purpose |
|---|---|
| `PORT`, `ROUTER_GO_ADDR` | Port, or `host:port` |
| `HOSTNAME_BIND` | Bind address; empty means all interfaces, or 127.0.0.1 when **Settings → Local only** is on (the variable wins) |
| `ROUTER_GO_DATA_DIR` / `DATA_DIR` | Data folder |
| `INITIAL_PASSWORD` | Initial password |
| `JWT_SECRET` | JWT secret (default: generated automatically) |
| `API_KEY_SECRET` | Secret for gateway API keys |
| `AUTH_COOKIE_SECURE=true` | Force the `Secure` cookie flag (on automatically over HTTPS) |
| `TRUST_PROXY=true` | Trust proxy headers from a reverse proxy in front of Router-Go |

Behind an HTTPS reverse proxy, set `TRUST_PROXY=true` and make sure the proxy sends `X-Forwarded-Proto`.

## Project layout

```
cmd/router-go/       entry point
internal/api/        dashboard API (/api) and the SPA
internal/gateway/    /v1 gateway: routing, key pool, fallback, breaker
internal/translate/  format translation (OpenAI, Anthropic, Gemini)
internal/provider/   built-in providers (cline, codebuddy, freebuff, kiro, opencode-free, qoder)
internal/auth/       dashboard login (bcrypt + JWT) and login limiter
internal/db/         SQLite: connections, keys, aliases, usage
internal/relay/      Vercel/Cloudflare relay for proxy pools
web/                 React dashboard (Vite + TypeScript)
```

## Development

```bash
make dev                  # Go with live reload (air) on :20128
cd web && bun run dev     # Vite on :5173, proxies to :20128
```

Install `air` with `go install github.com/air-verse/air@latest`; its config is `.air.toml`. For a backend on another port: `ROUTER_GO_PORT=8080 bun run dev`.

Tests: `make test`, which runs `go vet ./... && go test ./... && (cd web && bun run typecheck)`.
