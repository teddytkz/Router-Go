# PRD — Router-Go

Rewrite dari **XRouter-Next** (Next.js, `~/9router/XRouter-Next`) ke **Go**, dengan dashboard React yang di-embed ke binary Go.
Referensi implementasi Go: <https://github.com/luqman-v1/9router-go> (dipakai sebagai acuan struktur, bukan di-fork).

Status dokumen: **v1 — 2026-11-09 (fase 1 diimplementasi)**. Checklist porting ada di [`PORTING_CHECKLIST.md`](./PORTING_CHECKLIST.md) dan di-update tiap fitur selesai.

---

## 1. Latar belakang & tujuan

XRouter-Next adalah gateway AI lokal: satu endpoint OpenAI-compatible (`/v1/*`) yang me-route ke 40+ provider, plus dashboard Next.js. Masalahnya: butuh Node/Next runtime, memori besar, dan startup lambat.

**Tujuan Router-Go**
1. Satu binary Go (tanpa Node saat runtime) yang melayani API gateway **dan** dashboard.
2. Data kompatibel dengan XRouter-Next (skema SQLite sama) supaya bisa migrasi tanpa konversi.
3. Dashboard baru yang rapi, cepat, dan konsisten (dark/light).
4. Port bertahap; fase 1 hanya **custom provider OpenAI-compatible**.

**Non-goal fase 1**: provider OAuth (Claude, Codex, Kiro, dll.), translator format (Claude/Gemini), combo, tunnel, MITM, CLI tools, media (image/audio/video), SSO (OIDC/SAML), cloud sync, tray/CLI launcher.

## 2. Keputusan teknis

| Aspek | Keputusan | Alasan |
|---|---|---|
| Backend | Go (lihat go.mod), `net/http` + `chi` v5 | Sama seperti referensi, router ringan |
| SQLite | `modernc.org/sqlite` (pure Go, tanpa CGO) | Cross-compile Windows/Linux mudah |
| Skema DB | **Identik** dengan `src/lib/db/schema.js` XRouter-Next (tabel `settings`, `providerConnections`, `providerNodes`, `apiKeys`, `usageHistory`, `kv`, dst.) | Kompatibel data lama |
| Auth dashboard | bcrypt hash di `settings.password`, JWT HS256 di cookie `auth_token` (24 jam), login limiter per IP | Perilaku sama dengan Next |
| Frontend | React 19 + TypeScript + Vite + **TanStack Query** + Tailwind v4 + react-router | Sesuai permintaan |
| Embed | `//go:embed web/dist`, SPA fallback ke `index.html`, aset hashed `immutable` | Satu binary |
| Lokasi data | `~/.router-go` (Linux/macOS), `%USERPROFILE%\.router-go` (Windows). Override: `ROUTER_GO_DATA_DIR` / `DATA_DIR`. DB: `<data>/db/data.sqlite` | Sesuai permintaan |
| Port | Default `20128` (sama dengan XRouter-Next). Prioritas: `--port` > env `PORT`/`ROUTER_GO_ADDR` > `settings.port` > default. Ubah dari Settings memindahkan listener langsung (port baru di-bind dulu; port terpakai → 409, listener lama ditutup setelah 1 dtk) | Klien lama tetap jalan |

### Struktur repo

```
Router-Go/
├── cmd/router-go/main.go        # entrypoint
├── internal/
│   ├── provider/                # provider bawaan: provider.go (tipe + registry), builtin/ (impor semua), <nama>/ satu paket per provider
│   ├── config/                  # env, data dir, JWT secret
│   ├── db/                      # open sqlite, schema bootstrap, repos (settings, nodes, connections, keys, usage)
│   ├── auth/                    # bcrypt, JWT cookie, login limiter, API key gen/validate
│   ├── gateway/                 # /v1/* : model resolve, account fallback, upstream proxy, SSE passthrough
│   └── api/                     # /api/* dashboard handlers + router
├── web/                         # React SPA (src/), embed.go, dist/
├── docs/
└── Makefile
```

## 3. Fase 1 — Custom Provider OpenAI-compatible

### 3.1 User story
- Sebagai operator, saya login ke dashboard (password default `123456`, bisa diganti).
- Saya menambah **provider node** OpenAI-compatible: nama, prefix, base URL, tipe API (`chat` / `responses`).
- Saya menambah satu atau lebih **API key upstream** (key pool) ke node itu, mengetes koneksinya, dan melihat daftar model upstream (`GET {baseUrl}/models`).
- Saya membuat **API key gateway** untuk klien (`sk-{machineId}-{keyId}-{crc8}`).
- Klien memanggil `POST /v1/chat/completions` dengan `model: "<prefix>/<model>"`; gateway meneruskan (stream maupun non-stream) ke upstream, memutar key bila gagal.
- Saya melihat penggunaan (request, token, latensi, status) di halaman Usage.

