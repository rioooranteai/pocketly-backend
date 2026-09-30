# Temuan Audit Kode: Clean Architecture dan Tindak Lanjut

Status: **tercatat, belum dikerjakan**
Tanggal audit: 2026-09-27

## Ringkasan

Secara clean architecture, kode **masih patuh**. Arah dependency selalu ke dalam, tidak ada layer dalam yang mengimport layer luar, dan `go build ./...` serta `go vet ./...` lolos tanpa error.

| Layer | Import internal | Status |
|---|---|---|
| `domain` | Hanya stdlib | ✅ Murni, tanpa GORM/Gin/JWT |
| `port`, `repository` | `domain` | ✅ Hanya berisi interface |
| `usecase` | `domain`, `port`, `repository` (+ `uuid`) | ✅ Tidak mengenal infrastructure atau delivery |
| `infrastructure/*` | `domain`, `port`, `repository` | ✅ Tidak mengimport `usecase` atau `delivery` |
| `delivery/http/handler` | `domain`, `dto`, `middleware` | ✅ Interface service ada di sisi konsumen (`services.go`) |
| `delivery/http/middleware` | `port` | ✅ Bergantung pada `TokenVerifier`, bukan JWT langsung |
| `cmd/api` | Semua | ✅ Composition root |

Temuan di bawah ini **bukan pelanggaran**, melainkan hal yang layak dirapikan. Urutannya dari yang paling penting.

---

## 1. `TotalAmount` dan `Price` memakai `float64` untuk uang

**Prioritas: tinggi** (masalah kebenaran data, bukan clean architecture)

### Lokasi

| File | Baris | Field |
|---|---|---|
| `internal/domain/transaction.go` | 17 | `TotalAmount float64` |
| `internal/domain/transaction_item.go` | 17, 45–46 | `Price float64`, `Subtotal()` |
| `internal/infrastructure/persistence/gorm/models.go` | 47, 72 | `TotalAmount`, `Price` |
| `internal/delivery/http/dto/transaction_dto.go` | 16, 28, 35 | `Price`, `TotalAmount` |
| `internal/infrastructure/ai/openai_vision_extractor.go` | 60 | `Price` hasil parsing AI |

### Masalah

- `float64` tidak bisa menyimpan semua angka desimal dengan tepat. Contoh klasik: `0.1 + 0.2 = 0.30000000000000004`.
- Kalau total dijumlahkan dari banyak item, error pembulatan kecil bisa menumpuk, sehingga `TotalAmount` tidak sama persis dengan jumlah yang terlihat di struk.
- Di SQLite, `float64` disimpan sebagai `REAL`, jadi masalah yang sama terbawa sampai database.
- Perbandingan `==` antar nominal menjadi tidak bisa diandalkan.

### Rekomendasi

Rupiah praktis tidak memakai pecahan, jadi opsi paling sederhana:

- **Pakai `int64` dalam satuan rupiah** untuk `Price`, `Subtotal()`, dan `TotalAmount`.
- Kalau nanti butuh mata uang lain yang memakai sen, simpan dalam **satuan terkecil** (misalnya `int64` sen) ditambah kode mata uang, atau pakai library decimal (misalnya `shopspring/decimal`).

### Dampak yang perlu diperhatikan saat mengerjakan

- [ ] **Domain:** ganti tipe, lalu ganti pengecekan `isFinite` dengan pengecekan overflow perkalian dan penjumlahan `int64`. `ErrTotalOutOfRange` tetap dipakai.
- [ ] **DTO:** `Price *int64` dengan `binding:"required,min=0"`. JSON tetap berupa angka, tapi nilai desimal seperti `15000.5` akan ditolak. Ini perubahan kontrak API, jadi `docs/api-contract.md` dan frontend perlu ikut diperbarui.
- [ ] **Vision extractor:** AI bisa mengembalikan harga desimal. Tentukan aturannya (dibulatkan atau ditolak) dan tempatkan di adapter.
- [ ] **Model GORM dan migrasi:** kolom `REAL` menjadi `INTEGER`. Data lama perlu dikonversi (dibulatkan) lewat `cmd/migrate`.
- [ ] **Test:** tambahkan test untuk penjumlahan banyak item dan kasus overflow.

---

## 2. Belum ada file test sama sekali

**Prioritas: tinggi**

- Arsitekturnya sudah siap dites: usecase hanya bergantung pada interface (`repository.*`, `port.*`), dan handler bergantung pada `AuthService`/`TransactionService`, jadi fake mudah dipasang.
- Manfaat terbesar clean architecture (testability) belum dipakai.

### Rekomendasi urutan

