/* E2E dashboard: server (biner nyata, dashboard ter-embed) + HP palsu + Chromium.
 *   SMSC_TEST_DATABASE_URL='postgres://nobody@localhost:55432/postgres?sslmode=disable' node web/e2e/run.cjs [biner]
 * Memeriksa: login, enrollment via QR/kode, device online + SIM, kirim SMS, transaksi live sampai 'Sampai', timeline,
 * kill switch, RBAC viewer, aksesibilitas (axe), responsif mobile, tema gelap. Screenshot -> web/e2e/out/. */
const { chromium } = require('playwright')
const { spawn, execFileSync } = require('child_process')
const fs = require('fs'), path = require('path'), crypto = require('crypto')

const ROOT = path.resolve(__dirname, '../..')
const BIN = process.argv[2] || '/tmp/smscd'
const BASE = process.env.SMSC_TEST_DATABASE_URL
if (!BASE) { console.error('SMSC_TEST_DATABASE_URL belum diset'); process.exit(2) }
const DB = 'smsc_e2e_' + crypto.randomBytes(4).toString('hex')
const PORT = 19000 + Math.floor(Math.random() * 900)
const URL_ = `http://127.0.0.1:${PORT}`
const OUT = path.join(__dirname, 'out')
const AXE = fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8')
const ADMIN = 'admin@e2e.test', PW = 'e2e-admin-password-1'
const results = []
const procs = []

const psql = (sql) => execFileSync('psql', [BASE, '-qc', sql])
function check(cond, name, extra = '') { results.push([!!cond, name]); console.log(`${cond ? 'ok  ' : 'FAIL'} ${name}${cond ? '' : ' ' + extra}`) }

async function until(fn, ms, what) {
  const end = Date.now() + ms
  while (Date.now() < end) { try { if (await fn()) return true } catch {} await new Promise((r) => setTimeout(r, 200)) }
  throw new Error('timeout: ' + what)
}

async function api(method, p, body, cookie, csrf) {
  const h = { 'Content-Type': 'application/json' }
  if (cookie) h.Cookie = cookie
  if (csrf) h['X-CSRF-Token'] = csrf
  const r = await fetch(URL_ + p, { method, headers: h, body: body ? JSON.stringify(body) : undefined })
  return { status: r.status, json: await r.json().catch(() => null), cookie: r.headers.get('set-cookie') }
}

async function axeCheck(page, name) {
  await page.evaluate(AXE)
  const res = await page.evaluate(async () => (await axe.run(document, { runOnly: ['wcag2a', 'wcag2aa'] })).violations.map((v) => ({ id: v.id, impact: v.impact, n: v.nodes.length, help: v.help,
    nodes: v.nodes.slice(0, 4).map((n) => ({ t: n.target.join(' '), d: (n.any[0] && n.any[0].data && n.any[0].data.contrastRatio) ? `${n.any[0].data.fgColor} on ${n.any[0].data.bgColor} = ${n.any[0].data.contrastRatio}` : '' })) })))
  const bad = res.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  check(bad.length === 0, `aksesibilitas (axe, serius/kritis) - ${name}`, JSON.stringify(bad))
  const minor = res.filter((v) => !bad.includes(v))
  if (minor.length) console.log(`     catatan axe (${name}): ` + minor.map((v) => `${v.id}(${v.impact})`).join(', '))
}

