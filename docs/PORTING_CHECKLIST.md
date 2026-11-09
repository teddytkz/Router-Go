# Checklist Porting XRouter-Next → Router-Go

Legenda: `[x]` sudah di-port & teruji · `[~]` sebagian · `[ ]` belum · `—` tidak direncanakan.
Kolom "Fase" mengacu ke [`PRD.md`](./PRD.md) §4. **Terakhir diperbarui: 2026-11-09 (fase 1 + 2a + 2c selesai).**

## A. Fondasi
- [x] Data dir `~/.router-go` / `%USERPROFILE%\.router-go` (+ override env) — F1
- [x] Buka SQLite WAL + bootstrap skema identik XRouter-Next (12 tabel) — F1
- [x] Repo: settings (JSON row, default merge) — F1
- [x] Repo: providerNodes — F1
- [x] Repo: providerConnections (priority, reorder, dedupe nama apikey, health fields) — F1
- [x] Repo: apiKeys (+ access columns) — F1
- [x] Repo: usageHistory — F1
- [x] Repo: kv (customModels, modelAliases) — F1/F2a (CRUD alias & custom model)
- [x] Repo: combos (+ strategi di `settings.comboStrategies`) — F2a
- [x] Repo: proxyPools / proxyPoolFitness — F2
- [ ] Repo: pricing — F2
- [ ] Repo: requestDetails / usageDaily — F4
- [ ] Migrasi versioned + backup pra-migrasi — F2
- [ ] Impor legacy JSON (`db.json`, `usage.json`) — —

## B. Auth & keamanan
- [x] Login password (bcrypt, `settings.password`, `INITIAL_PASSWORD`) — F1
- [x] JWT cookie `auth_token` (HS256, 24 jam, secret file/env) — F1
- [x] Login limiter per IP — F1
- [x] Penanganan password default dari remote (mustChangePassword) — F1
- [x] `requireLogin` toggle + guard `/api/*` — F1
- [x] Ganti password (`PATCH /api/settings`) — F1
- [x] Reset password (CLI flag) — F1
- [x] Generate/validasi API key `sk-{machine}-{id}-{crc}` — F1
- [x] `requireApiKey` + per-key access (restricted/allow, fail-closed) — F1/F2a
- [x] Trusted-peer / strip `X-Forwarded-For` — F1
- [~] SSRF guard (`assertPublicUrl`) — F1 (validasi skema), F2 (mode remote) _(sebagian: validasi skema http(s) sudah; mode remote publik-only belum (private host sengaja diizinkan))_
- [ ] OIDC SSO — F6
- [ ] SAML SSO — F6
- [x] Machine ID (`node-machine-id`) — F1 (hash hostname/machine-id)

