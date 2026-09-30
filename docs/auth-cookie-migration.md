# Rencana Migrasi Auth: localStorage → Cookie httpOnly

Status: **rencana, belum dieksekusi**

## Latar belakang

Token JWT saat ini disimpan di `localStorage` (`stores/auth.ts` di frontend), sementara CSP masih mengizinkan `script-src 'unsafe-inline'` (`next.config.ts:21`). Kalau ada satu celah XSS, script penyerang bisa membaca token dari `localStorage` lalu mengambil alih akun user.

Tujuan migrasi ini:

1. Token tidak bisa dibaca JavaScript sama sekali (cookie `httpOnly`).
2. Script sisipan penyerang tidak bisa jalan (CSP berbasis nonce, tanpa `'unsafe-inline'`).

## Kondisi sekarang

| Bagian | Kondisi | Lokasi |
|---|---|---|
| Middleware auth | Hanya membaca header `Authorization: Bearer` | `internal/delivery/http/middleware/auth_middleware.go` |
| Response login/register | Token dikirim di body JSON (`AuthResponse.Token`) | `internal/delivery/http/handler/auth_handler.go`, `internal/delivery/http/dto/auth_dto.go` |
| CORS | `Access-Control-Allow-Origin: *` (hanya aman karena belum pakai cookie) | `internal/delivery/http/middleware/cors_middleware.go` |
| Endpoint `/me` | Belum ada | — |
| Endpoint `/logout` | Belum ada | — |
| Masa berlaku JWT | 4 jam, tanpa refresh token | `internal/infrastructure/auth/jwt.go` |
| Penyimpanan token di FE | `localStorage` | `stores/auth.ts` (repo frontend) |
| CSP | `script-src 'unsafe-inline'` | `next.config.ts:21` (repo frontend) |

## Fase 1: Backend Go

### 1.1 Login/Register set cookie

- Setelah login atau register berhasil, set cookie:

  | Atribut | Nilai |
  |---|---|
  | Nama | `access_token` |
  | `HttpOnly` | `true` |
  | `Secure` | `true` (bisa dimatikan lewat env untuk `http://localhost`) |
  | `SameSite` | `Lax` |
  | `Path` | `/` |
  | `Max-Age` | Sama dengan TTL JWT (4 jam) |

- Hapus field `token` dari `AuthResponse`. Response cukup berisi `name` dan `email`.
- Tambahkan konfigurasi env baru, misalnya `COOKIE_SECURE=false` untuk development.

### 1.2 `AuthMiddleware` membaca dari cookie

- Utamakan cookie `access_token`.
- Header `Authorization: Bearer` tetap diterima sebagai fallback selama masa transisi, dan untuk testing lewat Postman.
- *(Keputusan tertunda: fallback dipertahankan permanen atau dihapus setelah FE selesai migrasi.)*

### 1.3 Endpoint baru

- `GET /api/v1/me` (terproteksi): mengembalikan data user yang sedang login (`name`, `email`).
  - Butuh method baru di usecase (misalnya `AuthUsecase.GetProfile`). Repository tidak perlu diubah karena `UserRepository.FindByID` sudah ada.
- `POST /api/v1/logout`: menghapus cookie `access_token` (`Max-Age=-1`).

### 1.4 CORS

- Ganti `*` dengan origin FE yang spesifik, diambil dari env, misalnya `CORS_ALLOWED_ORIGINS=http://localhost:3000`.
- Tambahkan `Access-Control-Allow-Credentials: true`. Header ini tidak boleh digabung dengan `Allow-Origin: *`.
- Perbarui komentar di `cors_middleware.go` yang menyebut auth memakai Bearer token.

### 1.5 Proteksi CSRF

- `SameSite=Lax` sudah memblokir sebagian besar serangan CSRF.
- Lapisan tambahan: middleware yang mengecek header `Origin` pada request `POST`/`PUT`/`DELETE`. Kalau origin-nya tidak ada di allowlist, tolak dengan `403`.

### Penempatan layer (menjaga clean architecture)

- Cookie (nama, atribut, `Set-Cookie`) adalah urusan HTTP, jadi hanya ada di `delivery/http` (handler dan middleware). Usecase tetap mengembalikan token sebagai `string` dan tidak tahu soal cookie.
- Nilai konfigurasi cookie dan CORS dibaca di `infrastructure/config`, lalu diteruskan lewat `cmd/api` (composition root). Handler dan middleware tidak membaca env secara langsung.
- `AuthMiddleware` tetap bergantung pada `port.TokenVerifier`, bukan pada `auth.JWTSigner`.
- Method baru untuk `/me` ditambahkan ke interface `AuthService` di `handler/services.go` (sisi konsumen).

