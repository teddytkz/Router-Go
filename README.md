# Router-Go

Gateway AI dengan dashboard web, satu binary Go (dashboard React di-embed). Satu endpoint untuk banyak provider: klien cukup bicara format OpenAI, Anthropic, Gemini atau Ollama, Router-Go menerjemahkan, memilih key, dan melakukan fallback.

Ditulis ulang dari XRouter-Next ke Go. Skema database sama dengan XRouter-Next.

Dokumen lain: [`docs/PRD.md`](docs/PRD.md) · [`docs/PORTING_CHECKLIST.md`](docs/PORTING_CHECKLIST.md)

## Fitur

- **Banyak format, satu gateway.** Request dan response diterjemahkan antar format `chat/completions`, `responses`, `messages` (Anthropic) dan Gemini.
- **Provider.** Built-in (lihat tabel di bawah) dan custom node OpenAI-compatible atau Anthropic-compatible dengan prefix sendiri.
- **Key pool + fallback.** Beberapa key per provider dengan prioritas, round-robin atau sticky. Key yang kena rate limit atau ditolak otomatis dilewati, ada cooldown dan circuit breaker.
- **Combo.** Satu nama model yang berisi beberapa model, dicoba berurutan.
- **Model masking.** Nama alias yang dikirim klien (`fast`) dipetakan ke model atau combo sungguhan, jadi isi di belakangnya bisa diganti tanpa mengubah klien.
- **Custom model.** Daftarkan model manual untuk upstream yang tidak punya endpoint `/models`. Capability (vision, reasoning, audio, context window) bisa di-override.
- **API key gateway** dengan access list per key.
- **Proxy pools.** Arahkan traffic sebuah key lewat proxy (http/https/socks5), atau lewat relay yang di-deploy ke akun Vercel atau Cloudflare milikmu.
- **Usage, quota, console log, token saver** di dashboard.

## Provider built-in

| Provider | Prefix | Login | Status |
|---|---|---|---|
| OpenCode Free | `oc` | tanpa key | terbukti live |
| Freebuff | `fb` | tanpa key, lewat CLI resmi vendor | terbukti live |
| Cline | `cl` | OAuth | ready |
| CodeBuddy | `cbai` | OAuth | uji dengan mock |
| Qoder | `qd` | Personal Access Token (`pt-…`) | uji dengan mock |
| Kiro | `kr` | OAuth AWS Builder ID, atau tempel refresh token / JSON | experimental |

Provider berstatus *experimental* bisa ditolak upstream sewaktu-waktu. Tiap provider ada di `internal/provider/<nama>` dan mendaftar lewat `provider.Register`.

## Mulai cepat

Butuh Go dan [Bun](https://bun.sh).

```bash
make build            # build dashboard (web/dist) lalu binary ./router-go
./router-go           # http://localhost:20128
```

Tanpa `make`:

```bash
cd web && bun install && bun run build && cd ..
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o router-go ./cmd/router-go
```

Login pertama: password awal `123456`, **hanya bisa dari localhost**. Ganti segera di Settings. Dashboard yang dibuka dari luar menolak password bawaan.

```bash
./router-go --port 8080          # port lain
./router-go --reset-password     # lupa password
```

Urutan prioritas port: `--port` > env `PORT` / `ROUTER_GO_ADDR` > Settings > `20128`. Port juga bisa diubah di **Settings → Port**, server pindah saat itu juga tanpa restart.

## Cara pakai

1. Buka **Providers**, pilih provider built-in atau buat node custom (beri prefix, mis. `mygw`).
2. Tambah key (**API Key**) atau login lewat **OAuth** di halaman provider.
3. Buat API key gateway di **API Keys**.
4. Panggil gateway:

```bash
curl localhost:20128/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"mygw/<model>","messages":[{"role":"user","content":"halo"}]}'
```

Nama model memakai format `prefix/model`, atau nama combo, atau nama mask. Daftar model yang tersedia: `GET /v1/models`.

## Endpoint gateway

Autentikasi: header `Authorization: Bearer <api-key>`. Cookie tidak pernah dipakai di `/v1`.

| Endpoint | Fungsi |
|---|---|
| `POST /v1/chat/completions` | Chat (format OpenAI) |
| `POST /v1/responses`, `/v1/responses/compact` | Responses API |
| `POST /v1/messages`, `/v1/messages/count_tokens` | Format Anthropic |
| `POST /v1/embeddings` | Embedding |
| `POST /v1/images/generations` | Gambar |
| `POST /v1/audio/speech`, `/v1/audio/transcriptions` | Suara |
| `POST /v1/videos/generations`, `/edits`, `/extensions`; `GET /v1/videos/{id}` | Video |
| `GET /v1/models`, `/v1/models/info`, `/v1/models/*` | Daftar dan detail model |
| `POST /v1/api/chat` | Format Ollama |
| `GET/POST /v1beta/models/*` | Format Gemini |

## Dashboard

Ada di `/dashboard`: Providers, Combos, Model Masking, Token Saver, API Keys, Console Log, Usage, Proxy Pools, Quota, Settings.

**Keamanan:** satu password (tanpa username) disimpan sebagai hash bcrypt. Login sukses mengeluarkan JWT (HS256, berlaku 24 jam) di cookie `auth_token` yang `HttpOnly`. Ada pembatas percobaan login. Token sesi tidak bisa dicabut sebelum kedaluwarsa.

## Konfigurasi

Data disimpan di `~/.router-go` (Windows: `%USERPROFILE%\.router-go`), database di `db/data.sqlite`. Secret JWT dibuat otomatis dan disimpan di file `jwt-secret` di folder itu, jadi sesi tetap valid setelah restart.

| Env | Fungsi |
|---|---|
| `PORT`, `ROUTER_GO_ADDR` | Port, atau `host:port` |
| `HOSTNAME_BIND` | Alamat bind, kosong = semua interface |
| `ROUTER_GO_DATA_DIR` / `DATA_DIR` | Folder data |
| `INITIAL_PASSWORD` | Password awal |
| `JWT_SECRET` | Secret JWT (default: dibuat otomatis) |
| `API_KEY_SECRET` | Secret untuk API key gateway |
| `AUTH_COOKIE_SECURE=true` | Paksa flag `Secure` pada cookie (otomatis aktif lewat HTTPS) |
| `TRUST_PROXY=true` | Percayai header proxy di depan Router-Go |

Kalau Router-Go dipasang di belakang reverse proxy HTTPS, set `TRUST_PROXY=true` dan pastikan proxy mengirim `X-Forwarded-Proto`.

## Struktur

```
cmd/router-go/       entry point
internal/api/        API dashboard (/api) dan SPA
internal/gateway/    gateway /v1: routing, key pool, fallback, breaker
internal/translate/  terjemah antar format (OpenAI, Anthropic, Gemini)
internal/provider/   provider built-in (cline, codebuddy, freebuff, kiro, opencode-free, qoder)
internal/auth/       login dashboard (bcrypt + JWT) dan limiter
internal/db/         SQLite: koneksi, key, alias, usage
internal/relay/      relay Vercel/Cloudflare untuk proxy pool
web/                 dashboard React (Vite + TypeScript)
```

## Pengembangan

```bash
make dev                  # Go dengan live reload (air) di :20128
cd web && bun run dev     # Vite :5173, mem-proxy ke :20128
```

Pasang `air`: `go install github.com/air-verse/air@latest`. Config di `.air.toml`. Untuk backend di port lain: `ROUTER_GO_PORT=8080 bun run dev`.

Test: `make test`, sama dengan `go vet ./... && go test ./... && (cd web && bun run typecheck)`.