### 3.2 Fungsional

**Gateway (`/v1`)**
- `POST /v1/chat/completions` — passthrough ke `{baseUrl}/chat/completions`; SSE di-stream apa adanya (flush per chunk, cancel saat klien putus).
- `POST /v1/responses` — passthrough ke `{baseUrl}/responses` untuk node `apiType=responses`.
- `POST /v1/embeddings` — passthrough ke `{baseUrl}/embeddings`.
- `GET /v1/models` — model tiap node (`prefix/model`) dari `GET {baseUrl}/models` upstream (cache 5 menit). `customModels` (kv) ditunda ke fase 2.
- Resolusi model: `prefix/model` → node dengan `prefix` tsb; alias model (`kv.modelAliases`) didukung baca-saja.
- Pasangan format yang tidak cocok (mis. klien `/chat/completions` ke node `responses`) → `400` dengan pesan jelas (translator = fase 2).
- Auth klien: `Authorization: Bearer <key>` atau `x-api-key`; wajib bila `settings.requireApiKey != false`.
- **Account fallback**: pilih koneksi aktif berurutan `priority`; skip yang `rateLimitedUntil` belum lewat; pada 401/402/403/404/429/5xx tandai cooldown (429/rate-limit = exponential backoff 2s→5m; 401/402/403/404 = 2 menit; error transient/5xx = 30 detik; pola akun suspended = 24 jam; konstanta identik XRouter-Next) lalu coba koneksi berikut. Error request-scoped (4xx lain) langsung dikembalikan. Sukses → reset status.
- Pencatatan usage ke `usageHistory` (provider, model, connectionId, apiKey, endpoint, token, latensi, status).
- Header rahasia (`Authorization` klien) tidak pernah diteruskan ke upstream; upstream hanya dapat key koneksi.
- SSRF: base URL node divalidasi (skema http/https). Private/loopback **diizinkan** (self-hosted, mis. Ollama) karena hanya admin berlogin yang bisa menambah node.

**Dashboard API (`/api`)** — path sama dengan XRouter-Next bila ada:
`auth/{login,logout,status}`, `keys[/{id}]`, `provider-nodes[/{id}]`, `provider-nodes/validate`, `providers[/{id}]`, `providers/{id}/test`, `providers/{id}/models`, `settings`, `usage/stats`, `usage/history`, `health`, `version`.

**Dashboard UI (React)**
| Halaman | Isi |
|---|---|
| Login | Form password, pesan lockout |
| Dashboard | Pemilih rentang (24h/7d/30d); kartu total request, total input, total output, total cache; grafik request & token (input/output/cache) per jam/hari; grafik model paling sering dipakai |
| Providers | Daftar node + status, tambah/edit/hapus node, kelola key pool per node, tes koneksi, lihat model |
| API Keys | Base URL gateway, daftar & buat API key gateway (+ access list), contoh `curl`/SDK |
| Usage | Tabel riwayat request (filter provider/model/status), total token |
| Settings | Ganti password, toggle `requireLogin` / `requireApiKey`, info data dir & versi |

Desain: sidebar kolaps, tema dark/light mengikuti OS + toggle, palet netral dengan satu aksen, tipografi Inter + JetBrains Mono, skeleton loading, toast, empty state, responsif (mobile drawer), keyboard-accessible.

### 3.3 Non-fungsional
- Startup < 300 ms, memori idle < 60 MB.
- Streaming tanpa buffering penuh; tidak ada goroutine bocor saat klien putus.
- SQLite WAL, `busy_timeout=5000`, `foreign_keys=ON`.
- Secret: `JWT_SECRET` env atau file `<data>/jwt-secret` (0600). Cookie `HttpOnly`, `SameSite=Lax`, `Secure` bila `X-Forwarded-Proto=https` atau `AUTH_COOKIE_SECURE=true`.
- Login limiter: 5 gagal → lock bertahap per IP; IP diambil dari koneksi TCP (header `X-Forwarded-For` hanya dipercaya dari loopback).
- `apiKey` upstream tidak pernah dikirim balik utuh ke UI (masked).
- Default password `123456` hanya boleh dipakai dari localhost; dari remote, login ditolak sampai password diganti / `INITIAL_PASSWORD` kuat di-set (meniru perilaku Next).

### 3.4 Model data (kompatibel)

- `providerNodes(id, type, name, data{prefix,apiType,baseUrl}, createdAt, updatedAt)` — `id = "openai-compatible-{apiType}-{uuid}"`, `type="openai-compatible"`.
- `providerConnections(id, provider=<node id>, authType="apikey", name, priority, isActive, data{apiKey, providerSpecificData{prefix,apiType,baseUrl,nodeName}, testStatus, lastError, lastErrorAt, errorCode, rateLimitedUntil, backoffLevel,…})`.
- `apiKeys(id, key, name, machineId, isActive, createdAt, accessRestricted, accessAllow)`.
- `settings(id=1, data JSON)` — `password`, `requireLogin`, `requireApiKey`, dst.
- `usageHistory`, `kv`.
- Tabel lain dari skema XRouter-Next dibuat kosong agar DB bisa dibuka balik oleh Next.