### 1.6 Dokumentasi

- Perbarui `docs/api-contract.md`: response login/register tanpa `token`, auth lewat cookie, serta endpoint `/me` dan `/logout`.
- Perbarui `.env.example` dengan variabel baru (`COOKIE_SECURE`, `CORS_ALLOWED_ORIGINS`).

## Fase 2: Frontend Next.js

### 2.1 Proxy lewat Next.js (disarankan)

- Tambahkan `rewrites` di `next.config.ts` untuk meneruskan `/api/*` ke backend Go.
- Dengan begitu browser melihat FE dan API sebagai satu origin:
  - cookie terkirim otomatis,
  - `middleware.ts` Next.js bisa membaca cookie,
  - CORS hampir tidak terlibat.
- Kalau FE dan API tetap beda subdomain (misalnya `app.` dan `api.`), cookie harus di-set dengan `Domain=.namadomain.com`.

### 2.2 Store auth (`stores/auth.ts`)

- Berhenti menyimpan token. Hapus `persist` atau `localStorage` untuk token.
- Store hanya menyimpan data user dari `/me`, dan hanya di memory.

### 2.3 Fetch client

- Hapus header `Authorization`.
- Tambahkan `credentials: 'include'` (tidak wajib kalau sudah lewat proxy satu origin).
- Kalau dapat `401`, kosongkan store lalu redirect ke `/login`.

### 2.4 `middleware.ts` Next.js

- Untuk rute terproteksi (misalnya `/dashboard`): kalau cookie `access_token` tidak ada, redirect ke `/login` sebelum halaman dirender.
- Ini hanya untuk UX. Validasi token yang sebenarnya tetap dilakukan oleh `AuthMiddleware` di backend Go.

### 2.5 Logout

- Panggil `POST /api/logout`, kosongkan store, lalu redirect ke `/login`.

### 2.6 Bersihkan sisa lama

- Hapus token lama dari `localStorage` user yang sudah pernah login, misalnya dengan `localStorage.removeItem(...)` sekali saat app load.

## Fase 3: CSP berbasis nonce

- Hapus `'unsafe-inline'` dari `script-src` di `next.config.ts`.
- Pindahkan pembuatan header CSP ke `middleware.ts` Next.js dengan nonce acak per request. Next.js otomatis memasang nonce itu ke script bawaannya.
- Konsekuensi: halaman dirender dinamis karena nonce berubah setiap request, jadi static optimization berkurang. Untuk aplikasi keuangan yang butuh login, ini tidak masalah.

## Fase 4 (opsional, nanti): Refresh token

- Access token dibuat pendek (misalnya 15 menit).
- Refresh token disimpan di cookie `httpOnly` terpisah dan dicatat di DB supaya bisa dicabut (logout dari semua perangkat, token dicuri, dsb.).
- Butuh tabel baru, jadi dikerjakan terpisah dari migrasi ini.

## Alur setelah migrasi

```
User buka /dashboard
  │  GET /dashboard  (+ Cookie ditempel otomatis oleh browser)
  ▼
Next.js middleware.ts
  ├── cookie tidak ada → redirect /login (backend tidak dihubungi)
  └── cookie ada → kirim halaman
  ▼
Halaman memanggil GET /api/v1/me  (+ Cookie)
  ▼
AuthMiddleware Go: cek signature dan expiry JWT
  ├── tidak valid → 401 → FE redirect /login
  └── valid → 200 + data user → dashboard tampil
```

## Urutan eksekusi

1. **Fase 1**, dengan fallback header dipertahankan supaya FE lama tidak langsung rusak.
2. **Fase 2**, FE pindah ke cookie.
3. Hapus fallback header di backend (opsional, tergantung keputusan di 1.2).
4. **Fase 3**, CSP nonce.
5. **Fase 4**, kalau dibutuhkan.

## Keputusan yang belum diambil

- [ ] Lokasi repo/folder frontend.
- [ ] Fallback `Authorization: Bearer` dipertahankan permanen atau dihapus setelah migrasi.
- [ ] FE dan API memakai proxy satu origin (disarankan) atau tetap beda domain.
- [ ] Refresh token (Fase 4) dikerjakan sekarang atau nanti.

## Risk acceptance (sampai migrasi selesai)

Sampai Fase 1–3 selesai, risiko berikut **diketahui dan diterima sementara**: satu celah XSS di frontend cukup untuk mencuri token JWT dari `localStorage`, dan token itu berlaku sampai 4 jam. Mitigasi sementara: React meng-escape teks secara default, hindari `dangerouslySetInnerHTML`, dan jangan pakai URL `href` dari input user tanpa validasi.
