require('fake-indexeddb/auto');
const fs = require('fs'), vm = require('vm'), assert = require('node:assert/strict'), test = require('node:test');
const DIR = require('path').join(__dirname, '../../internal/web/static/assets/');
for (const f of ['local-crypto.js', 'local-db.js', 'local-ledger.js', 'local-backup.js']) vm.runInThisContext(fs.readFileSync(DIR + f, 'utf8'), { filename: f });
const L = globalThis.LedgerLocal, C = L.crypto, V = L.vault;
const IT = { iterations: 1000 }; // cepat untuk tes; konstanta produksi diuji terpisah
const api = (m, p, b) => L.api(m, p, b);
const uuid = () => L.ledger.uuid();
const rejects = (p, re) => assert.rejects(p, re);
const bytesHas = (u8, needle) => Buffer.from(u8).includes(Buffer.from(needle));

async function fresh(pin = '482913x!') { await V.wipe(); const rec = await V.create(pin, IT); return { rec, pin }; }
async function scenario() {
  const { rec, pin } = await fresh();
  const b = await api('POST', '/businesses', { name: 'Warung Bu Sari', kind: 'warung_makan' });
  const cats = (await api('GET', `/businesses/${b.id}/categories`)).categories;
  const cat = (n) => cats.find((c) => c.name === n).id;
  return { rec, pin, bid: b.id, base: `/businesses/${b.id}`, cat };
}

test('kripto: PBKDF2 produksi 600.000 iterasi', () => assert.equal(C.PBKDF2_ITER, 600000));

test('kripto: buka dengan PIN, PIN salah ditolak, kode pemulihan, ganti PIN, rotasi', async () => {
  const { meta, recoveryCode } = await C.createVault('pin-rahasia-1', IT);
  assert.match(recoveryCode, /^([A-Z2-7]{4}-){7}[A-Z2-7]{4}$/);
  assert.ok(await C.openWithPin(meta, 'pin-rahasia-1'));
  await rejects(C.openWithPin(meta, 'pin-rahasia-2'), /PIN salah/);
  assert.ok(await C.openWithRecovery(meta, recoveryCode.toLowerCase().replace(/-/g, ' ')), 'normalisasi kode pemulihan');
  await rejects(C.openWithRecovery(meta, 'AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA'), /pemulihan salah/);
  // ganti PIN dengan PIN lama
  const m2 = await C.rewrapPin(meta, { pin: 'pin-rahasia-1' }, 'pin-baru-kuat-9', IT);
  assert.ok(await C.openWithPin(m2, 'pin-baru-kuat-9'));
  await rejects(C.openWithPin(m2, 'pin-rahasia-1'), /PIN salah/);
  await rejects(C.rewrapPin(meta, { pin: 'salah' }, 'x123456', IT), /PIN lama salah/);
  // lupa PIN: pakai kode pemulihan
  const m3 = await C.rewrapPin(meta, { recovery: recoveryCode }, 'pin-lewat-recovery', IT);
  assert.ok(await C.openWithPin(m3, 'pin-lewat-recovery'));
  // rotasi: kode lama mati, kode baru hidup, DEK sama (data lama tetap terbaca)
  const r = await C.rotateRecovery(meta, 'pin-rahasia-1');
  await rejects(C.openWithRecovery(r.meta, recoveryCode), /pemulihan salah/);
  assert.ok(await C.openWithRecovery(r.meta, r.recoveryCode));
  const k1 = await C.openWithPin(meta, 'pin-rahasia-1'), k2 = await C.openWithRecovery(r.meta, r.recoveryCode);
  const sealed = await C.sealJSON(k1, 'tx:1', { a: 1 });
  assert.deepEqual(await C.openJSON(k2, 'tx:1', sealed), { a: 1 });
});

test('kripto: AAD mengikat slot, modifikasi terdeteksi, IV acak', async () => {
  const { meta } = await C.createVault('pin-rahasia-1', IT);
  const dek = await C.openWithPin(meta, 'pin-rahasia-1');
  const a = await C.sealJSON(dek, 'tx:A', { note: 'rahasia' });
  const a2 = await C.sealJSON(dek, 'tx:A', { note: 'rahasia' });
  assert.notDeepEqual(a.iv, a2.iv);
  assert.ok(!bytesHas(a.ct, 'rahasia'));
  await rejects(C.openJSON(dek, 'tx:B', a), /rusak|diubah/); // dipindah ke slot lain
  const t = { iv: a.iv, ct: Uint8Array.from(a.ct) }; t.ct[3] ^= 1;
  await rejects(C.openJSON(dek, 'tx:A', t), /rusak|diubah/);
});

