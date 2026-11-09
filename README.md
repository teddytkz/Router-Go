# Router-Go

Gateway AI OpenAI-compatible + dashboard, ditulis ulang dari XRouter-Next ke Go (satu binary, dashboard React di-embed).

Fase 1+2a: custom provider **OpenAI-compatible** dan **Anthropic-compatible**, endpoint `/v1/chat/completions`, `/v1/responses`, `/v1/messages` (saling diterjemahkan), combo, alias/custom model, key pool dengan fallback, API key gateway (+ access list), usage.
Dokumen: [`docs/PRD.md`](docs/PRD.md) · [`docs/PORTING_CHECKLIST.md`](docs/PORTING_CHECKLIST.md)

```bash
# build (butuh Go dan Bun)
cd web && bun install && bun run build && cd ..
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o router-go ./cmd/router-go   # atau: make build

./router-go                      # http://localhost:20128 , password awal 123456 (hanya dari localhost)
./router-go --port 8080          # port lain; prioritas: --port > env PORT > Settings > 20128
./router-go --reset-password     # lupa password
```

Data: `~/.router-go` (Windows: `%USERPROFILE%\.router-go`), DB `db/data.sqlite` (skema sama dengan XRouter-Next).
Port juga bisa diubah di **Settings → Port** (server pindah saat itu juga, tanpa restart). Env: `PORT`, `ROUTER_GO_DATA_DIR`/`DATA_DIR`, `INITIAL_PASSWORD`, `JWT_SECRET`, `API_KEY_SECRET`, `TRUST_PROXY`, `AUTH_COOKIE_SECURE`.

Dev: `air` (live reload Go, config di `.air.toml`; pasang: `go install github.com/air-verse/air@latest`) + `cd web && bun run dev` (Vite :5173 mem-proxy ke :20128; port lain: `ROUTER_GO_PORT=8080 bun run dev`).
Test: `go vet ./... && go test ./... && (cd web && bun run typecheck)`.

Pakai: buat node di **Providers** (prefix mis. `mygw`), tambah key, buat API key di **API Keys**, lalu
`curl localhost:20128/v1/chat/completions -H "Authorization: Bearer <key>" -d '{"model":"mygw/<model>","messages":[...]}'`.