### 3.5 Kriteria selesai (acceptance)
1. `make build` menghasilkan satu binary; `./router-go` membuka dashboard di `:20128`.
2. Login → tambah node `https://api.openai.com/v1` (atau mock lokal) → tambah key → tes sukses.
3. `curl /v1/chat/completions` stream & non-stream melalui `prefix/model` berhasil; dengan 2 key dan key pertama 429, request jatuh ke key kedua.
4. Usage muncul di dashboard.
5. DB yang dibuat Router-Go bisa dibaca XRouter-Next dan sebaliknya (tabel/kolom sama) untuk 5 tabel fase 1.
6. `go vet`, `go test ./...`, `tsc --noEmit` hijau; ada test untuk fallback, auth, resolve model, streaming passthrough.

## 3b. Fase 2a — Translator, combo, anthropic-compatible, custom models, key access

Lanjutan fase 1 (diminta langsung setelah fase 1). Spesifikasi di sini jadi kontrak backend ↔ frontend.

### Format & translator
- Tiga format klien/upstream: `chat` (`/v1/chat/completions`), `responses` (`/v1/responses`), `messages` (Anthropic `/v1/messages`, **endpoint baru**).
- Format upstream sebuah node: openai-compatible `apiType` = `chat` | `responses`; anthropic-compatible = `messages`.
- Jika format klien ≠ format upstream, gateway menerjemahkan lewat hub `chat`: request `klien→chat→upstream`, response `upstream→chat→klien`, termasuk streaming SSE (event per event, tanpa buffering penuh), tool calls, system prompt, gambar base64, usage. Paket: `internal/translate`.
- Error ke klien memakai bentuk error format klien (OpenAI `{error:{message,type,code}}`, Anthropic `{type:"error",error:{type,message}}`).

### Node anthropic-compatible
- `providerNodes.type="anthropic-compatible"`, id `anthropic-compatible-{uuid}`, `data{prefix, baseUrl}` (tanpa `apiType`).
- Upstream: `POST {baseUrl}/messages`, header `x-api-key` + `anthropic-version: 2023-06-01`; model list `GET {baseUrl}/models`.
- Key pool, fallback, cooldown identik dengan node openai-compatible. `POST /api/provider-nodes` menerima `type` (`openai-compatible` default | `anthropic-compatible`).

### Combo
- Tabel `combos` (sudah ada). Combo = nama (tanpa `/`) + daftar model (`prefix/model` atau alias) berurutan. Klien memakai `model: "<nama combo>"`.
- Strategi: `fallback` (urut; lanjut ke anggota berikut bila anggota gagal/cooling/menolak request) dan `round-robin` (rotasi titik awal tiap request; sticky limit = `settings.comboStickyRoundRobinLimit`, default 1). Strategi per-combo disimpan di `settings.comboStrategies[name].fallbackStrategy`, default `settings.comboStrategy` (`fallback`), kompatibel XRouter-Next.
- Error request-scoped (4xx lain) dari satu anggota → lanjut ke anggota berikutnya tanpa cooldown; bila semua gagal, kembalikan respons anggota request-scoped pertama; selain itu 503 ringkasan + `Retry-After` bila ada cooldown.
- API: `GET /api/combos` → `{combos:[{id,name,kind,models[],strategy,context_length,createdAt,updatedAt}]}`; `POST /api/combos {name,models[],strategy?,context_length?}`; `PUT /api/combos/{id}` (field sama, parsial); `DELETE /api/combos/{id}`. Nama unik (409), `^[A-Za-z0-9_.-]{1,64}$`, models ≥1 string.
- `GET /v1/models` ikut menampilkan combo (`owned_by:"combo"`).

### Model alias & custom model
- `kv(scope='modelAliases')` key=alias value=`prefix/model`; `kv(scope='customModels')` key=`{prefix}|{id}|llm` value=`{providerAlias,id,type:"llm",name}` (format XRouter-Next).
- API: `GET /api/models/alias` → `{aliases:{alias:model}}`; `PUT /api/models/alias {alias,model}`; `DELETE /api/models/alias?alias=`. `GET /api/models/custom` → `{models:[{providerAlias,id,type,name}]}`; `POST /api/models/custom {providerAlias,id,name?}`; `DELETE /api/models/custom?providerAlias=&id=`.
- Custom model dipakai untuk upstream yang tidak mengekspos `/models`; muncul di `/v1/models` dan daftar model dashboard (digabung, tanpa duplikat).