test('kripto: kekuatan PIN', () => {
  for (const p of ['12345', '123456', '111111', 'password']) assert.equal(C.strength(p).ok, false, p);
  assert.equal(C.strength('48291357').score, 1);
  assert.ok(C.strength('Kopi-Susu-Gula-2026!').score >= 3);
});

test('brankas: data tersimpan terenkripsi, terkunci tanpa PIN, kunci percobaan', async () => {
  const s = await scenario();
  await api('POST', `${s.base}/transactions`, { wallet: 'business', kind: 'income', amount: 123456, note: 'CATATAN-RAHASIA-PELANGGAN' });
  const dump = await V.rawDump();
  assert.ok(dump.records.length >= 2);
  for (const r of dump.records) {
    assert.ok(!bytesHas(r.ct, 'CATATAN-RAHASIA') && !bytesHas(r.ct, 'Warung Bu Sari') && !bytesHas(r.ct, '123456'), 'plaintext bocor di ' + r.id);
  }
  assert.ok(!JSON.stringify(dump.meta).includes('Warung'), 'metadata tidak boleh memuat data');
  V.lock();
  assert.equal(V.isOpen(), false);
  await rejects(api('GET', '/businesses'), /terkunci/);
  // 3 kali salah -> penundaan; PIN benar pun ditolak selama penundaan
  const realNow = Date.now; let t = realNow();
  Date.now = () => t;
  for (let i = 0; i < 3; i++) await rejects(V.unlock({ pin: 'salah-salah' }), /PIN salah/);
  await rejects(V.unlock({ pin: s.pin }), /Terlalu banyak percobaan/);
  t += 10_000; // lewat penundaan
  await V.unlock({ pin: s.pin });
  Date.now = realNow;
  assert.equal((await api('GET', '/businesses')).businesses.length, 1, 'data kembali utuh setelah buka kunci');
  await rejects(V.unlock({ pin: s.pin }).then(() => { V.lock(); return V.unlock({ pin: 'x' }); }), /PIN salah/);
});

test('brankas: modifikasi/penukaran ciphertext terdeteksi saat dibuka', async () => {
  const s = await scenario();
  await api('POST', `${s.base}/transactions`, { wallet: 'business', kind: 'income', amount: 1000 });
  await api('POST', `${s.base}/transactions`, { wallet: 'business', kind: 'income', amount: 2000 });
  const d = await V.rawDump();
  const tx = d.records.filter((r) => r.id.startsWith('tx:'));
  // tukar isi dua rekaman (serangan "ganti nominal")
  const swapped = d.records.map((r) => r.id === tx[0].id ? { ...r, iv: tx[1].iv, ct: tx[1].ct } : r);
  await V.replaceAll(d.meta, swapped, d.images);
  V.lock();
  await rejects(V.unlock({ pin: s.pin }), /rusak|diubah/);
});

test('ledger: skenario kebocoran kas sama dengan server (laba 2jt, prive 2,45jt)', async () => {
  const { base, cat } = await scenario();
  const cid = uuid();
  const inc = { wallet: 'business', kind: 'income', amount: 9_800_000, client_id: cid, note: 'penjualan' };
  const a = await api('POST', `${base}/transactions`, inc), a2 = await api('POST', `${base}/transactions`, inc);
  assert.equal(a.id, a2.id, 'client_id sama tidak menggandakan');
  await api('POST', `${base}/transactions`, { wallet: 'business', kind: 'expense', amount: 7_800_000, category_id: cat('Bahan baku') });
  const tr = await api('POST', `${base}/transfers`, { direction: 'to_personal', amount: 2_450_000 });
  assert.equal(tr.transactions.length, 2);
  const w = Object.fromEntries((await api('GET', `${base}/wallets`)).wallets.map((x) => [x.kind, x.balance]));
  assert.deepEqual(w, { business: -450_000, personal: 2_450_000 });
  const m = await api('GET', `${base}/reports/monthly`);
  assert.equal(m.profit, 2_000_000); assert.equal(m.net_prive, 2_450_000);
  assert.equal(m.leak_detected, true); assert.equal(m.leak_amount, 450_000);
  // hapus transfer memulihkan kedua dompet
  await api('DELETE', `${base}/transactions/${tr.transactions[0].id}`);
  const w2 = Object.fromEntries((await api('GET', `${base}/wallets`)).wallets.map((x) => [x.kind, x.balance]));
  assert.deepEqual(w2, { business: 2_000_000, personal: 0 });
  assert.equal((await api('GET', `${base}/reports/monthly`)).leak_detected, false);
});

