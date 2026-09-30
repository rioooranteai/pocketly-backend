# Rancangan Chatbot: RAG + Analytics (Semantic Layer)

Status: **rencana, belum dieksekusi**
Tanggal diskusi: 2026-09-29

## Ringkasan keputusan

| Topik | Keputusan | Alasan singkat |
|---|---|---|
| Orkestrasi | **Manual di Go, tanpa LangGraph/framework** | Alurnya kecil dan deterministik; kode manual lebih mudah dijelaskan |
| Routing | **Deterministik: satu LLM kecil sebagai router → `switch` di Go** | Murah dan biayanya bisa diprediksi (2 panggilan LLM per pertanyaan) |
| Tool calling / agent loop | **Tidak dipakai** | Belum dibutuhkan untuk chatbot RAG + analytics sederhana |
| Pertanyaan data transaksi | **Semantic layer mini, bukan NL2SQL bebas** | Lebih akurat, lebih aman, dan gagalnya terlihat jelas |
| Semantic layer | **Tulis sendiri, di belakang interface `AnalyticsRepository`** | dbt/MetricFlow tidak cocok untuk OLTP per-user; Cube jadi opsi nanti |
| LLM provider | **OpenAI, model kecil** | Jev (TypeSafe) belum di-update dan belum bisa dipakai |
| API OpenAI | **Responses API untuk kode baru** | Rekomendasi OpenAI untuk integrasi baru; Chat Completions tetap dipakai kode lama |
| Structured output | **Strict JSON schema + retry dengan feedback error** | Padanan `ModelRetry` di Pydantic AI |
| Vector DB | **Pinecone** | Managed, ada Go SDK, tidak menunggu migrasi Postgres |
| Database transaksi | **SQLite sekarang, Postgres nanti** | Migrasi Postgres ada di rencana caching + load balancing, belum dieksekusi |
| Evaluasi | **Golden set ~200 soal, stratified, dev/test split** | Margin error ±5%, bisa dipertanggungjawabkan saat wawancara |

---

## 1. Alur chatbot

```
Pertanyaan user (+ user_id dari JWT)
   │
   ▼  Call 1: LLM kecil, structured output strict (+ retry helper)
{
  "route": "analytics" | "knowledge" | "clarify" | "chitchat" | "unsupported",
  "analytics": { metric, group_by, filters, time_range, order, limit } | null,
  "search_query": "..." | null,
  "clarify_question": "..." | null
}
   │
   ▼  switch di Go (deterministik)
analytics   → domain.Validate(spec) → AnalyticsRepository.Run(userID, spec)
knowledge   → Embedder.Embed(search_query) → VectorStore.Search(top-k)
clarify     → kembalikan clarify_question ke user
chitchat    → jawaban template / singkat
unsupported → pesan sopan + contoh pertanyaan yang bisa dijawab
   │
   ▼  Call 2: LLM menyusun jawaban natural dari hasil (angka / chunk)
Jawaban
```

Prinsip yang dipegang:

- **Angka selalu dari SQL yang deterministik.** LLM hanya memilih spec (call 1) dan merangkai kalimat (call 2), tidak pernah menghitung.
- **`user_id` selalu dari JWT**, tidak pernah dari output LLM. Prompt injection apa pun tidak bisa melintas ke data user lain.
- **Isi dokumen RAG diperlakukan sebagai data, bukan instruksi.**

### Pemetaan konsep LangGraph

Untuk yang terbiasa dengan LangGraph, padanannya di sini:

| LangGraph | Pocketly |
|---|---|
| `State` | struct `chatState` |
| Node | method di `ChatUsecase` |
| Conditional edge | `switch` pada `route` |
| Checkpointer / memory | `ConversationRepository` (fase lanjut) |

---

## 2. Semantic layer mini (pengganti NL2SQL bebas)

### Konsep

LLM **tidak menulis SQL**. LLM menerjemahkan pertanyaan ke **spec terstruktur**, lalu Go meng-compile spec itu menjadi SQL dari potongan yang sudah ditulis dan dites.