async function main() {
  psql(`CREATE DATABASE ${DB}`)
  const env = { ...process.env, DATABASE_URL: BASE.replace('/postgres?', `/${DB}?`), PUBLIC_BASE_URL: URL_, SESSION_SECRET: 's'.repeat(40), HTTP_ADDR: `127.0.0.1:${PORT}`,
    AUTO_MIGRATE: 'true', SECURE_COOKIES: 'false', REQUIRE_VPN: 'false', DISPATCH_TICK: '300ms', REAPER_INTERVAL: '500ms' }
  execFileSync(BIN, ['migrate'], { env })
  execFileSync(BIN, ['admin', 'create', '--email', ADMIN, '--password', PW], { env })
  const srv = spawn(BIN, ['serve'], { env, stdio: 'ignore' }); procs.push(srv)
  await until(async () => (await fetch(URL_ + '/readyz')).ok, 15000, 'server siap')

  // setup lewat API: jendela kirim sepanjang hari + delay pendek, supaya tes tidak bergantung jam
  const login = await api('POST', '/api/v1/auth/login', { email: ADMIN, password: PW })
  const cookie = login.cookie.split(';')[0], csrf = login.json.csrf_token
  const pols = await api('GET', '/api/v1/dispatch-policies', null, cookie)
  const def = pols.json.items.find((p) => p.is_default)
  const put = await api('PUT', `/api/v1/dispatch-policies/${def.id}`, { ...def, min_delay_ms: 500, max_delay_ms: 500,
    send_window: { timezone: 'UTC', windows: [{ days: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '00:00' }] } }, cookie, csrf)
  check(put.status === 200, 'setup policy default via API', JSON.stringify(put.json))
  await api('POST', '/api/v1/users', { email: 'viewer@e2e.test', role: 'viewer', password: 'viewer-password-12' }, cookie, csrf)

  const browser = await chromium.launch()
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const page = await ctx.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  const shot = (n) => page.screenshot({ path: path.join(OUT, n + '.png'), fullPage: true })

  // ---- login ----
  await page.goto(URL_ + '/')
  await page.getByRole('heading', { name: 'SMSC Gateway' }).waitFor()
  await axeCheck(page, 'login')
  await shot('01-login')
  await page.getByLabel('Email').fill(ADMIN)
  await page.getByLabel('Password').fill('salah-salah-123')
  await page.getByRole('button', { name: 'Masuk' }).click()
  await page.getByRole('alert').waitFor()
  check((await page.getByRole('alert').textContent()).includes('Kredensial salah'), 'login salah menampilkan pesan seragam')
  await page.getByLabel('Password').fill(PW)
  await page.getByRole('button', { name: 'Masuk' }).click()
  await page.getByRole('heading', { name: 'Overview' }).waitFor()
  check(true, 'login berhasil, Overview tampil')

  // ---- tambah device ----
  await page.getByRole('link', { name: 'Devices', exact: true }).click()
  await page.getByText('Belum ada device').waitFor()
  await page.getByRole('button', { name: '+ Tambah device' }).click()
  await page.getByLabel('Nama device (opsional)').fill('HP Gateway E2E')
  await page.getByRole('button', { name: 'Buat QR' }).click()
  const qrImg = page.getByRole('img', { name: /QR enrollment/ })
  await qrImg.waitFor()
  const code = (await page.locator('dialog strong.mono').textContent()).trim()
  check(/^[A-Z2-9]{4}-[A-Z2-9]{4}$/.test(code), `kode manual tampil (${code})`)
  check((await qrImg.getAttribute('src')).startsWith('data:image/png'), 'QR dirender sebagai gambar')
  check((await page.getByText('Kedaluwarsa').count()) === 0 && (await page.getByText(/Berlaku 1[45]:/).count()) === 1, 'QR baru menampilkan sisa waktu ±15 menit (bukan kedaluwarsa)')
  await shot('02-add-device')
  const dev = spawn('python3', [path.join(ROOT, 'server/tests/fake_device.py'), '--server', URL_, '--token', code, '--heartbeat', '2', '--latency', '0.2'], { stdio: 'ignore' }); procs.push(dev)
  await page.getByText(/Device “HP Gateway E2E” terhubung/).waitFor({ timeout: 15000 })
  check(true, 'dialog berubah "terhubung" otomatis lewat SSE saat HP palsu masuk')
  await page.keyboard.press('Escape')
  await page.getByRole('cell', { name: /HP Gateway E2E/ }).waitFor()
  await until(async () => (await page.getByText('Online').count()) > 0, 10000, 'badge Online')
  check((await page.getByText('Telkomsel').count()) > 0 && (await page.getByText('XL').count()) > 0, 'dua SIM terdeteksi (Telkomsel, XL)')
  await axeCheck(page, 'devices')
  await shot('03-devices')

  // ---- kirim SMS ----
  await page.getByRole('link', { name: 'Kirim SMS', exact: true }).click()
  await page.getByLabel('Penerima', { exact: false }).first().fill('08123456789\n08129998877\nbukan-nomor')
  await page.getByLabel('Isi pesan').fill('Halo dari E2E, tagihan Anda jatuh tempo besok.')
  check((await page.getByText(/GSM-7 · 46 septet · 1 segmen/).count()) === 1, 'penghitung segmen GSM-7 benar')
  await page.getByLabel('Isi pesan').fill('Halo 😀 emoji')
  check((await page.getByText(/Unicode \(UCS-2\)/).count()) === 1, 'emoji memaksa UCS-2')
  await page.getByLabel('Isi pesan').fill('Halo dari E2E, tagihan Anda jatuh tempo besok.')
  await axeCheck(page, 'kirim sms')
  await shot('04-send')
  await page.getByRole('button', { name: /Kirim ke 3 nomor/ }).click()
  await page.getByText(/2 pesan masuk antrean/).waitFor()
  check((await page.getByText('ditolak', { exact: false }).count()) > 0 && (await page.getByText('nomor tidak valid').count()) === 1, 'nomor tidak valid ditolak dengan alasan per baris')

  // ---- transaksi ----
  await page.getByRole('link', { name: 'Transaksi', exact: true }).click()
  await until(async () => (await page.getByRole('row').filter({ hasText: '+628123456789' }).getByText('Sampai').count()) === 1, 25000, 'pesan 1 berstatus Sampai')
  await until(async () => (await page.getByRole('row').filter({ hasText: '+628129998877' }).getByText('Sampai').count()) === 1, 25000, 'pesan 2 berstatus Sampai')
  check(true, 'kedua pesan mencapai "Sampai" tanpa reload (SSE)')
  await axeCheck(page, 'transaksi')
  await page.getByRole('row').filter({ hasText: '+628123456789' }).click()
  await page.getByRole('heading', { name: 'Timeline' }).waitFor()
  const tl = (await page.locator('.timeline').textContent())
  check(['created', 'queued', 'assigned', 'sending', 'sent', 'delivered'].every((t) => tl.includes(t)), 'timeline memuat created→assigned→sending→sent→delivered')
  await shot('05-transaction-detail')
  await page.keyboard.press('Escape')
  await page.getByPlaceholder('Cari nomor (+62…)').fill('+62812999')
  await until(async () => (await page.getByRole('row').filter({ hasText: '+628123456789' }).count()) === 0 && (await page.getByRole('row').filter({ hasText: '+628129998877' }).count()) === 1, 8000, 'filter nomor')
  check(true, 'filter pencarian nomor bekerja')
  await page.getByPlaceholder('Cari nomor (+62…)').fill('')
  const csv = await ctx.request.get(URL_ + '/api/v1/messages/export')
  check(csv.status() === 200 && (await csv.text()).split('\n')[0].startsWith('id,created_at'), 'ekspor CSV mengikuti sesi')

  // ---- overview + settings ----
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  await page.getByText('Terkirim hari ini').waitFor()
  await until(async () => (await page.locator('.stat', { hasText: /sampai hari ini/i }).locator('.v').innerText()).trim() === '2', 8000, 'angka overview "sampai hari ini" = 2')
  const barH = await page.evaluate(() => { return Math.max(-1, ...[...document.querySelectorAll('.bars .d')].map((e) => e.getBoundingClientRect().height)) })
  check(barH > 5, `grafik overview menggambar batang (tinggi ${Math.round(barH)}px)`)
  check((await page.locator('.bars .b').count()) === 24, 'grafik selalu 24 slot jam')
  await shot('06-overview')
  await axeCheck(page, 'overview')
  await page.getByRole('link', { name: 'Pengaturan', exact: true }).click()
  await page.getByRole('tab', { name: 'Policy' }).waitFor()
  check((await page.getByText('default').count()) > 0, 'Pengaturan > Policy menampilkan policy default')
  await page.getByRole('tab', { name: 'Audit' }).click()
  await page.getByText('device.enroll').first().waitFor()
  check(true, 'audit log memuat device.enroll')

  // ---- kill switch ----
  await page.getByRole('link', { name: 'Overview', exact: true }).click()
  await page.getByRole('button', { name: /Pause semua pengiriman/ }).click()
  await page.getByLabel(/Ketik "PAUSE"/).fill('PAUSE')
  await page.getByRole('button', { name: 'Lanjutkan' }).click()
  await page.getByText(/Semua pengiriman DIJEDA/).waitFor()
  check(true, 'kill switch menampilkan banner')
  await shot('07-paused')
  await page.getByRole('button', { name: 'Lanjutkan' }).first().click()
  await until(async () => (await page.getByText(/Semua pengiriman DIJEDA/).count()) === 0, 8000, 'banner hilang')

  // ---- responsif + tema gelap ----
  await page.setViewportSize({ width: 390, height: 800 })
  for (const route of ['/', '/devices', '/transactions', '/send', '/settings']) {
    await page.goto(URL_ + route)
    await page.waitForLoadState('networkidle')
    const w = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth }))
    check(w.sw <= w.iw + 1, `tanpa scroll horizontal di 390px: ${route} (${w.sw}/${w.iw})`)
  }
  await shot('08-mobile-transactions')
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto(URL_ + '/transactions'); await page.waitForLoadState('networkidle')
  await axeCheck(page, 'transaksi (tema gelap)')
  await shot('09-dark-transactions')
  await page.emulateMedia({ colorScheme: 'light' })

  // ---- RBAC viewer ----
  const vctx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  const vp = await vctx.newPage()
  await vp.goto(URL_ + '/')
  await vp.getByLabel('Email').fill('viewer@e2e.test'); await vp.getByLabel('Password').fill('viewer-password-12')
  await vp.getByRole('button', { name: 'Masuk' }).click()
  await vp.getByRole('heading', { name: 'Overview' }).waitFor()
  check((await vp.getByRole('button', { name: /Pause semua/ }).count()) === 0, 'viewer tidak melihat tombol kill switch')
  await vp.getByRole('link', { name: 'Transaksi', exact: true }).click()
  await vp.getByRole('row').filter({ hasText: '****' }).first().waitFor()
  check(true, 'viewer melihat nomor termasking')
  check((await vp.getByRole('link', { name: 'Ekspor CSV' }).count()) === 0, 'viewer tidak punya Ekspor CSV')
  await vp.getByRole('link', { name: 'Pengaturan', exact: true }).click()
  await vp.getByText('Hanya admin yang dapat membuka pengaturan').waitFor({ timeout: 8000 })
  check(true, 'viewer ditolak di Pengaturan')

  // ---- logout ----
  await page.goto(URL_ + '/'); await page.getByRole('button', { name: 'Keluar' }).click()
  await page.getByRole('heading', { name: 'SMSC Gateway' }).waitFor()
  check(true, 'logout kembali ke login')

  const real = errors.filter((e) => !/favicon|Failed to load resource.*(401|429)/.test(e))
  check(real.length === 0, 'tidak ada error konsol/halaman', real.join(' | ').slice(0, 300))
  await browser.close()
}

main().catch((e) => { console.error('E2E gagal:', e.message); results.push([false, 'exception: ' + e.message]) }).finally(() => {
  procs.forEach((p) => { try { p.kill('SIGTERM') } catch {} })
  setTimeout(() => {
    try { execFileSync('psql', [BASE, '-qc', `DROP DATABASE IF EXISTS ${DB} WITH (FORCE)`]) } catch {}
    const bad = results.filter((r) => !r[0])
    console.log(`\n${results.length - bad.length}/${results.length} pemeriksaan lulus`)
    process.exit(bad.length ? 1 : 0)
  }, 800)
})