- [ ] `domain`: `CalculateTotal`, `ValidateItemData`, `IsValidEmail`, `IsValidCategory`.
- [ ] `usecase`: `AuthUsecase` (register duplikat, login salah password, timing email tidak ada) dan `TransactionUsecase` (ownership check, fallback kategori, validasi item) dengan fake repository dan fake port.
- [ ] `handler`: pemetaan error ke status HTTP dengan `httptest` dan fake service.
- [ ] `auth`: `JWTSigner` (token expired, algoritma salah, secret salah).

---

## 3. Daftar kategori ditulis dua kali di prompt OpenAI

**Prioritas: sedang**

- **Lokasi:** `internal/infrastructure/ai/openai_categorizer.go`, konstanta `categorizerPrompt`.
- **Masalah:** daftar kategori ditulis hardcode di prompt, dengan komentar "keep in sync with domain.Categories". Kalau domain menambah kategori, prompt bisa lupa diubah dan model tidak akan pernah memilih kategori baru itu.
- **Pembanding:** `typesafe_categorizer.go` sudah membangun kriteria dari `domain.Categories` lewat `buildTypeSafeCategoryCriteria()`.
- [ ] Bangun daftar kategori di prompt dari `domain.Categories` ditambah map deskripsi, seperti pola di `typesafe_categorizer.go`.

---

## 4. Nama config API key menyesatkan

**Prioritas: sedang**

- **Lokasi:** `internal/infrastructure/config/config.go:33`, `cmd/api/bootstrap.go:96`, `cmd/api/bootstrap.go:103`.
- **Masalah:**
  - `cfg.VisionConfig.APIKey` (env `VISION_API_KEY`) juga dipakai oleh `OpenAICategorizer`, padahal namanya "vision".
  - `cfg.AIAPIKey` (env `AI_API_KEY`) dibaca tapi tidak dipakai, karena TypeSafe categorizer belum dipasang.
- [ ] Ganti nama menjadi sesuatu seperti `OpenAIConfig.APIKey` / `OPENAI_API_KEY`, lalu hapus `AIAPIKey` kalau TypeSafe memang tidak akan dipakai. Perbarui `.env.example`.

---

## 5. Usecase memakai `log` stdlib secara langsung

**Prioritas: rendah**

- **Lokasi:** `internal/usecase/auth_usecase.go:108`, `internal/usecase/transaction_usecase.go:81`, `internal/usecase/transaction_usecase.go:85`.
- **Masalah:** dalam clean architecture yang ketat, logging adalah detail infrastruktur dan sebaiknya disuntikkan lewat port. Untuk skala sekarang ini masih wajar.
- [ ] Opsional: pakai `*slog.Logger` (stdlib, structured logging) yang disuntikkan lewat constructor, atau buat `port.Logger`. Cocok dikerjakan bersamaan dengan `pkg/logger` (lihat poin 7).

---

## 6. Handler mengimport package `middleware` hanya untuk `UserIDKey`

**Prioritas: rendah**

- **Lokasi:** `internal/delivery/http/handler/transaction_handler.go:12`, dipakai di setiap handler (`c.GetString(middleware.UserIDKey)`).
- **Masalah:** handler dan middleware ada di layer yang sama, jadi ini **bukan pelanggaran**. Tapi handler jadi bergantung pada package middleware hanya demi satu konstanta.
- [ ] Opsional: pindahkan ke helper kecil, misalnya `authctx.UserID(c)` di package terpisah, yang dipakai oleh middleware dan handler.

---

## 7. Sisa-sisa kecil

**Prioritas: rendah**

- [ ] **Folder kosong** `pkg/logger` dan `pkg/response`: isi atau hapus.
- [x] **Komentar JWT tidak sinkron:** `internal/infrastructure/auth/jwt.go:28` sekarang menulis "valid for 4 hours", sesuai `ttl` di baris 39 (`4 * time.Hour`).
- [ ] **Komentar beda bahasa:** `internal/delivery/http/handler/transaction_handler.go:57` dan `:166` (`// Sesuai signature Usecase ...`) berbahasa Indonesia, sementara komentar lain berbahasa Inggris. Komentar ini juga hanya mengulang signature, jadi bisa dihapus.

---

## Hal yang sudah baik (pertahankan)

- Model GORM terpisah dari entity domain, dengan mapper `toUserModel`/`toUserDomain` dan `toTransactionModel`/`toTransactionDomain`.
- Handler memakai DTO sendiri, jadi field seperti `Password` tidak pernah bocor ke response.
- Error domain dipetakan ke status HTTP di handler, bukan di usecase.
- `var _ repository.UserRepository = (*GormUserRepository)(nil)` memastikan kontrak repository terpenuhi saat compile.
- Interface service didefinisikan di sisi konsumen (`handler/services.go`).
- `Bootstrap` menjadi satu-satunya tempat yang mengenal semua implementasi konkret.

## Terkait

- `docs/auth-cookie-migration.md`: rencana migrasi token ke cookie httpOnly dan CSP nonce.