### Per-key access list
- Kolom `apiKeys.accessRestricted` / `accessAllow` (JSON array). Default tak terbatas. Key *restricted* hanya boleh memanggil combo/model di allow list (cocok persis, case-insensitive, pada identitas **ter-resolve** `nodeId/model`; alias dan prefix sama dianggap satu target; combo yang diizinkan memberi anggotanya hanya bila lewat combo itu).
- Fail-closed: allow list rusak → tidak boleh apa pun. Ditolak = `403` `This API key is not allowed to use model "<requested>"` sebelum kredensial upstream disentuh. `/v1/models` disaring sesuai allow list.
- API: item key di `GET/POST /api/keys` memuat `access:{restricted,allow[]}`; `POST /api/keys` boleh menerima `access`; `PATCH /api/keys/{id}` menerima `access` (validasi: objek `{restricted:bool,allow:string[]}`, ≤200 entri, ≤256 karakter/entri; invalid → 400).

### Di luar fase 2a (tetap fase 2b+)
pricing, Gemini `/v1beta`, `/v1/messages/count_tokens`, round-robin antar *koneksi* (account semaphore), capability auto-switch, smart/fusion combo, Dockerfile.

## 3c. Fase 2c — Token Saver, Guards & Shields, halaman Usage

- **Ponytail** (`settings.ponytailEnabled`, `ponytailLevel` lite|full|ultra) dan **Watermarks Remover** (`watermarksRemoverEnabled`): menyuntik instruksi ke system prompt body upstream (setelah translasi; teks prompt sama dengan XRouter-Next). Idempoten; tidak mengubah body klien yang dipakai anggota combo lain. Watermarks Remover hanya lewat prompt, tidak menyaring respons. **Custom system prompt** (`customPromptEnabled`, `customPrompt` ≤8000 karakter). Urutan: ponytail → watermarks → custom → loop guard. Token saver lain (RTK, caveman, headroom, pxpipe) tidak di-port.
- **Loop Guard** (`loopGuardEnabled`, default aktif): mendeteksi tool call identik ≥3×, urutan tool call berulang ≥2×, atau teks asisten/kalimat berulang ≥3× pada riwayat request; lalu menyuntik termination prompt dan `[ROUTER NOTE: …]` di pesan terakhir.
- **Circuit Breaker** (`circuitBreakerEnabled`, default aktif): per node; 5 kegagalan sisi provider (408/500/502/503/504/520/524 atau error jaringan) dalam 60 dtk → open; request gagal cepat (503 + `Retry-After`) sehingga combo lanjut ke anggota berikut; setelah 30 dtk satu probe (half-open), gagal → open lagi dengan tunggu ×2 tiap 3 probe gagal (maks ×16). Beda dari asli: kegagalan dihitung dalam jendela geser dan dihapus oleh sukses.
- **Semaphore** (`semaphoreEnabled`, default aktif): maks 3 request paralel per koneksi (`providerSpecificData.maxConcurrency`, 0 = tanpa batas), antrian FIFO ≤20, tunggu ≤30 dtk; koneksi yang masih punya slot dipilih lebih dulu.
- API: `PATCH /api/settings` menerima kunci di atas (validasi level/panjang). UI: halaman **Token Saver**.
- **Kapabilitas model** (`kv customModels[].caps`): vision, reasoning, audioInput, contextWindow. Terdeteksi dari id model (`internal/modelcaps`: pola keluarga model, ukuran pada nama seperti `-32k`/`-1m`, tabel jendela konteks) dan dapat di-override per model (`PATCH /api/models/custom`); override selalu menang, deteksi hanya menyalakan. Konteks tidak dikenal (0) tidak pernah menyingkirkan model.
- **Routing sadar kapabilitas**: dari request dibaca kebutuhan: gambar/audio pada giliran user saat ini = wajib (hard), parameter reasoning/thinking = pilihan (soft), estimasi token (≈4 karakter/token, 1000 per gambar/berkas, + `max_tokens`) terhadap jendela konteks. Anggota combo diurutkan stabil: memenuhi semua → memenuhi yang wajib → sisanya; yang tak muat konteks atau tak punya kapabilitas wajib ke belakang (tidak dibuang, fallback tetap jalan).
- **Capacity Adapter** (`settings.capacityAdapter.{vision,audioInput} = {enabled, roundRobin, models[]}`): bila tak ada kandidat yang memenuhi kapabilitas wajib, pool adapter disisipkan di depan (model asli tetap sebagai fallback); key restricted hanya diarahkan ke model adapter yang diizinkan. Tidak ada pemangkasan riwayat untuk jendela konteks adapter (belum).
- **Provider bawaan** (`internal/provider/<nama>`): cline, codebuddy, qoder, freebuff, opencode-free, kiro (lihat "Kiro" di bawah). Tiap paket mendaftar lewat `provider.Register` di `init()`; `provider/builtin` mengimpor semuanya. Provider bawaan diperlakukan seperti node (prefix = alias, mis. `cl/<model>`; key pool = koneksi dengan `provider = <id registry>`), sehingga fallback, combo, translasi, usage, dan access list langsung berlaku. Katalog model registry langsung bisa dipakai (aturan "harus di-add" hanya untuk provider custom). Quirk per provider: stream paksa + perakitan respons non-stream, amplop `{success,data}`, format per model (responses/messages), header statis. Alias terpakai tidak bisa dipakai node custom. `GET /api/builtin-providers` mendaftar registry. Auth: API key/token, dan login OAuth browser untuk provider yang mendukungnya. Penyamaran identitas klien resmi (UA/versi CLI, fingerprint TLS) **sengaja tidak** di-port, jadi CodeBuddy, Freebuff, OpenCode Free bisa ditolak upstream; Qoder (protokol bertanda tangan sendiri) terdaftar tetapi belum bisa dirute.
- **Login OAuth** (`POST /api/oauth/{provider}/start|exchange|poll`): alurnya berbeda per provider dan ditulis di paketnya (`internal/provider/<nama>/oauth.go`). `code` (Cline): login URL → browser kembali ke `/oauth/callback?code=…` (token ada di dalam kode, base64) → `exchange`. `poll` (CodeBuddy: state + `authUrl`; Freebuff: fingerprint perangkat + `loginUrl`): dashboard memanggil `poll` sampai token tiba. Token disimpan di koneksi `authType=oauth` (tidak pernah dikirim ke UI), di-refresh otomatis 2 menit sebelum kedaluwarsa (satu refresh per koneksi), dan dikirim sebagai key (Cline: awalan `workos:`). Login ulang akun yang sama memperbarui koneksi yang ada; login pertama mengaktifkan katalog model. Header dinamis per request (id request CodeBuddy) dan header default registry (Cline, CodeBuddy: data XRouter) dikirim juga pada panggilan login; semuanya bisa dilihat dan diubah di kartu Custom Headers.
- **Pengaturan provider bawaan**: model katalog harus di-enable dulu (kv `customModels`, sama dengan provider custom; kunci pertama otomatis mengaktifkan seluruh katalog; `POST /api/builtin-providers/{id}/models {enable-all|disable-all}`). **Custom headers** per provider (`PUT/DELETE /api/builtin-providers/{id}/headers`, kv `providerHeaders`): override di atas header default registry; `Authorization`/`x-api-key`/`Host`/dll ditolak (kredensial milik API key). **Key pool** per provider (`PUT /api/provider-config/{id}`, kv `providerConfig`): `roundRobin` + `sticky` memutar key awal tiap request.
- **Quota Tracker** (`/dashboard/quota`): pelacak pemakaian per provider dan per koneksi (hari ini / 7 hari / bulan ini, terakhir dipakai) dari `GET /api/quota` (`connections`, `usage`, `limits`); batas bersifat lapisan opsional yang bisa dipasang langsung pada provider atau key. Batas disimpan di kv `quotaLimits`: batas `requests` atau `tokens` per `provider` atau `connection`, jendela `hour|day|week|month`, `calendar` (batas waktu lokal server; minggu mulai Senin) atau `rolling`. Pemakaian dihitung dari `usageHistory` (cancelled tidak dihitung). Dengan `enforce`, gateway melewati target yang kuotanya habis (key berikutnya / anggota combo menggantikan; semua habis → 429 + `Retry-After`); tanpa `enforce` hanya pelacakan. `GET/POST /api/quota`, `PUT/DELETE /api/quota/{id}`.
- **Model harus di-add dulu**: sebuah model hanya bisa dipakai klien setelah ditambahkan ke provider-nya (`kv customModels`, via Providers → Models). Daftar `/models` upstream hanya katalog untuk dipilih; model yang belum di-add ditolak `404 model_not_added` (alias dan combo mengikuti aturan yang sama; key *restricted* mendapat 403 tanpa membocorkan nama). `/v1/models` hanya menampilkan model yang sudah di-add. `GET /api/provider-nodes/{id}/models` → `{models (added), upstream, error?}`.
- **Console log** (`/dashboard/console-log`): buffer 1000 baris di memori (`internal/consolelog`); `GET /api/console-logs/stream` (SSE: `init`, `lines`, `clear`), `GET`/`DELETE /api/console-logs`. Gateway mencatat satu baris per request, kegagalan upstream/cooldown, fallback combo, circuit breaker, loop guard, antrian semaphore, dan tes model.
- **Dashboard CPU/RAM**: `GET /api/system` hanya untuk proses Router-Go (CPU % dari kapasitas mesin, RSS, Go heap, goroutine; riwayat 2 menit).
- **Usage**: `GET /api/usage/summary?period=month|today|24h|7d|30d|60d|all` (total, seri per jam/hari, breakdown per model/provider/akun/API key/endpoint, live 5 menit, recent) dan `POST /api/usage/reset`. Token cache dicatat di kolom `tokens` (`cached_tokens`).

