# Profil SCIM 2.0 (RFC 7643 / 7644) — User dan Group

Base URL: `{PUBLIC_BASE_URL}/scim/v2` · Autentikasi: `Authorization: Bearer smsc_scim_…` (token dibuat admin di **Pengaturan > SCIM**
atau `POST /api/v1/scim-tokens`). Aktif hanya bila `SCIM_ENABLED=true` (selain itu semua endpoint `/scim/v2/*` menjawab 404).
Respons: `application/scim+json`; permintaan boleh `application/scim+json` atau `application/json`. Galat berformat
`urn:ietf:params:scim:api:messages:2.0:Error` (`status` string, `scimType`, `detail`).

## Yang didukung

| Fitur | Dukungan |
|---|---|
| Discovery | `GET /ServiceProviderConfig`, `/ResourceTypes[/User]`, `/Schemas[/{id}]` |
| Resource | `User` (`/Users`) dan `Group` (`/Groups`, lihat bagian *Grup → peran*) |
| Operasi | `GET/POST /Users`, `GET/PUT/PATCH/DELETE /Users/{id}`; `GET/POST /Groups`, `GET/PUT/PATCH/DELETE /Groups/{id}` |
| Filter | `attr eq "nilai"` (dan `eq true|false`) yang digabung `and`. Users: `userName`, `emails.value`, `externalId`, `id`, `active`. Groups: `displayName`, `externalId`, `id`. Lainnya → 400 `invalidFilter` |
| Pagination | `startIndex` (mulai 1), `count` (default 50, maks. 100); `count=0` hanya mengembalikan `totalResults` |
| PATCH | `add` / `replace` / `remove` (tak peka huruf: `Replace` dari Azure AD diterima); dengan `path`, atau tanpa path dengan `value` berupa objek (gaya Okta); boolean string `"True"/"False"` diterima |
| Bulk, sort, ETag, change password | **tidak** didukung (dinyatakan di ServiceProviderConfig) |

## Pemetaan atribut

| SCIM | Gateway | Catatan |
|---|---|---|
| `id` | `users.id` (UUID) | |
| `userName` | email = **login** | wajib berupa alamat email; disimpan huruf kecil; unik (409 `uniqueness`) |
| `emails` | — | dikembalikan dari `userName`; perubahan diabaikan |
| `externalId` | `scim_external_id` | unik |
| `active` | `NOT disabled` | `false` menonaktifkan akun dan **mencabut semua sesinya**; PUT yang tidak menyebut `active` **tidak** mengubahnya |
| `roles[].value` | peran: `viewer` \| `operator` \| `admin` | beberapa nilai → yang tertinggi; kosong/dihapus → `SCIM_DEFAULT_ROLE`; `admin` ditolak kecuali `SCIM_ALLOW_ADMIN_ROLE=true` |
| `displayName`, `name.{givenName,familyName,formatted}` | disimpan dan dikembalikan | tidak dipakai aplikasi |
| atribut lain (`phoneNumbers`, `addresses`, `title`, enterprise extension, …) | diabaikan | PATCH-nya diterima agar sinkronisasi IdP tidak gagal; path yang benar-benar tak dikenal → 400 `invalidPath` |

## Grup → peran (fase 13)

Grup dipetakan ke peran dari environment — daftar nama dipisah koma, **tak peka huruf**: `SCIM_ADMIN_GROUPS` (butuh `SCIM_ALLOW_ADMIN_ROLE=true`,
jika tidak server gagal start), `SCIM_OPERATOR_GROUPS`, `SCIM_VIEWER_GROUPS`. Satu nama di dua peran → gagal start. Grup yang tak dipetakan tetap
disimpan tetapi tidak memengaruhi peran.

**Peran efektif** pengguna terkelola = **tertinggi** dari (atribut `roles` langsung, peran semua grupnya); tanpa keduanya → `SCIM_DEFAULT_ROLE`.
Contoh: `roles=operator` + grup viewer → operator; hapus atribut `roles` → jatuh ke peran dari grup. Pengguna lokal yang diadopsi
(`SCIM_ADOPT_EXISTING`) memakai perannya saat itu sebagai peran langsung, sehingga grup hanya dapat menaikkan.

| Atribut Group | Catatan |
|---|---|
| `displayName` | wajib, unik (tak peka huruf, 409 `uniqueness`), maks. 200 karakter. **Mengganti nama** mengubah pemetaan untuk semua anggota |
| `externalId` | unik |
| `members[]` | hanya `type: User` yang dikelola SCIM (`value` = `id` pengguna); anggota tak dikenal / pengguna lokal / grup bersarang → 400 `invalidValue`; maks. 10.000 |
| `meta`, `id` | read-only. Pada User, `groups[]` read-only |

PATCH grup: `add`/`replace`/`remove` pada `members` (array `value`), `remove` dengan `path: members[value eq "id"]` (Okta/Azure), `remove members` tanpa value
= kosongkan, tanpa `path` dengan objek `value` (`displayName`, `externalId`, `members`), `displayName`, `externalId`. `excludedAttributes=members` didukung pada GET.

Setiap perubahan grup (buat/ubah/hapus/anggota/nama) menghitung ulang peran semua anggota terdampak **dalam satu transaksi**; bila ada yang akan
menurunkan **admin aktif terakhir**, seluruh perubahan dibatalkan (409 `mutability`). Akun admin tidak disentuh perubahan grup bila `SCIM_ALLOW_ADMIN_ROLE=false`.
Perubahan peran akibat grup diaudit (`scim.user_update`, `via=group`). Saat server start (leader), pemetaan diterapkan ulang ke semua anggota grup
(rekonsiliasi), sehingga mengubah `SCIM_*_GROUPS` lalu restart berlaku tanpa menunggu IdP.

## Aturan keamanan

* Pengguna yang dibuat SCIM **tanpa password** (hanya bisa masuk lewat SSO OIDC). Saat login SSO pertama, email terverifikasi dari IdP yang sama
  menautkan akun otomatis (`audit: auth.oidc_link via=scim`).
* SCIM **hanya melihat dan mengubah pengguna yang dikelolanya** (`scim_managed`). Pengguna lokal: tak terlihat, `POST` dengan `userName`
  yang sama → 409, `GET/PUT/PATCH/DELETE` → 404 — kecuali `SCIM_ADOPT_EXISTING=true` (pengguna lokal terlihat dan diadopsi pada penulisan pertama).
* SCIM (termasuk lewat grup) tidak dapat memberi/mengubah/menghapus **admin** kecuali `SCIM_ALLOW_ADMIN_ROLE=true`, dan **tidak pernah** boleh menyisakan nol admin aktif
  (409 `mutability`).
* `DELETE` menghapus permanen (sesi/kredensial ikut hilang; audit tetap menyimpan email). Untuk deprovisioning yang dapat dibalik gunakan `active=false`.
* Semua perubahan diaudit dengan aktor `scim:<nama token>` (`scim.user_create|update|deactivate|delete`).
* Laju: 20 permintaan/detik per token (burst 40) → 429 `Retry-After`.

## Contoh

```http
POST /scim/v2/Users
Authorization: Bearer smsc_scim_…
Content-Type: application/scim+json

{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"budi@contoh.id","externalId":"00u1abc",
 "name":{"givenName":"Budi","familyName":"Santoso"},"roles":[{"value":"operator"}],"active":true}
```

```http
PATCH /scim/v2/Users/0192…
{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}
```