```
"Berapa pengeluaran makan per bulan 3 bulan terakhir?"
        ▼
{ "metric": "total_spending", "group_by": "month",
  "filters": { "categories": ["food"] },
  "time_range": { "preset": "last_3_months" } }
        ▼
SELECT <bucket bulan>, COALESCE(SUM(t.total_amount), 0)
FROM transactions t
WHERE t.user_id = ? AND t.category IN (?) AND t.date >= ? AND t.date < ?
GROUP BY 1 ORDER BY 1
```

- **Metric**: definisi bisnis yang ditulis sekali (`total_spending`, `transaction_count`, `average_transaction`, `max_transaction`, metric level item).
- **Dimension**: `category`, `merchant` (description), `day` / `week` / `month`.
- **Filter dan waktu**: nilai lewat placeholder `?`. Identifier hanya dari map allowlist.
- **Tanggal relatif** (`last_month`, `this_week`, dll.) dihitung di Go memakai timezone user, bukan oleh LLM.

### Kenapa bukan NL2SQL bebas

Pendekatan sebelumnya (LLM menulis SQL, lalu diamankan dengan larangan `INSERT`/`DELETE` dan sanitasi teks) adalah **denylist**, dan banyak serangan yang lolos:

| Lolos dari filter teks | Contoh |
|---|---|
| Baca data user lain | `SELECT * FROM transactions` tanpa `WHERE user_id`, `... OR 1=1` |
| Baca tabel sensitif | `SELECT email, password FROM users` |
| Writable CTE (Postgres) | `WITH x AS (DELETE ... RETURNING *) SELECT * FROM x` |
| DoS | `pg_sleep(60)`, cross join tanpa `LIMIT` |
| Fungsi berefek samping | `pg_read_file`, `dblink`; di SQLite `ATTACH DATABASE`, `PRAGMA` |
| Obfuscation | `DeLeTe`, `DEL/**/ETE`, multi-statement `;` |

Semantic layer adalah **allowlist by construction**: LLM tidak bisa menghasilkan SQL berbahaya karena memang tidak menulis SQL.

Selain aman, akurasinya juga lebih tinggi. LLM tidak perlu tahu jebakan skema seperti:

- **Double counting**: join `transactions` × `transaction_items` lalu `SUM(t.total_amount)` akan menghitung transaksi 5 item sebanyak 5 kali. SQL-nya valid dan angkanya salah tanpa error.
- Perlakuan `uncategorized`.
- Beda fungsi tanggal SQLite (`strftime`) dan Postgres (`date_trunc`).

Benchmark dbt 2026 (11 soal × 20 run, data yang sudah dimodelkan): text-to-SQL 84–90%, semantic layer 98–100%. Kesimpulan terpentingnya: **semantic layer gagal secara eksplisit, text-to-SQL gagal secara diam-diam.**

Kalau suatu saat NL2SQL bebas tetap dipakai (sebagai fallback), urutan kekuatan guardrail-nya:

1. Permission di DB: role read-only, hanya akses view, RLS (butuh Postgres), `statement_timeout`.
2. Parse SQL ke AST dengan allowlist node, tabel, dan fungsi.
3. Filter teks/regex, hanya sebagai lapisan tambahan.

### Kenapa tulis sendiri, bukan dbt

| | dbt / MetricFlow | Kebutuhan Pocketly |
|---|---|---|
| Target | Data warehouse, analis internal | DB aplikasi (OLTP), query per end-user |
| Runtime | Python | Go |
| SQLite | Adapter komunitas, tidak resmi | Masih SQLite |
| API runtime | Semantic Layer API butuh dbt Cloud Team/Enterprise (berbayar). MetricFlow sendiri sudah Apache 2.0 sejak Okt 2025, tapi harus dibungkus sendiri jadi service | Dipanggil per request chat |
| Skala | Ratusan model | 3 tabel, beberapa metric |

Kalau nanti butuh tool yang mature, yang cocok adalah **Cube Core** (Apache 2.0): self-host dengan Docker, REST API JSON, security context dari JWT untuk filter per user, dan caching bawaan. Karena semantic layer ada di belakang interface `AnalyticsRepository`, penggantiannya tidak menyentuh usecase.