### Endpoint tambahan & format klien Gemini (selesai, F2b)
- **Format klien baru `gemini`** (`/v1beta/models/{model}:generateContent|streamGenerateContent|countTokens`, `GET /v1beta/models[/{model}]`). Desain: `serve("gemini")` mengubah body Gemini menjadi request `chat` di awal (`geminiRequest`: model dari URL, `stream` dari aksi `:streamGenerateContent`, bukan dari body), sehingga routing, combo, capability, key pool, usage dan token saver tidak berubah. Hanya sisi respons yang tahu format `gemini`: `relayConverted`/`relayAggregated` merender hub → Gemini (JSON atau SSE), dan error memakai `{error:{code,message,status}}` (`INVALID_ARGUMENT`, `UNAUTHENTICATED`, `NOT_FOUND`, `RESOURCE_EXHAUSTED`, …). Upstream tetap `chat`/`responses`/`messages` (tidak ada node Gemini). Kode: `internal/translate/gemini.go`, `internal/gateway/gemini.go`.
- Key API Gemini: `Authorization: Bearer`, `x-goog-api-key`, atau `?key=` (query hanya untuk `/v1beta`).
- `GET /v1/models/info?id=<prefix/model>` dan `GET /v1/models/{prefix/model}`: info satu model (`id`, `owned_by`, `capabilities{vision,reasoning,audioInput}`, `contextWindow` bila diketahui) dari daftar `/v1/models` yang boleh dipakai key; 404 `model_not_found` bila tidak ada/tidak boleh.
- `POST /v1/messages/count_tokens` → `{input_tokens}` dan Gemini `:countTokens` → `{totalTokens}`: **estimasi lokal** (~4 karakter/token, gambar 1000), tanpa memanggil upstream.
- `POST /v1/responses/compact`: seperti `/v1/responses`, tetapi node `responses` dipanggil di `/responses/compact`.
- `POST /v1/api/chat` (Ollama): memakai pipeline chat; `stream` default true; jawaban NDJSON `{model,created_at,message,done}` (tool call dikirim utuh di objek `done`). Dibuat sebagai pembungkus `ResponseWriter` di atas `serve("chat")` (`internal/gateway/endpoints.go`).


