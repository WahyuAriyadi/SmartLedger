// Cadangan terenkripsi, pemulihan, ekspor CSV, dan status penyimpanan.
// File cadangan berisi ciphertext yang SAMA dengan di perangkat + DEK yang terbungkus: aman disimpan di mana saja
// (WhatsApp, Drive, flashdisk) karena tanpa PIN/kode pemulihan tidak bisa dibuka.
(function (g) {
  'use strict';
  const L = (g.LedgerLocal = g.LedgerLocal || {});
  const V = () => L.vault;
  const C = () => L.crypto;
  const FORMAT = 'bukukas-backup';
  const MAX_FILE_BYTES = 80 * 1024 * 1024;
  const ID_RE = /^(business|tx|receipt|settings):[0-9A-Za-z-]{1,64}$/;

  const wibDay = (d) => L.ledger.wibDay(d);

  async function exportBackup({ includeImages = false } = {}) {
    const dump = await V().rawDump();
    const { b64 } = C();
    const m = dump.meta;
    const file = {
      format: FORMAT, v: 1, created_at: new Date().toISOString(),
      meta: { v: m.v, kdf: m.kdf, rec: m.rec, wrappedPin: m.wrappedPin, wrappedRec: m.wrappedRec, createdAt: m.createdAt },
      records: dump.records.map((r) => ({ id: r.id, iv: b64.enc(r.iv), ct: b64.enc(r.ct) })),
      images: includeImages ? dump.images.map((r) => ({ id: r.id, iv: b64.enc(r.iv), ct: b64.enc(r.ct) })) : [],
    };
    const blob = new Blob([JSON.stringify(file)], { type: 'application/octet-stream' });
    await V().markBackup();
    return { blob, filename: `bukukas-cadangan-${wibDay(new Date())}.bkp`, counts: { records: file.records.length, images: file.images.length, bytes: blob.size } };
  }

  // Validasi ketat: file cadangan adalah INPUT TIDAK TERPERCAYA (bisa korup atau sengaja dirangkai).
  function parseBackup(text) {
    const bad = (msg) => new Error(msg || 'File cadangan tidak valid.');
    if (typeof text !== 'string' || text.length > MAX_FILE_BYTES) throw bad('File cadangan terlalu besar atau rusak.');
    let o;
    try { o = JSON.parse(text); } catch { throw bad(); }
    if (!o || o.format !== FORMAT || o.v !== 1 || !o.meta || !Array.isArray(o.records) || !Array.isArray(o.images)) throw bad();
    const k = o.meta.kdf, rec = o.meta.rec;
    const isW = (w) => w && typeof w.iv === 'string' && typeof w.ct === 'string' && w.iv.length < 40 && w.ct.length < 200;
    if (!k || k.name !== 'PBKDF2-SHA256' || !Number.isInteger(k.iter) || k.iter < 1000 || k.iter > 2_000_000 ||
        typeof k.salt !== 'string' || !rec || typeof rec.salt !== 'string' || !isW(o.meta.wrappedPin) || !isW(o.meta.wrappedRec)) throw bad();
    if (o.records.length > 500000 || o.images.length > 20000) throw bad();
    const okRow = (r, re) => r && typeof r.id === 'string' && re.test(r.id) && typeof r.iv === 'string' && typeof r.ct === 'string';
    if (!o.records.every((r) => okRow(r, ID_RE)) || !o.images.every((r) => okRow(r, /^[0-9A-Za-z-]{1,64}$/))) throw bad();
    return o;
  }

  // Pulihkan: kredensial diverifikasi terhadap cadangan SEBELUM data lokal disentuh. Mengganti seluruh isi perangkat.
  async function restore(backup, credential) {
    const { b64 } = C();
    const meta = backup.meta;
    const dek = credential.recovery != null ? await C().openWithRecovery(meta, credential.recovery) : await C().openWithPin(meta, credential.pin);
    const rows = backup.records.map((r) => ({ id: r.id, iv: b64.dec(r.iv), ct: b64.dec(r.ct) }));
    // Uji dekripsi sampel: menolak cadangan yang kuncinya tidak cocok dengan isinya.
    const sample = rows.length <= 25 ? rows : rows.filter((_, i) => i % Math.ceil(rows.length / 25) === 0);
    for (const r of sample) await C().openJSON(dek, r.id, r);
    const images = backup.images.map((r) => ({ id: r.id, iv: b64.dec(r.iv), ct: b64.dec(r.ct) }));
    const m = { ...meta, counter: { changes: 0, lastBackupAt: backup.created_at || null }, lock: { fails: 0, nextTry: 0 } };
    await V().replaceAll(m, rows, images);
    V().V.meta = m; V().V.dek = dek;
    await V().loadAll();
    return { records: rows.length, images: images.length };
  }

  // ---------- ekspor CSV ----------
  // Sel yang diawali = + - @ (atau tab/CR) diberi awalan ' agar tidak dieksekusi sebagai rumus oleh Excel/Sheets (CSV injection).
  function csvCell(v) {
    let s = v == null ? '' : String(v);
    if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
    return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
  }
  function toCsv(bid, month) {
    const rows = L.ledger.active(bid)
      .filter((t) => !month || wibDay(t.occurred_at).slice(0, 7) === month)
      .sort((a, b) => Date.parse(a.occurred_at) - Date.parse(b.occurred_at));
    const kindName = { income: 'Pemasukan', expense: 'Pengeluaran', transfer: 'Pindah dompet' };
    const timeFmt = new Intl.DateTimeFormat('id-ID', { timeZone: 'Asia/Jakarta', hour: '2-digit', minute: '2-digit', hour12: false });
    const out = [['Tanggal', 'Jam', 'Dompet', 'Jenis', 'Arah', 'Kategori', 'Nominal (Rp)', 'Catatan', 'Sumber']];
    for (const t of rows) {
      const api = L.ledger.txApi(t);
      out.push([wibDay(t.occurred_at), timeFmt.format(new Date(t.occurred_at)), t.wallet === 'business' ? 'Usaha' : 'Pribadi',
        kindName[t.kind], t.direction === 'in' ? 'Masuk' : 'Keluar', api.category_name || '', t.amount, t.note || '', t.source === 'ocr' ? 'Scan nota' : 'Manual']);
    }
    return '\ufeff' + out.map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n';
  }

  // ---------- unduh / bagikan ----------
  function download(blob, filename) {
    const url = URL.createObjectURL(blob);
    const a = g.document.createElement('a');
    a.href = url; a.download = filename; a.rel = 'noopener';
    g.document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 30000);
  }
  async function share(blob, filename) {
    const file = new File([blob], filename, { type: 'application/octet-stream' });
    if (g.navigator.canShare && g.navigator.canShare({ files: [file] })) {
      await g.navigator.share({ files: [file], title: 'Cadangan Buku Kas Pintar' });
      return true;
    }
    return false;
  }

  // ---------- status penyimpanan ----------
  async function storageStatus() {
    const s = g.navigator && g.navigator.storage;
    const out = { persisted: null, usage: null, quota: null };
    try { if (s && s.persisted) out.persisted = await s.persisted(); } catch { /* tidak didukung */ }
    try { if (s && s.estimate) { const e = await s.estimate(); out.usage = e.usage; out.quota = e.quota; } } catch { /* tidak didukung */ }
    return out;
  }
  async function requestPersist() {
    try { return !!(g.navigator.storage && g.navigator.storage.persist && (await g.navigator.storage.persist())); } catch { return false; }
  }

  // Perlu mencadangkan? Banyak perubahan sejak cadangan terakhir, atau sudah >7 hari.
  function backupDue() {
    const c = V().meta && V().meta.counter;
    if (!c) return false;
    if (c.changes >= 20) return true;
    if (c.changes > 0 && !c.lastBackupAt && c.changes >= 5) return true;
    return !!(c.changes > 0 && c.lastBackupAt && Date.now() - Date.parse(c.lastBackupAt) > 7 * 86400000);
  }

  L.backup = { exportBackup, parseBackup, restore, toCsv, csvCell, download, share, storageStatus, requestPersist, backupDue };
})(typeof globalThis !== 'undefined' ? globalThis : window);