## C. Gateway API (`/v1`)
- [x] `POST /v1/chat/completions` (OpenAI-compatible node, stream + non-stream) — F1
- [x] `POST /v1/responses` (node `responses`) — F1
- [x] `POST /v1/embeddings` (passthrough) — F1
- [x] `GET /v1/models` — F1
- [x] `GET /v1/models/info?id=`, `GET /v1/models/{prefix/model}` (id, owned_by, capabilities vision/reasoning/audioInput, contextWindow dari `modelcaps`; hormati access list key; slug jenis `image|tts|stt|embedding|image-to-text|web` → daftar kosong) — F2b
- [x] `POST /v1/messages/count_tokens` (**estimasi lokal** ~4 karakter/token, gambar tetap 1000; bukan tokenizer asli) — F2b
- [x] `POST /v1/responses/compact` (node `responses` dipanggil di `/responses/compact`; node chat/Anthropic menjawab seperti `/v1/responses`, sama dengan XRouter — di XRouter flag `_compact` hanya dipakai executor Codex) — F2b
- [x] `POST /v1/messages` (Anthropic) — F2a
- [x] `/v1beta/models/{model}:generateContent`, `:streamGenerateContent` (SSE; model dari URL, stream dari aksi), `:countTokens` (estimasi), `GET /v1beta/models[/{model}]`; key lewat Bearer, `x-goog-api-key` atau `?key=`; error `{error:{code,message,status}}` — F2b _(belum: `?alt=sse` dianggap selalu aktif, `fileData`/`cachedContent`, forward native TTS ke Google, `system_instruction` snake_case)_
- [x] `POST /v1/api/chat` (Ollama chat: `stream` default true, jawaban NDJSON; `options`/`format`/`images` Ollama belum dipetakan) — F2b
- [ ] `/v1/images`, `/v1/audio/*` — F5
- [-] `/v1/search`, `/v1/web/fetch` — sengaja dihapus (tidak dipakai)
- [x] Parse `prefix/model`, resolusi node — F1
- [x] Alias model (baca) — F1
- [x] Account fallback + cooldown + exponential backoff — F1
- [~] Error rules (`ERROR_RULES`, permanent-account quarantine) — F1 (inti) / F4 (lengkap) _(sebagian: aturan generik, aturan CodeBuddy, quarantine akun permanen + henti setelah 5 key permanen berturut-turut sudah; aturan per-provider lain belum)_
- [~] Account semaphore (batas paralel per key, antrian 20, timeout 30 dtk) — F2c _(round-robin/sticky antar koneksi belum)_
- [x] Combo (fallback, round-robin + sticky limit) — F2a  _(kind/smart/fusion belum)_
- [~] Translator chat↔responses↔messages (request, response, SSE) — F2a
- [x] Translator Gemini sisi klien (F2b): request gemini→chat (systemInstruction, roles user/model, teks, `inlineData` gambar/audio, functionCall/functionResponse, tools + `toolConfig`, `generationConfig` → max_tokens/temperature/top_p/stop; tipe skema `OBJECT`→`object`), response chat/messages/responses→Gemini (non-stream + SSE: teks, thought, functionCall utuh di chunk penutup, `finishReason`, `usageMetadata`). _Bukan format upstream: tidak ada node Gemini (gemini→upstream dan parser respons Gemini belum ada)._
- [ ] `json_schema → json_object` fallback untuk compat node — F2
- [x] `stream_options` strip pada non-stream — F1
- [x] Kapabilitas model (vision, reasoning, audio, context window): deteksi dari nama + override manual — F2c
- [x] Routing sadar kapabilitas: urut anggota combo (vision/audio wajib, reasoning pilihan, context muat) — F2c
- [~] Strip unsupported params — F3 _(sebagian: groq/mistral/cerebras/nvidia/openai berdasar host; aturan per-provider lain belum)_
- [x] Reasoning content injector (deepseek: semua giliran assistant, kimi: giliran tool-call) — F3
- [x] Token saver: Ponytail (lite/full/ultra) + Watermarks Remover + custom system prompt — F2c
- [ ] Token saver: RTK / caveman / headroom / pxpipe — — _(sengaja tidak di-port; hanya ponytail & watermarks remover yang diminta)_
- [~] Capacity adapter (pool model per kapabilitas vision/audio, round-robin; pdf/video belum) — F2c
- [x] Loop guard (tool-call & text loop, chat/messages/responses) — F2c
- [x] Circuit breaker per provider (5 gagal/60 dtk → open, probe 30 dtk, eskalasi) — F2c