### Kiro — provider dengan protokol sendiri (selesai, eksperimental)
- **Hook `Native`** (`provider.Provider.Native`, `internal/provider/native.go`): untuk upstream yang tidak berbahasa OpenAI. Provider menyediakan `Endpoints(base, psd)` (daftar URL berurutan), `Request(ctx, NativeCall)` (token, `providerSpecificData` koneksi, id model upstream, body **chat**, flag stream) dan `Response(resp, model, stream)` yang mengubah respons upstream 2xx menjadi respons chat-completions biasa (SSE + `usage` bila stream, JSON bila tidak; di Kiro lewat `io.Pipe` + goroutine decoder, dan menutup body upstream bila klien pergi). Di `attempt` (`proxy.go`) provider ber-`Native` dipanggil lewat `nativeDo` (`internal/gateway/native.go`) dengan `g.Client` dan `ConnCtx` (proxy pool tetap berlaku); format upstream selalu `chat`, jadi klien chat/responses/messages/gemini, combo, rotasi key, kuota, circuit breaker, usage dan peek respons kosong bekerja tanpa perubahan. Status >= 400 dikembalikan apa adanya (klasifikasi/cooldown lama); 401/403/404 pindah ke endpoint berikutnya, status lain tidak. Tes key (`ProbeModel`/`ValidateBuiltin`) lewat jalur yang sama dengan request kecil sungguhan (`probeNative`; `providerSpecificData` koneksi dibawa di context oleh `ConnCtx`).
- **Protokol** (`internal/provider/kiro`): `POST /generateAssistantResponse` dengan body `conversationState` (giliran user/assistant bergantian, tool definition di giliran terakhir, tool call dipasangkan dengan hasilnya; yang tidak berpasangan atau tanpa definisi diratakan jadi teks), respons berupa AWS event stream biner (frame + CRC32 diverifikasi, frame terpotong = error, bukan jawaban pendek). Endpoint berurutan: `q.<region>.amazonaws.com`, `codewhisperer.us-east-1.amazonaws.com` (+ header `X-Amz-Target`), `runtime.us-east-1.kiro.dev`; region hanya `us-east-1`/`eu-central-1` yang boleh masuk nama host. Akhiran model buatan router: `-thinking` (menambah blok `<thinking_mode>`, budget dari `reasoning_effort`/`thinking.budget_tokens`, default 16000, permintaan eksplisit mengalahkan akhiran) dan `-agentic` (prompt tulis-bertahap); id model dinormalisasi ke ejaan Kiro (`claude-sonnet-4-5-2025…` → `claude-sonnet-4.5`). Teks di dalam `<thinking>` menjadi `reasoning_content`. Tool call dikumpulkan sampai akhir stream; usage dari `metricsEvent`, atau perkiraan dari `contextUsageEvent` bila tidak ada.
- **Metode auth** (`providerSpecificData.authMethod`): `builder-id` — tombol OAuth, alur device-code AWS (`poll`: daftar client OIDC publik → `device_authorization` → polling `/token`); refresh ke AWS OIDC dengan clientId+clientSecret hasil pendaftaran. `google`/`github`/`imported` (social) — tempel refresh token di dialog Add key; refresh ke `prod.us-east-1.auth.desktop.kiro.dev/refreshToken`. `idc` (IAM Identity Center) — tempel JSON `{"refreshToken","clientId","clientSecret","region"}`; refresh ke `oidc.<region>.amazonaws.com/token`. `external_idp` (Entra) — tempel JSON `{"authMethod":"external_idp","refreshToken","clientId","tokenEndpoint","scope","profileArn"}`; refresh ke token endpoint IdP (hanya host login Microsoft via https) dan request membawa header `TokenType: EXTERNAL_IDP`. Impor (`OAuth.Import`) memvalidasi dengan satu kali refresh sebelum menyimpan; yang ditempel bukan API key, jadi disimpan sebagai koneksi OAuth. Tidak di-port: login browser Google/GitHub (redirect `kiro://`) dan device-code IDC dengan start URL sendiri (keduanya bisa lewat impor), serta auth API key Kiro.
- **Refresh** (`OAuth.RefreshConn`): varian refresh yang juga menerima `providerSpecificData` koneksi dan boleh mengembalikan `Tokens.Extra` yang digabung ke data koneksi (`UpdateTokens`); dipakai untuk `profileArn` yang baru diketahui (dari jawaban refresh, atau `ListAvailableProfiles`). `secret()` memperbarui map data koneksi yang sedang dipakai request, jadi ARN baru langsung ikut di request yang memicu refresh. Tanpa ARN: ARN default publik (Builder ID / social) dipakai, kecuali akun `idc`/`external_idp` (ARN default milik akun lain → 403). `clientSecret` tidak pernah ikut daftar koneksi di API.
- **Header**: hanya header statis registry (`User-Agent`, `X-Amz-User-Agent`; bisa diubah di kartu Custom Headers) ditambah header protokol (`Accept` event stream, `Authorization`, `TokenType`, ARN profil, `X-Amz-Target`). Tidak di-port: penanda identitas IDE (`x-amzn-kiro-agent-mode`, `machine-id`, header SDK, fingerprint per akun di `kiroModels`), session replay, konteks waktu di prompt, gerbang integritas/retry perbaikan, daftar model live (`ListAvailableModels`) dan field effort native (`additionalModelRequestFields`). Kuota Kiro: lihat "Quota dari API provider".
- **Risiko**: Kiro dibuat untuk IDE/CLI Kiro; memakai akunnya lewat router tidak dilisensikan dan akun bisa dibatasi atau ditangguhkan (XRouter-Next menandainya deprecated dengan RISK_NOTICE yang sama). Dicatat di `Provider.Note` (tampil di dashboard). Status `experimental`: semua diuji hanya terhadap mock, belum ke layanan asli.