test('ledger: paginasi, filter, edit, ringkasan harian, rincian kategori', async () => {
  const { base, cat } = await scenario();
  const day = L.ledger.wibDay(new Date()), yest = L.ledger.wibDay(new Date(Date.now() - 86400000));
  const yTime = new Date(Date.now() - 86400000).toISOString();
  const inc = await api('POST', `${base}/transactions`, { wallet: 'business', kind: 'income', amount: 100000, category_id: cat('Penjualan'), note: 'jual pagi' });
  const exp = await api('POST', `${base}/transactions`, { wallet: 'business', kind: 'expense', amount: 40000, category_id: cat('Bahan baku') });
  await api('POST', `${base}/transactions`, { wallet: 'business', kind: 'income', amount: 50000, occurred_at: yTime });
  const tr = await api('POST', `${base}/transfers`, { direction: 'to_personal', amount: 20000, note: 'uang jajan' });
  const count = async (q) => (await api('GET', `${base}/transactions?${q}`)).transactions.length;
  for (const [q, want] of [['', 5], ['kind=income', 2], ['kind=expense', 1], ['kind=transfer', 2], [`category_id=${cat('Bahan baku')}`, 1],
    [`from=${day}&to=${day}`, 4], [`to=${yest}`, 1], ['wallet=personal', 1]]) assert.equal(await count(q), want, q);
  for (const q of ['kind=x', 'category_id=x', 'from=bukan', `from=${day}&to=${yest}`, 'cursor=ngawur']) await rejects(api('GET', `${base}/transactions?${q}`), /tidak valid|harus|sebelum/);
  // paginasi stabil
  const p1 = await api('GET', `${base}/transactions?limit=2`);
  const p2 = await api('GET', `${base}/transactions?limit=2&cursor=${p1.next_cursor}`);
  const p3 = await api('GET', `${base}/transactions?limit=2&cursor=${p2.next_cursor}`);
  const ids = [...p1.transactions, ...p2.transactions, ...p3.transactions].map((t) => t.id);
  assert.equal(ids.length, 5); assert.equal(new Set(ids).size, 5); assert.equal(p3.next_cursor, '');
  // edit
  const up = await api('PUT', `${base}/transactions/${exp.id}`, { note: 'beras', category_id: cat('Listrik, air, dan gas') });
  assert.equal(up.category_name, 'Listrik, air, dan gas'); assert.equal(up.amount, 40000);
  await rejects(api('PUT', `${base}/transactions/${exp.id}`, { note: 'x', amount: 1 }), /tidak valid/);
  await rejects(api('PUT', `${base}/transactions/${exp.id}`, { note: 'x', category_id: cat('Penjualan') }), /tidak cocok/);
  await rejects(api('PUT', `${base}/transactions/${uuid()}`, { note: 'x' }), /tidak ditemukan/);
  await api('PUT', `${base}/transactions/${tr.transactions[0].id}`, { note: 'ganti', occurred_at: yTime });
  for (const t of (await api('GET', `${base}/transactions?kind=transfer`)).transactions) { assert.equal(t.note, 'ganti'); assert.equal(L.ledger.wibDay(t.occurred_at), yest); }
  const d = (await api('GET', `${base}/reports/daily?days=7`)).days;
  assert.equal(d.length, 7); assert.equal(d[6].date, day); assert.equal(d[6].income, 100000); assert.equal(d[6].expense, 40000); assert.equal(d[5].income, 50000);
  await rejects(api('GET', `${base}/reports/daily?days=40`), /days/);
  const br = await api('GET', `${base}/reports/categories`);
  assert.equal(br.total, 40000); assert.equal(br.items[0].name, 'Listrik, air, dan gas'); assert.equal(br.items[0].pct, 100);
  void inc;
});

