# CLAUDE.md

Router-Go is an AI gateway shipped as one Go binary with an embedded React dashboard. It is a rewrite of **XRouter-Next** (Next.js); the SQLite schema is identical. Read [`README.md`](README.md) for what it does, [`docs/PRD.md`](docs/PRD.md) for the design and [`docs/PORTING_CHECKLIST.md`](docs/PORTING_CHECKLIST.md) for what is already ported.

## Working with the maintainer

- The maintainer writes in Indonesian. Reply in Indonesian; write code, comments, commits and **all docs in English**.
- Evidence over reassurance: say what was proven (test output, status code, live capture) and what was not. Never write "should work".
- End a report with what you skipped or did not check, and the remaining risk.
- Do not touch `web/dist` by hand. It is tracked in git and embedded into the binary, so rebuild it (`bun run build`) after any frontend change; the diff will be large, that is expected.

## Commands

```bash
make build                 # dashboard (web/dist) + ./router-go
make dev                   # Go with live reload (air) on :20128; run `cd web && bun run dev` beside it for HMR
make test                  # go vet + go test ./... + web typecheck
go test ./internal/provider/... ./internal/gateway/... ./internal/api/...   # fast loop for provider work
cd web && npx tsc --noEmit && bun run build                                 # frontend check
```

Gotcha: `pkill -f <name>` run from the Bash tool can match its own command line and kill the shell. Kill by PID (`ss -ltnp | grep <port>`).

## Layout

- `cmd/router-go` entrypoint. `internal/api` dashboard REST API. `internal/gateway` routing, key pool, fallback, upstream calls and validation. `internal/translate` format translation. `internal/db` SQLite store. `internal/provider` the provider registry and every built-in provider (`internal/provider/<name>`, linked in by `internal/provider/builtin`).
- `web/` React dashboard (Vite, Tailwind, TanStack Query). Provider UI lives in `web/src/components/providers`; provider logos in `web/public/providers/<id>.png`.

## Porting a provider: analyse, find bugs, implement, then document

Do these in order. Do not start writing code before step 2 is done.

1. **Analyse the reference first.** The reference implementations are `~/9router/XRouter-Next` and `~/9router/9router` (JS, `open-sse/providers/registry/<id>.js`, plus their `tests/unit/<id>-provider.test.js`). Read the registry entry and everything it points at (executor, translator, OAuth, model fetcher, usage/quota). Write down, before coding: endpoint and base URL, auth type, headers, request/response quirks, model catalogue and how it is fetched, capabilities, rate-limit behaviour. If the real client can be run, capture its traffic instead of guessing.
2. **Check for bugs and risks.** In the reference (it has bugs too: do not copy them blindly), in the upstream's behaviour, and in how it meets our code. Probe the real endpoint without a key (expect 401, not 404). Look for: wrong base URL or path, headers the upstream refuses, billing side effects of a probe, anti-abuse guards, places where our `Provider` struct cannot express the quirk. Decide which parts are not portable and say so.
3. **Implement the smallest thing that works.**
   - One package per provider: `internal/provider/<name>/provider.go`, registered with `provider.Register(...)` in `init()`, and a blank import added to `internal/provider/builtin/builtin.go`.
   - A plain OpenAI-compatible provider is only data (`ID`, `Alias`, `Name`, `Category`, `Status`, `BaseURL`, `Models`, `Passthrough`, ...). `BaseURL + "/chat/completions"` is the endpoint. Reach for `Native`, `Prepare`, `Transport`, `OAuth` only when the upstream needs it.
   - `Status` is honest: `ready` only when proven, `experimental` when the upstream may reject us, `unsupported` when it cannot be routed.
   - **Key providers** (`CategoryAPIKey`): the key is checked with `GET {BaseURL}/models` when it is added (`ValidateBuiltin`); a 401/403 refuses the key, anything else does not block it. Never validate with a billable call.
   - Never replicate a wire protocol that a backend guards against third-party clients (see Freebuff: it bills strikes to the account). Drive the vendor's own client instead.
   - Tests: a provider test (registration, base URL, model acceptance), and a mock-upstream test for anything with logic (`provider.Override(id, url)` points a registered provider at an `httptest` server). `internal/api/phase2_test.go` pins the number of built-in providers: update it.
4. **Every provider has an icon.** Put `web/public/providers/<provider id>.png` (use the reference's `public/providers/<id>.png`; otherwise fetch the vendor's own icon) and add the id to the `LOGOS` set in `web/src/components/providers/Builtin.tsx`. Without both the UI falls back to two-letter initials, which is a bug, not a design. `TestEveryProviderHasAnIcon` (`internal/provider/builtin`) fails when either is missing; also check `/providers/<id>.png` is served after `bun run build`.
5. **Verify.** `go vet ./... && go test ./...`, `tsc`, and a live call with a real key if one exists. If there is no key, say that the success path is only proven against a mock.
6. **Update the docs in the same change** (English only):
   - `README.md`: the built-in providers table (prefix, sign-in, honest status).
   - `docs/PORTING_CHECKLIST.md`: tick `[x]` / `[~]` / `[ ]` with the phase, and list what was not carried over.
   - `docs/PRD.md`: anything about routes, behaviour or structure that changed.
   - Remove or rewrite lines that are now false; do not leave a feature documented after it was deleted.

## Conventions

- Keep the diff small and reuse what exists (`ModelPicker`, `AddKeyDialog`, `Modal`, `Field`, existing hooks in `web/src/lib/hooks.ts`). No new dependency for a few lines.
- Code a user can see (dashboard, errors) must be accessible: labels on icon buttons, keyboard reachable, status not conveyed by colour alone.
- A shortcut with a known limit gets a comment: `shortcut: <the limit>, <when to upgrade>`.
- Provider quirks that took a live run to discover belong in a code comment next to the code that handles them, with the date they were verified.
