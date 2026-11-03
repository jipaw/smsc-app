# Daftar password umum

`common.txt` diturunkan dari dua daftar di [SecLists](https://github.com/danielmiessler/SecLists) (lisensi MIT):
`Passwords/Common-Credentials/10k-most-common.txt` dan `100k-most-used-passwords-NCSC.txt` (daftar NCSC Inggris).
Disaring: huruf kecil, hanya ASCII cetak, panjang 8–64 (password lebih pendek dari 12 karakter sudah ditolak oleh aturan panjang;
entri 8–11 karakter dipakai sebagai *kata dasar* untuk mendeteksi variasi seperti `Sunshine2024!`), diurutkan, tanpa duplikat.
Diambil 2026-11-03. Perbarui dengan mengganti berkas ini; tes memastikan kata-kata kunci tetap terdeteksi.