### Quota dari API provider & katalog free tier (selesai, eksperimental)
Berbeda dengan `/api/quota` (batas buatan user, tidak diubah): ini kuota yang dilaporkan provider sendiri per koneksi, dan jadi data halaman Quota Tracker.
- **`GET /api/usage/{connectionId}[?force=1]`** (login dashboard; rute statis `/usage/stats` dst. tetap didahulukan chi). Bentuk sama dengan XRouter: `{"plan"?, "message"?, "quotas": {"<nama>": {"used","total","remaining"?,"resetAt"(ISO|null),"unlimited"?,"displayName"?,"recurring"?}}, "raw"?}`; `quotas` selalu ada (`{}` bila kosong). Alur (`internal/api/api_usage_conn.go`): koneksi → node bawaan → `Gateway.Secret` (refresh token OAuth bila hampir habis) → `Provider.Usage` dengan `ConnCtx` (proxy pool koneksi), `OAuthClient` (header statis provider + custom headers) dan timeout 15 dtk. Masalah sign-in/upstream selalu menjadi `message` dengan HTTP 200 (bukan 5xx); koneksi tak dikenal 404; provider tanpa hook → `{"message":"Usage API not implemented for <id>","quotas":{}}`. Token tidak pernah ada di respons.
- **Cache** 30 dtk per koneksi di memori, hanya untuk jawaban yang berisi kuota; entri tidak dipakai bila key/token berubah (termasuk setelah refresh); `?force=1` melewatinya. Jawaban berisi pesan tidak di-cache.
- **Hook** `Provider.Usage func(ctx, Doer, UsageCall) (Usage, error)` (`internal/provider/usage.go`; `UsageCall` = `Token`, `Data` (providerSpecificData), `BaseURL` yang bisa di-`Override` di tes). Handler ada di paket provider masing-masing: **Kiro** (`kiro/usage.go`, port `getKiroUsage`: tiga percobaan endpoint codewhisperer GET → POST (`X-Amz-Target`) → Q GET, header `tokentype: API_KEY`/`TokenType: EXTERNAL_IDP`, pesan khusus idc / google-github / lainnya; baris `*_freetrial`), **Freebuff** (`freebuff/usage.go`: **GET** `/freebuff/session`, tidak pernah POST karena POST menghabiskan kuota sesi; `rateLimitsByModel`, `displayName` dari katalog, `recurring: true`), **CodeBuddy intl** (`codebuddy/usage.go`: `POST /billing/meter/get-user-resource`, paket refill (bernama Daily/Weekly/Monthly, `recurring: true`) dipisah dari paket bonus (`Bonus Pack N`, `recurring: false`, `resetAt` = kedaluwarsa)). Cline, OpenCode dan Qoder: XRouter tidak punya handler (Cline/OpenCode) atau handlernya butuh tukar PAT dan Qoder belum bisa dirute di sini, jadi menjawab "not implemented". Hanya header statis yang dideklarasikan provider yang dikirim; header penyamaran IDE Kiro (`KiroIDE`) dan identitas CLI Freebuff tidak di-port.
- **Dashboard tahu provider mana yang mendukung** lewat field boolean `usage` di tiap item `GET /api/builtin-providers` (`builtinView`).
- **`GET /api/free-tiers`** → `{"tiers":[{"providerId","providerName","providerAlias","freeRefresh"|null,"modelId"|null,"modelName"|null}]}` (port `listFreeTiers`, hanya provider bawaan; custom node bukan free tier). Provider gratis = `category: free` atau `Provider.FreeRefresh` terisi (`daily|weekly|monthly|one-time|rolling`; di registry baru Freebuff `daily`). Satu baris provider membawa kadens; baris model hanya untuk provider `category: free` (Kiro, Freebuff, OpenCode). Urutan: kadens (daily, weekly, monthly, rolling, one-time, tak diketahui terakhir), nama provider, id model (baris provider sebelum modelnya).