### Cakupan pertanyaan

Semantic layer **tidak menjamin** semua pertanyaan terjawab (NL2SQL juga tidak). Targetnya adalah cakupan sebesar mungkin, gagal secara eksplisit, dan cakupan yang terukur serta terus bertambah.

| Jenis pertanyaan | Contoh | Status v1 |
|---|---|---|
| Total / jumlah / rata-rata | "Total jajan bulan ini?" | ✅ |
| Breakdown | "Pengeluaran per kategori minggu lalu" | ✅ |
| Tren | "Pengeluaran makan per bulan tahun ini" | ✅ |
| Top N | "5 merchant paling sering" | ✅ |
| Cari / daftar transaksi | "Transaksi Indomaret kemarin" | ✅ |
| Ekstrem | "Transaksi terbesarku bulan ini" | ✅ |
| Level item | "Berapa kali beli kopi?" | ⚠️ perlu metric grain item (terpisah, supaya tidak double count) |
| Perbandingan periode | "Bulan ini vs bulan lalu" | ⚠️ perlu field `compare_to` |
| Persentase | "Berapa persen untuk makan?" | ⚠️ turunan breakdown, dihitung di Go |
| Penilaian / saran | "Apakah saya boros?" | ⚠️ data via spec, lalu ditafsirkan LLM di call 2 |
| Data yang tidak ada | "Sisa saldoku?", "pemasukan bulan ini?" | ❌ Pocketly hanya mencatat pengeluaran |
| Prediksi | "Cukup nggak sampai akhir bulan?" | ❌ di luar scope v1 |

Cara menambah cakupan: log setiap pertanyaan yang jatuh ke `unsupported` atau gagal `Validate()` (dengan anonimisasi), review berkala, tambahkan field ke spec untuk pola yang sering muncul, lalu masukkan pertanyaannya ke golden set.

---

## 3. LLM provider

- **OpenAI, model kecil.** Router hanya perlu memilih enum dan mengisi spec kecil, jadi model kecil sudah cukup. Nama model dibuat configurable.
- **Jev (TypeSafe)** awalnya direncanakan untuk router, tapi belum ada update dan belum bisa dipakai. `TypeSafeCategorizer` tetap ada di `ai/categorizer/typesafe.go` dan belum di-wire.
- **Responses API** untuk kode chatbot baru. Categorizer dan vision tetap memakai Chat Completions (tidak deprecated), dan migrasinya opsional.
- **Satu client bersama** di `ai/openai/`, dibangun sekali dari config, lalu di-inject ke semua adapter. Sekarang tiap adapter membuat client sendiri dan key diambil dari `VisionConfig.APIKey`.

### Structured output dan retry

Kode sekarang memakai *prompted output* ("Respond with ONLY valid JSON") lalu `json.Unmarshal`. Ini rapuh. Penggantinya dua lapis:

1. **Structured Outputs strict** (`Strict: true` + JSON schema, dengan `enum` untuk field pilihan). Error format hampir hilang. Schema bisa digenerate dari struct Go, misalnya dengan `invopop/jsonschema`.
2. **Validasi bisnis + retry dengan feedback** (padanan `ModelRetry` di Pydantic AI, atau `RetryWithErrorOutputParser` di LangChain). Kalau `Validate()` gagal, jawaban yang salah dan pesan error-nya dikirim balik ke model. **Maksimal 1–2 retry**, karena setiap retry adalah panggilan berbayar.

```go
// ai/openai/structured.go (sketsa)
func GenerateStructured[T any](ctx, client, model, messages, validate func(T) error, maxRetries int) (T, error)
```

Error jaringan tidak di-retry di helper ini karena SDK sudah menangani retry-nya sendiri.

---

## 4. Vector DB: Pinecone

- **Pinecone** dipakai untuk retrieval RAG. Go SDK tersedia (`github.com/pinecone-io/go-pinecone`).
- Diakses lewat interface `VectorStore`, jadi bisa diganti ke **pgvector** setelah migrasi Postgres kalau ingin semuanya di satu database.
- **Namespace:**
  - Knowledge base bersama (FAQ, artikel): satu namespace, misalnya `knowledge`.
  - Kalau nanti meng-embed data milik user (misalnya pencarian semantik item transaksi), pakai **namespace per user** atau filter metadata `user_id` yang dipaksakan oleh kode. Nilainya tetap dari JWT, bukan dari LLM.