test('ledger: validasi input & isolasi usaha', async () => {
  const { base } = await scenario();
  for (const body of [{ wallet: 'x', kind: 'income', amount: 1 }, { wallet: 'business', kind: 'transfer', amount: 1 },
    { wallet: 'business', kind: 'income', amount: 0 }, { wallet: 'business', kind: 'income', amount: 1.5 }, { wallet: 'business', kind: 'income', amount: 1e12 },
    { wallet: 'business', kind: 'income', amount: 5, note: 'a'.repeat(281) }, { wallet: 'business', kind: 'income', amount: 5, client_id: 'x' },
    { wallet: 'business', kind: 'income', amount: 5, occurred_at: new Date(Date.now() + 3 * 86400000).toISOString() },
    { wallet: 'business', kind: 'income', amount: 5, isAdmin: true }]) await rejects(api('POST', `${base}/transactions`, body), /.+/, JSON.stringify(body));
  await rejects(api('GET', `/businesses/${uuid()}/wallets`), /usaha tidak ditemukan/);
  await rejects(api('POST', '/businesses', { name: 'x' }), /2-80/);
  for (let i = 0; i < 4; i++) await api('POST', '/businesses', { name: `Usaha ${i + 2}` });
  await rejects(api('POST', '/businesses', { name: 'Usaha ke-6' }), /maksimal 5/);
});

const fakeJpeg = () => new File([Uint8Array.from([0xff, 0xd8, 0xff, 0xe0, ...Array(300).fill(7)])], 'nota.jpg', { type: 'image/jpeg' });
const form = (f) => { const fd = new FormData(); fd.append('image', f); return fd; };
const ocrOk = { engine: 'mock', failed: false, merchant: 'Toko Sumber Rejeki', date: '', confidence: 0.94, items: [
  { name: 'Beras 25 kg', qty: 1, unit_price: 310000, category_hint: 'Bahan baku' }, { name: 'Plastik', qty: 2.5, unit_price: 8000, category_hint: 'Kemasan' }] };

test('nota: unggah (OCR gateway), foto terenkripsi lokal, konfirmasi atomik per kategori', async () => {
  const { base, cat } = await scenario();
  const calls = [];
  globalThis.fetch = async (url, opts) => { calls.push({ url, opts }); return new Response(JSON.stringify(ocrOk), { status: 200 }); };
  const r = await api('POST', `${base}/receipts`, form(fakeJpeg()));
  assert.equal(calls.length, 1); assert.equal(calls[0].url, '/v1/ocr/extract');
  assert.equal(r.status, 'needs_review'); assert.equal(r.items.length, 2); assert.equal(r.items[0].category_id, cat('Bahan baku'));
  const blob = await api('GET', `${base}/receipts/${r.id}/image`);
  assert.equal(blob.size, 304);
  const img = (await V.rawDump()).images[0];
  assert.ok(!bytesHas(img.ct, 'xxxx') && img.ct.length >= 304, 'foto tersimpan sebagai ciphertext');
  const items = [{ name: 'Beras', qty: 1, unit_price: 310000, category_id: cat('Bahan baku') }, { name: 'Minyak', qty: 1, unit_price: 95000, category_id: cat('Bahan baku') },
    { name: 'Plastik', qty: 2.5, unit_price: 8000, category_id: cat('Kemasan') }];
  await rejects(api('POST', `${base}/receipts/${r.id}/confirm`, { items: [{ name: 'x', qty: 1, unit_price: 5, category_id: cat('Penjualan') }] }), /tidak cocok/);
  await rejects(api('POST', `${base}/receipts/${r.id}/confirm`, { items: [{ name: 'x', qty: -1, unit_price: 5 }] }), /tidak valid/);
  const res = await api('POST', `${base}/receipts/${r.id}/confirm`, { merchant: 'Toko Sumber Rejeki', date: '2026-10-05', items });
  assert.equal(res.total, 425000); assert.equal(res.transactions.length, 2);
  await rejects(api('POST', `${base}/receipts/${r.id}/confirm`, { items }), /sudah dikonfirmasi/);
  assert.equal((await api('GET', `${base}/wallets`)).wallets.find((w) => w.kind === 'business').balance, -425000);
  const t = (await api('GET', `${base}/transactions?kind=expense`)).transactions;
  assert.ok(t.every((x) => x.source === 'ocr' && x.note === 'Toko Sumber Rejeki'));
});