## D. Provider
- [x] Node `openai-compatible` (chat/responses) — F1
- [x] Validasi node (`/models` lalu chat probe) — F1
- [x] Tes koneksi per connection — F1
- [x] Fetch model upstream per connection — F1
- [x] Node `anthropic-compatible` — F2a
- [ ] Node `custom-embedding` — F5
- [x] Kerangka provider bawaan (`internal/provider/<nama>`, registry statis, key pool, translasi, combo, usage) — F2c
- [x] Cline (API key; stream paksa + amplop `{success,data}`) — F2c
- [~] CodeBuddy (API key; jalur biasa, gateway-nya bisa menolak klien non-IDE) — F2c
- [~] OpenCode Free (tanpa key; jalur biasa tanpa identitas/TLS klien resmi → mungkin 403) — F2c
- [~] Freebuff (token; tanpa sesi/run & header klien resmi → kemungkinan ditolak) — F2c
- [~] Kiro (hook `Native`: protokol CodeWhisperer + AWS event stream → chat; auth Builder ID device-code, impor refresh token social/IDC/external_idp, refresh per metode, profileArn; chat/tools/thinking/agentic) — F4 _(eksperimental, hanya diuji dengan mock; tanpa penanda identitas IDE, session replay, repair retry, model live, login browser Google/GitHub, API key Kiro)_
- [ ] Qoder (protokol agent SSE bertanda tangan belum di-port; terdaftar tapi tidak bisa dirute) — F3
- [ ] Provider apikey bawaan lain (OpenAI, DeepSeek, Groq, …) via registry — F3
- [x] Login OAuth browser per provider: Cline (kode via callback + refresh), CodeBuddy & Freebuff (polling) — F2c _(belum diuji ke layanan asli)_
- [ ] Login OAuth Qoder — F3 (protokol belum di-port)
- [x] Quota Tracker lokal (batas request/token per provider atau key; jendela jam/hari/minggu/bulan, kalender atau bergulir; opsi blokir saat habis) — F2c
- [~] Quota dari API provider (limit akun OAuth/plan): `GET /api/usage/{connectionId}` (bentuk sama dengan XRouter: `plan`, `message`, `quotas`), cache 30 dtk per koneksi (`?force=1` melewati), lewat proxy pool koneksi; hook `Provider.Usage` untuk Kiro, Freebuff (GET saja), CodeBuddy intl; flag `usage` di `/api/builtin-providers`; provider lain menjawab `Usage API not implemented for <id>` — F4 _(hanya diuji dengan mock; belum: Qoder, GitHub, Codex, Claude, Gemini, Antigravity, GLM, MiniMax, Kimi, dll., retry setelah force-refresh token, header penyamaran IDE Kiro/CLI Freebuff)_
- [~] Quota Tracker UI sama dengan XRouter (`/dashboard/quota`) — F4: kartu per koneksi dengan tabel quota (bar sisa %, used/total, hitung mundur reset; hijau >70, kuning 30-70, merah <30), filter provider (sinkron `?provider=`), filter akun aktif/dimatikan, urut "Expiring first" dan "% quota" (saat satu provider dipilih), "Turn off Empty"/"Turn on Available", auto-refresh 60 dtk + hitung mundur + "Updated", refresh per koneksi dan semua, paginasi (10/20/50/100/kustom), cache quota di localStorage, sembunyikan baris quota (disimpan per browser), toggle/edit/hapus koneksi, daftar "Available free tiers" (per provider, yang terhubung dulu, cari, badge cadence; `GET /api/free-tiers`), dan tracker lokal lama dipindah ke bagian "Custom limits" di bawahnya. Tidak dibawa: reset-credit Claude/Codex, jadwal auto-ping, kekhususan Antigravity/Gemini, logo provider (diganti inisial). _(belum dilihat di browser)_
- [x] Katalog free tier (`GET /api/free-tiers`: provider gratis + model-nya, kadens `freeRefresh`, urut daily → weekly → monthly → rolling → one-time → tak diketahui) — F4
- [x] Custom headers per provider bawaan (default dari XRouter untuk Cline & CodeBuddy; override, reset ke default) — F2c
- [x] Round robin + sticky untuk key pool per provider — F2c
- [x] Model provider bawaan harus di-enable dulu (Enable/Disable All, katalog otomatis aktif saat key pertama) — F2c
- [x] Proxy pool + deploy relay Vercel/Cloudflare — F2b
- [ ] Executor khusus (azure, bedrock, vertex, ollama-local, …) — F3
- [ ] Provider OAuth + token refresh (claude, codex, gemini, kiro, cursor, github, …) — F4
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
- [~] `usage/summary` (periode, seri, breakdown, live 5m, recent, aktivitas provider 24j) + `usage/active` (request berjalan) + `usage/reset` — F2c _(chart/stream/recalculate/pricing belum)_
- [ ] `usage/request-details` — F4
- [x] `health`, `ready`, `version` — F1
- [x] `health/routing` (kartu jumlah provider per state circuit + avg response), `models/test`, `providers/test-batch` (log hasil per key), `system` (CPU/RAM) — F2c
- [x] Daftar provider disederhanakan ala XRouter: 4 section (Custom/OAuth/Free/API Key), judul kiri + "Test All" kanan, grid 4 kolom kartu ringkas (ikon + nama + 1 baris status); `test-batch` menerima `providers: [id...]` — F2d
- [x] Proxy Pools (F3): tabel `proxyPools`, tipe http/vercel/cloudflare, `strictProxy`, `noProxy`, halaman Proxy Pools, pilih pool per key (dialog Edit key, `providerSpecificData.proxyPoolId`), deploy relay Vercel (Edge function) dan Cloudflare (Worker) lewat token (tidak disimpan); relay dilindungi key rahasia per deploy (beda dari XRouter yang `/log`-nya terbuka). Belum: fitness/quarantine pool, Deno/Netlify.
- [ ] `init`, `shutdown`, `autostart` — F6
- [~] `models/{alias,custom}` — F2a _(disabled/availability/test/catalog-sync belum)_
- [~] `combos` CRUD — F2a _(presets/suggest belum)_
- [ ] `proxy-pools` — F2
- [ ] `pricing` — F2
- [ ] `translator` (halaman playground translator) — F2b
- [ ] `oauth/*` — F4
- [ ] `media-providers` — F5
- [ ] `tunnel`, `cli-tools`, `mitm`, `headroom`, `pxpipe`, `plugins`, `skills`, `mcp`, `tags`, `locale` — F6