- **Embedding**: OpenAI embedding model via `port.Embedder`, atau integrated inference milik Pinecone. Keputusannya masih terbuka (lihat bagian 9).
- **Ingestion** dibuat sebagai command terpisah (`cmd/ingest`), bukan dijalankan saat server start. Polanya sama seperti `cmd/migrate`.

---

## 5. Rencana struktur kode

```
internal/
├── domain/
│   ├── analytics_query.go        ← spec (Metric, Dimension, TimeRange, ...) + Validate()
│   ├── analytics_row.go
│   └── knowledge_chunk.go
├── port/
│   ├── chat_model.go             ← Generate / GenerateStructured untuk router & jawaban
│   ├── embedder.go
│   └── vector_store.go           ← Upsert / Search (Pinecone sekarang, pgvector nanti)
├── repository/
│   └── analytics_repository.go   ← Run(ctx, userID, spec)
├── usecase/
│   └── chat_usecase.go           ← router → switch → call 2
├── infrastructure/
│   ├── ai/
│   │   ├── openai/               ← shared client + GenerateStructured
│   │   ├── categorizer/          (sudah ada)
│   │   ├── vision/               (sudah ada)
│   │   ├── chat/                 ← implementasi port.ChatModel (Responses API)
│   │   └── embedding/            ← implementasi port.Embedder
│   ├── vectorstore/pinecone/     ← implementasi port.VectorStore
│   └── persistence/gorm/
│       └── analytics_repository.go  ← compiler spec → SQL (SQLite dulu)
└── delivery/http/
    ├── dto/chat_dto.go
    └── handler/chat_handler.go   ← POST /api/v1/chat (AuthMiddleware + RateLimitPerUser)
cmd/ingest/                       ← ingest dokumen ke Pinecone
```

```go
type AnalyticsRepository interface {
    Run(ctx context.Context, userID string, q domain.AnalyticsQuery) ([]domain.AnalyticsRow, error)
}
```

`domain.AnalyticsQuery` tidak tahu apa-apa soal SQL. Implementasinya:

- **Sekarang:** `persistence/gorm/analytics_repository.go` (compiler SQLite).
- **Setelah migrasi Postgres:** hanya file ini yang berubah (`strftime` → `date_trunc`).
- **Opsional nanti:** `infrastructure/cube/analytics_repository.go`.

---

## 6. Hubungan dengan migrasi Postgres

Migrasi SQLite → Postgres adalah **tahap 1** di rencana caching + load balancing (Postgres → Redis cache → Nginx LB → rate limiter terdistribusi → health check → k6). Rencana itu **sudah didiskusikan tapi belum dieksekusi**.

Dampaknya ke chatbot:

| Bagian chatbot | Tergantung Postgres? |
|---|---|
| Router, structured output, retry helper | Tidak |
| Pinecone / RAG | Tidak |
| Compiler semantic layer | Ya. Fungsi tanggal harus ditulis ulang, tapi terisolasi di satu file |
| Fallback NL2SQL bebas dengan RLS | Ya. SQLite tidak punya role maupun RLS |
| pgvector sebagai pengganti Pinecone | Ya |
| Cube Core | Praktisnya ya |

Kedua urutan sama-sama bisa:

- **Postgres dulu**: compiler langsung ditulis untuk Postgres, jadi tidak ada kerja dua kali.
- **Chatbot dulu**: compiler ditulis untuk SQLite, lalu satu file itu diganti saat migrasi.

Keputusan urutannya masih terbuka.

---

## 7. Evaluasi: golden set