## 4. Roadmap fase berikutnya (ringkas)

| Fase | Isi |
|---|---|
| 2a | **(§3b, dikerjakan)** translator chat↔responses↔messages, `/v1/messages`, anthropic-compatible node, combo, alias/custom models, per-key access |
| 2b | Translator Gemini (klien, selesai), pricing/cost, Dockerfile |
| 3 | Provider API-key bawaan (OpenAI, Anthropic, Gemini, DeepSeek, …) via registry data-driven |
| 4 | Provider OAuth + token refresh, quota, request details/observability |
| 5 | Media (image/TTS/STT/video), embeddings custom node, MCP |
| 6 | Tunnel, MITM, CLI tools, skills, SSO (OIDC/SAML), launcher/tray, updater |

## 5. Risiko
| Risiko | Mitigasi |
|---|---|
| Skema/format JSON `data` berbeda halus dari Next | Struct Go memakai `map[string]any` untuk field tak dikenal agar round-trip tidak menghilangkan data |
| Skala XRouter-Next sangat besar (37 executor, 40+ translator) | Port bertahap, checklist eksplisit, tiap fase bisa dirilis |
| Streaming SSE bocor/buffer | Test integrasi dengan mock upstream, `http.Flusher`, context cancel |
| Lockout/limit login salah → terkunci | Reset via `router-go --reset-password` (menghapus `settings.password`) |

### Proxy pools (selesai)
- Pool = proxy biasa (`http(s)://`, `socks5://`) atau relay yang di-deploy user ke Vercel (Edge function) / Cloudflare (Worker) memakai token API (dipakai sekali, tidak disimpan).
- Key memilih pool lewat `providerSpecificData.proxyPoolId` (dialog Edit key). Semua panggilan upstream (chat, test key, test model) lewat satu `http.RoundTripper` yang membaca pool dari context.
- `strictProxy`: pool nonaktif → request gagal, bukan jatuh ke IP langsung. Pool terhapus → key kembali langsung.
- Relay memakai secret per-deploy (`x-relay-key`) dan tidak menyimpan log/header apa pun.
- Test key/model selalu streaming (CodeBuddy menolak non-stream; beberapa upstream menjawab stream jauh lebih cepat).