## F. Dashboard UI
- [x] Shell: sidebar, topbar, tema dark/light, toast, responsif — F1
- [x] Login — F1
- [x] Token Saver page (Ponytail, Watermarks Remover, Guards & Shields, custom prompt) — F2c
- [x] Usage page seperti XRouter (periode, tab Overview/Request Logs, kartu, grafik, tabel grouped) — F2c _(tab Detailed Analysis & biaya belum)_
- [x] Dashboard: kartu CPU & RAM khusus proses Router-Go (sparkline 2 menit) — F2c
- [x] Dashboard (kartu request/input/output/cache, grafik request, token, model) — F1/F2a
- [x] Model provider harus di-add dulu sebelum bisa dipakai (daftar upstream hanya katalog) — F2c
- [x] Providers seperti XRouter: kartu + filter + routing health, halaman detail (koneksi, prioritas, test, model, upstream models) — F1/F2c
- [x] API Keys (base URL, API keys + access list, contoh pakai) — F1/F2a
- [—] Playground / basic-chat — dihapus sengaja (permintaan pemilik)
- [x] Usage (riwayat) — F1
- [x] Settings / Profile — F1
- [x] Combos — F2a
- [x] Models (alias & custom models) — F2a
- [ ] Proxy pools / proxy fitness — F2
- [ ] Pricing — F2
- [ ] Translator — F2
- [x] Quota (tracker lokal) — F2c
- [x] Console log (SSE live, filter level/teks, follow, clear; log request/upstream/combo/breaker/guard/test) — F2c
- [ ] Media providers — F5
- [ ] CLI tools, MITM, Token saver, PXPipe, Skills, next-feature — F6
- [ ] i18n (`i18n/`, `public/i18n`) — F6
- [ ] Landing, setup wizard, `callback` OAuth — F4

## G. Operasional & distribusi
- [x] Single binary dengan SPA embed + precompress — F1
- [x] Makefile (`build`, `dev`, `test`) — F1
- [x] Live reload Go dengan Air (`.air.toml`, `make dev`) — F2c
- [x] Graceful shutdown — F1
- [x] Port dapat diatur: `--port`, env, Settings (pindah langsung), default 20128 — F2c
- [ ] Dockerfile / docker-compose — F2
- [ ] CLI launcher (`start/stop/status`, daemon) — F6
- [ ] Tray, autostart, updater — F6
- [ ] Tunnel (Cloudflare/Tailscale) — F6
- [ ] Cloud sync — —

## H. Pengujian
- [~] Unit: auth (bcrypt/JWT/limiter/API key) — F1 _(sebagian: login/lockout/masking teruji lewat integrasi; belum ada unit test terpisah)_
- [x] Unit: resolve model + fallback/cooldown — F1
- [x] Integrasi: mock upstream stream/non-stream/429 → fallback — F1
- [x] Unit/integrasi: translator 3 format (request/response/SSE) + gateway translasi, combo, access list, CRUD dashboard — F2a
- [~] Kompatibilitas skema DB dengan XRouter-Next — F1 _(sebagian: skema identik & `data` JSON round-trip; belum diuji membuka DB dengan XRouter-Next sungguhan)_
- [x] Frontend: `tsc --noEmit`, build — F1
- [ ] E2E Playwright dashboard — F2

---
**Ringkasan:** 79 selesai · 13 sebagian · 49 belum (fase 2b–6).

## Layanan pendukung (src/sse/services)
- [x] Tes dijatah: tes key/model yang sama dalam 10 dtk dijawab dari memori; "Test all" melewati provider eksperimental (CodeBuddy, Freebuff, OpenCode, ...) dan hanya bisa dites satu per satu — F2d
- [x] Refresh token OAuth di latar belakang (tiap 5 menit, yang kedaluwarsa < 30 menit) — F4
- [x] Empty-response failover di combo (200 tanpa teks/tool-call → anggota berikutnya; content_filter/length tidak diulang; stream ditahan sampai ada isi, maks 128 KB / 20 dtk) — F4
- [x] Endpoint media passthrough: `/v1/images/generations`, `/v1/audio/speech`, `/v1/audio/transcriptions` (multipart), `/v1/videos/{generations,edits,extensions}` + polling `GET /v1/videos/{id}[/content]?model=` lewat node OpenAI-compatible — F5 _(adapter khusus provider: ElevenLabs, fal, Stability, dll. tidak di-port)_
- [x] CodeBuddy: pesan sistem di awal + konten user bertipe (`Prepare` di registry). Tidak ada penyamaran sebagai klien resmi (rewrite identitas / header kanal) — sengaja.