| Aspek | Keputusan |
|---|---|
| Jumlah | **~200 soal** (v1 minimal 100). Pada akurasi ~85%: 100 soal → ±7%, 200 soal → ±5% |
| Komposisi | Stratified per jenis pertanyaan di tabel cakupan, **15–20 soal per jenis** |
| Soal negatif | **~20%**: seharusnya `unsupported`, ambigu (seharusnya `clarify`), prompt injection |
| Bahasa | Slang, typo, campuran Indonesia-Inggris, tanggal relatif |
| Split | **150 dev / 50 held-out test.** Prompt di-tuning dengan dev, angka yang dilaporkan dari test |
| Pengulangan | Tiap soal **3 kali**, untuk mengukur konsistensi |
| Penilaian | **Exact match spec JSON / hasil angka, deterministik**, tanpa LLM-as-judge |
| Metrik | Akurasi router, akurasi spec, akurasi **per jenis**, tingkat unsupported |
| Kapan dijalankan | Setiap prompt atau model berubah |

Kalimat untuk wawancara:

> "Golden set 200 soal, stratified ke 10 jenis pertanyaan, termasuk 20% soal negatif dan adversarial. Dipisah 150 dev dan 50 held-out, dinilai deterministik lewat exact match spec, dan tiap soal dijalankan 3 kali. Akurasi di held-out X% ±5%, per kategori terburuk Y%, dan eval-nya jalan otomatis setiap prompt atau model berubah."

---

## 8. Urutan pengerjaan

- [ ] **0. Kontrak spec**: daftar final metric, dimension, filter, dan preset waktu.
- [ ] **1. Golden set v1** (~100 soal) diturunkan dari kontrak spec.
- [ ] **2. Fondasi AI**: shared OpenAI client, `OpenAIConfig{APIKey, ChatModel, EmbeddingModel}`, dan `GenerateStructured` + retry. Sebagai contoh pertama, categorizer dimigrasi ke strict schema dengan `enum`.
- [ ] **3. Analytics**: `domain.AnalyticsQuery` + `Validate()`, `AnalyticsRepository` + compiler, lalu unit test compiler (termasuk kasus double counting).
- [ ] **4. Router + `ChatUsecase`** (route analytics dulu) + endpoint `POST /api/v1/chat`.
- [ ] **5. RAG**: `cmd/ingest`, Pinecone `VectorStore`, `Embedder`, route knowledge.
- [ ] **6. Eval runner** golden set, lalu laporan per jenis.
- [ ] **7. Fase lanjut**: memory percakapan, streaming SSE, log unsupported, dan perluasan cakupan (item, `compare_to`).
- [ ] **(Opsional)** Fallback NL2SQL dengan guardrail (butuh Postgres), Cube, pgvector.

---

## 9. Pertanyaan terbuka

1. **Isi knowledge base RAG apa?** FAQ aplikasi, artikel tips keuangan, atau pencarian semantik item transaksi user? Jawabannya menentukan skema namespace Pinecone.
2. **Embedding**: OpenAI embedding model atau integrated inference Pinecone?
3. **Model router**: model kecil OpenAI yang mana? Apakah sama dengan categorizer?
4. **Urutan**: migrasi Postgres dulu atau chatbot dulu?
5. **Streaming** jawaban (SSE) di v1 atau fase lanjut?

---

## Referensi

- [Semantic Layer vs. Text-to-SQL: 2026 Benchmark (dbt)](https://docs.getdbt.com/blog/semantic-layer-vs-text-to-sql-2026)
- [Text-to-SQL in Production (TianPan)](https://tianpan.co/blog/2026/04/10/text-to-sql-failure-modes-production)
- [Text-to-SQL Security: 10 Risks (dpriver)](https://www.dpriver.com/blog/text-to-sql-security-10-risks-before-production-deployment/)
- [Announcing open source MetricFlow (dbt Labs)](https://www.getdbt.com/blog/open-source-metricflow-governed-metrics)
- [About MetricFlow (dbt Developer Hub)](https://docs.getdbt.com/docs/build/about-metricflow)
- [cube-js/cube (GitHub)](https://github.com/cube-js/cube)
- [Migrate to the Responses API (OpenAI)](https://developers.openai.com/api/docs/guides/migrate-to-responses)
- [Pydantic AI: Output](https://pydantic.dev/docs/ai/core-concepts/output/)
- [cloudwego/eino (GitHub)](https://github.com/cloudwego/eino)