test('nota: OCR gagal/offline tetap bisa diisi manual; error gerbang diteruskan', async () => {
  const { base, cat } = await scenario();
  globalThis.fetch = async () => { throw new TypeError('offline'); };
  const r = await api('POST', `${base}/receipts`, form(fakeJpeg()));
  assert.equal(r.status, 'failed'); assert.equal(r.items.length, 0);
  const res = await api('POST', `${base}/receipts/${r.id}/confirm`, { items: [{ name: 'Gas', qty: 1, unit_price: 21000, category_id: cat('Listrik, air, dan gas') }] });
  assert.equal(res.total, 21000);
  globalThis.fetch = async () => new Response(JSON.stringify({ error: { message: 'kuota OCR harian sudah habis' } }), { status: 429 });
  await rejects(api('POST', `${base}/receipts`, form(fakeJpeg())), /kuota OCR/);
  await rejects(api('POST', `${base}/receipts`, form(new File([new Uint8Array(400)], 'x.jpg'))), /JPEG atau PNG/);
  await rejects(api('POST', `${base}/receipts`, form(new File([new Uint8Array(6 << 20)], 'x.jpg'))), /5 MB/);
  let sent = null;
  globalThis.fetch = async (u, o) => { sent = o; return new Response(JSON.stringify({ failed: true, items: [] }), { status: 200 }); };
  await V.commit([{ type: 'settings', id: 'main', obj: { ocrCode: 'kode-akses' } }]); V.state.settings = { ocrCode: 'kode-akses' };
  await api('POST', `${base}/receipts`, form(fakeJpeg()));
  assert.equal(sent.headers['X-Access-Code'], 'kode-akses');
});

test('cadangan: ekspor -> hapus -> pulihkan (PIN & kode pemulihan), kredensial salah tidak merusak data', async () => {
  const s = await scenario();
  await api('POST', `${s.base}/transactions`, { wallet: 'business', kind: 'income', amount: 777000, note: 'penting' });
  const { blob, counts, filename } = await L.backup.exportBackup({ includeImages: true });
  assert.match(filename, /^bukukas-cadangan-\d{4}-\d{2}-\d{2}\.bkp$/);
  const text = await blob.text();
  assert.ok(!text.includes('penting') && !text.includes('Warung') && !text.includes('777000'), 'cadangan tidak boleh memuat plaintext');
  assert.equal(V.meta.counter.changes, 0, 'penghitung perubahan direset setelah cadangan');
  const backup = L.backup.parseBackup(text);

  // data lokal berbeda; pemulihan dengan PIN salah TIDAK boleh mengubah apa pun
  await V.wipe(); await V.create('pin-lain-lain-1', IT);
  await api('POST', '/businesses', { name: 'Usaha Lain' });
  await rejects(L.backup.restore(backup, { pin: 'salah-salah' }), /PIN salah/);
  assert.equal((await api('GET', '/businesses')).businesses[0].name, 'Usaha Lain', 'data lokal utuh setelah pemulihan gagal');
  // cadangan dirusak -> ditolak sebelum menimpa
  const broken = JSON.parse(text); broken.records[0].ct = broken.records[0].ct.replace(/.$/, (c) => (c === 'A' ? 'B' : 'A'));
  await rejects(L.backup.restore(L.backup.parseBackup(JSON.stringify(broken)), { pin: s.pin }).then(async () => { V.lock(); await V.unlock({ pin: s.pin }); }), /rusak|diubah|Data/);
  await V.wipe(); await V.create('pin-lain-lain-1', IT);

  const info = await L.backup.restore(backup, { pin: s.pin });
  assert.equal(info.records, counts.records);
  assert.equal((await api('GET', '/businesses')).businesses[0].name, 'Warung Bu Sari');
  const tx = (await api('GET', `${s.base}/transactions`)).transactions;
  assert.equal(tx[0].amount, 777000); assert.equal(tx[0].note, 'penting');
  // lewat kode pemulihan di perangkat baru
  await V.wipe();
  await L.backup.restore(backup, { recovery: s.rec });
  assert.equal((await api('GET', `${s.base}/wallets`)).wallets[0].balance, 777000);
  V.lock(); await V.unlock({ pin: s.pin }); // PIN lama tetap berlaku pada data hasil pemulihan
  assert.equal(V.isOpen(), true);
});

test('cadangan: parseBackup menolak input berbahaya/korup', () => {
  const ok = { format: 'bukukas-backup', v: 1, created_at: 'x', meta: { kdf: { name: 'PBKDF2-SHA256', iter: 1000, salt: 'AAAA' }, rec: { salt: 'AAAA' }, wrappedPin: { iv: 'AA', ct: 'AA' }, wrappedRec: { iv: 'AA', ct: 'AA' } }, records: [], images: [] };
  assert.doesNotThrow(() => L.backup.parseBackup(JSON.stringify(ok)));
  const mut = (fn) => { const o = JSON.parse(JSON.stringify(ok)); fn(o); return JSON.stringify(o); };
  for (const s of ['bukan json', '{}', 'null', mut((o) => { o.format = 'lain'; }), mut((o) => { o.v = 2; }),
    mut((o) => { o.meta.kdf.iter = 999_999_999; }), mut((o) => { o.meta.kdf.iter = 5; }), mut((o) => { o.meta.kdf.name = 'MD5'; }),
    mut((o) => { o.records = [{ id: '../../etc/passwd', iv: 'A', ct: 'A' }]; }), mut((o) => { o.records = [{ id: 'tx:abc', iv: 1, ct: 'A' }]; }),
    mut((o) => { o.images = [{ id: 'a/b', iv: 'A', ct: 'A' }]; }), mut((o) => { delete o.meta.wrappedPin; })]) {
    assert.throws(() => L.backup.parseBackup(s), /tidak valid|rusak|terlalu/, s.slice(0, 60));
  }
});

test('CSV: tanpa injeksi rumus, kutip & koma aman, BOM UTF-8', async () => {
  const { base, bid } = await scenario();
  for (const note of ['=HYPERLINK("http://jahat","klik")', '+1+1', '-2+3', '@SUM(A1)', 'biasa, ada "kutip"', 'baris\nbaru']) {
    await api('POST', `${base}/transactions`, { wallet: 'business', kind: 'income', amount: 1000, note });
  }
  const csv = L.backup.toCsv(bid, '');
  assert.ok(csv.startsWith('\ufeffTanggal,Jam,Dompet,Jenis,Arah,Kategori,Nominal (Rp),Catatan,Sumber\r\n'));
  for (const bad of ['=HYPERLINK', '+1+1', '-2+3', '@SUM']) {
    assert.ok(!csv.split('\r\n').some((l) => l.split(',').some((c) => c.replace(/^"/, '').startsWith(bad))), 'rumus tidak ternetralkan: ' + bad);
  }
  assert.ok(csv.includes(`"'=HYPERLINK(""http://jahat"",""klik"")"`), 'rumus diberi awalan kutip & kutip digandakan');
  assert.ok(csv.includes('"biasa, ada ""kutip"""'));
  assert.equal(L.backup.csvCell("'sudah"), "'sudah");
  assert.equal(L.backup.toCsv(bid, '1999-01').trim().split('\r\n').length, 1, 'bulan kosong hanya header');
});

test('cadangan: pengingat jatuh tempo', async () => {
  await scenario();
  assert.equal(L.backup.backupDue(), false);
  V.meta.counter = { changes: 20, lastBackupAt: null }; assert.equal(L.backup.backupDue(), true);
  V.meta.counter = { changes: 6, lastBackupAt: null }; assert.equal(L.backup.backupDue(), true);
  V.meta.counter = { changes: 2, lastBackupAt: null }; assert.equal(L.backup.backupDue(), false);
  V.meta.counter = { changes: 1, lastBackupAt: new Date(Date.now() - 8 * 86400000).toISOString() }; assert.equal(L.backup.backupDue(), true);
  V.meta.counter = { changes: 0, lastBackupAt: new Date(Date.now() - 30 * 86400000).toISOString() }; assert.equal(L.backup.backupDue(), false);
});

test('pengaturan: ganti PIN lewat brankas (PIN lama berhenti berlaku, data utuh)', async () => {
  const s = await scenario();
  await api('POST', `${s.base}/transactions`, { wallet: 'business', kind: 'income', amount: 5000 });
  await V.changePin({ pin: s.pin }, 'pin-sangat-baru-77');
  V.lock();
  await rejects(V.unlock({ pin: s.pin }), /PIN salah/);
  await V.unlock({ pin: 'pin-sangat-baru-77' });
  assert.equal((await api('GET', `${s.base}/wallets`)).wallets[0].balance, 5000);
  const code = await V.rotateRecovery('pin-sangat-baru-77');
  V.lock(); await V.unlock({ recovery: code });
  assert.equal(V.isOpen(), true);
});
