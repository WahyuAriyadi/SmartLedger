// Logika buku kas di perangkat. Meniru kontrak REST server (bentuk JSON, aturan validasi, pesan galat yang sama)
// agar antarmuka tidak perlu tahu data ada di mana: api(method, path, body) -> objek JSON, sama seperti fetch ke /v1.
// Semua perhitungan (saldo, laba, kebocoran kas) dijalankan di sini, pada data yang hanya terdekripsi saat aplikasi terbuka.
(function (g) {
  'use strict';
  const L = (g.LedgerLocal = g.LedgerLocal || {});

  const MAX_AMOUNT = 10_000_000_000, MAX_NOTE = 280, MAX_ITEMS = 50, MAX_UPLOAD = 5 * 1024 * 1024;

  class ApiError extends Error { constructor(msg, status = 400) { super(msg); this.status = status; } }
  const bad = (m) => new ApiError(m, 400);
  const notFound = (m) => new ApiError(m, 404);

  const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
  function uuid() {
    if (g.crypto.randomUUID) return g.crypto.randomUUID();
    const b = g.crypto.getRandomValues(new Uint8Array(16));
    b[6] = (b[6] & 15) | 64; b[8] = (b[8] & 63) | 128;
    const h = [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
  }
  const runes = (s) => [...String(s || '')].length;
  const cleanText = (s, limit) => [...String(s || '').replace(/[\u0000-\u001f\u007f]/g, '').trim()].slice(0, limit).join('');

  // ---------- kategori bawaan (ID tetap, sama dengan nama di server) ----------
  const CATEGORY_SEED = [
    ['income', 'business', 'Penjualan'], ['income', 'business', 'Pendapatan lain'],
    ['expense', 'business', 'Bahan baku'], ['expense', 'business', 'Kemasan'], ['expense', 'business', 'Gaji karyawan'],
    ['expense', 'business', 'Sewa tempat'], ['expense', 'business', 'Listrik, air, dan gas'], ['expense', 'business', 'Transportasi'],
    ['expense', 'business', 'Pemasaran'], ['expense', 'business', 'Perawatan alat'], ['expense', 'business', 'Lain-lain (usaha)'],
    ['expense', 'personal', 'Kebutuhan rumah tangga'], ['expense', 'personal', 'Pendidikan'], ['expense', 'personal', 'Kesehatan'],
    ['expense', 'personal', 'Lain-lain (pribadi)'],
    ['transfer', 'any', 'Ambil pribadi (prive)'], ['transfer', 'any', 'Setor modal'],
  ];
  const CATEGORIES = CATEGORY_SEED.map(([kind, scope, name], i) => ({
    id: `00000000-0000-4000-8000-${String(i + 1).padStart(12, '0')}`, kind, scope, name,
  }));
  const catById = (id) => CATEGORIES.find((c) => c.id === id);
  const catByName = (n) => CATEGORIES.find((c) => c.name === n);

  // ---------- waktu (zona WIB, sama dengan server) ----------
  const wibFmt = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Jakarta', year: 'numeric', month: '2-digit', day: '2-digit' });
  const wibDay = (d) => wibFmt.format(new Date(d)); // YYYY-MM-DD
  const wibStart = (day) => new Date(day + 'T00:00:00+07:00');
  const dateOk = (iso, now) => {
    const t = Date.parse(iso);
    return !Number.isNaN(t) && t <= now + 86400000 && t >= now - 5 * 365.25 * 86400000;
  };

  // ---------- akses data ----------
  const V = () => L.vault;
  function S() {
    if (!V().isOpen()) throw new ApiError('Aplikasi terkunci. Masukkan PIN.', 401);
    return V().state;
  }
  function biz(bid) {
    const b = S().businesses.find((x) => x.id === bid);
    if (!b) throw notFound('usaha tidak ditemukan');
    return b;
  }
  const active = (bid) => S().transactions.filter((t) => t.business_id === bid && !t.deleted_at);
  const lineTotal = (qty, price) => Math.round(qty * price);
  const nowMs = () => Date.now();

  function strict(body, allowed) {
    if (!body || typeof body !== 'object' || Array.isArray(body)) throw bad('format permintaan tidak valid');
    for (const k of Object.keys(body)) if (!allowed.includes(k)) throw bad('format permintaan tidak valid');
  }
  function checkAmount(a) {
    if (!Number.isInteger(a) || a < 1 || a > MAX_AMOUNT) throw bad('nominal harus antara Rp1 dan Rp10.000.000.000');
  }
  function checkNote(n) { if (runes(n) > MAX_NOTE) throw bad('catatan maksimal 280 karakter'); }
  function checkOptUUID(v, msg) { if (v != null && v !== '' && !UUID_RE.test(v)) throw bad(msg); }

  const txApi = (t) => ({
    id: t.id, wallet: t.wallet, kind: t.kind, direction: t.direction, amount: t.amount,
    category_id: t.category_id || null, category_name: t.category_id ? (catById(t.category_id) || {}).name || null : null,
    note: t.note || '', occurred_at: t.occurred_at, source: t.source, transfer_group_id: t.transfer_group_id || null,
    created_at: t.created_at,
  });
  const byTimeDesc = (a, b) => Date.parse(b.occurred_at) - Date.parse(a.occurred_at) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0);

  // ---------- usaha, dompet, kategori ----------
  function me() { return { id: 'device', name: (S().settings && S().settings.name) || 'Pemilik', phone: '', created_at: V().meta.createdAt }; }

  async function createBusiness(body) {
    strict(body, ['name', 'kind']);
    const name = cleanText(body.name, 80);
    if (runes(name) < 2) throw bad('nama usaha harus 2-80 karakter');
    const kind = body.kind || 'lainnya';
    if (!['warung_makan', 'toko_kelontong', 'pedagang_pasar', 'jasa', 'lainnya'].includes(kind)) throw bad('jenis usaha tidak dikenal');
    if (S().businesses.length >= 5) throw new ApiError('maksimal 5 usaha per akun', 409);
    const b = { id: uuid(), name, kind, created_at: new Date().toISOString() };
    await V().commit([{ type: 'business', id: b.id, obj: b }]);
    S().businesses.push(b);
    return b;
  }

  function balances(bid) {
    const bal = { business: 0, personal: 0 };
    for (const t of active(bid)) bal[t.wallet] += t.direction === 'in' ? t.amount : -t.amount;
    return bal;
  }
  function wallets(bid) {
    biz(bid);
    const bal = balances(bid);
    return { wallets: [
      { id: `${bid}:business`, kind: 'business', name: 'Dompet Usaha', balance: bal.business },
      { id: `${bid}:personal`, kind: 'personal', name: 'Dompet Pribadi', balance: bal.personal },
    ] };
  }

  // ---------- transaksi ----------
  function validateCategory(categoryId, kind, walletKind) {
    if (!categoryId) return null;
    const c = catById(categoryId);
    if (!c) throw bad('kategori tidak ditemukan');
    if (c.kind !== kind || (c.scope !== 'any' && c.scope !== walletKind)) throw bad('kategori tidak cocok dengan jenis atau dompet');
    return c.id;
  }

  async function addTransaction(bid, body) {
    biz(bid);
    strict(body, ['wallet', 'kind', 'category_id', 'amount', 'occurred_at', 'note', 'client_id']);
    if (body.wallet !== 'business' && body.wallet !== 'personal') throw bad("dompet harus 'business' atau 'personal'");
    if (body.kind !== 'income' && body.kind !== 'expense') throw bad("jenis harus 'income' atau 'expense'");
    checkAmount(body.amount); checkNote(body.note || '');
    checkOptUUID(body.category_id, 'category_id tidak valid'); checkOptUUID(body.client_id, 'client_id harus UUID');
    if (body.occurred_at && !dateOk(body.occurred_at, nowMs())) throw bad('tanggal transaksi di luar rentang yang diizinkan');
    if (body.client_id) {
      const dup = S().transactions.find((t) => t.business_id === bid && t.client_id === body.client_id && !t.deleted_at);
      if (dup) return { created: false, tx: txApi(dup) }; // pengiriman ulang: tidak menggandakan
    }
    const cat = validateCategory(body.category_id, body.kind, body.wallet);
    const t = {
      id: uuid(), business_id: bid, wallet: body.wallet, kind: body.kind, direction: body.kind === 'income' ? 'in' : 'out',
      amount: body.amount, category_id: cat, note: body.note || '', source: 'manual', client_id: body.client_id || null,
      occurred_at: body.occurred_at ? new Date(body.occurred_at).toISOString() : new Date().toISOString(),
      created_at: new Date().toISOString(), deleted_at: null, transfer_group_id: null, receipt_id: null,
    };
    await V().commit([{ type: 'tx', id: t.id, obj: t }]);
    S().transactions.push(t);
    return { created: true, tx: txApi(t) };
  }

  async function transfer(bid, body) {
    biz(bid);
    strict(body, ['direction', 'amount', 'note', 'client_id']);
    if (body.direction !== 'to_personal' && body.direction !== 'to_business') throw bad("arah harus 'to_personal' atau 'to_business'");
    checkAmount(body.amount); checkNote(body.note || ''); checkOptUUID(body.client_id, 'client_id harus UUID');
    if (body.client_id) {
      const dup = S().transactions.find((t) => t.business_id === bid && t.client_id === body.client_id && !t.deleted_at);
      if (dup) return { created: false, transactions: S().transactions.filter((t) => t.transfer_group_id === dup.transfer_group_id && !t.deleted_at).map(txApi) };
    }
    const toPersonal = body.direction === 'to_personal';
    const cat = catByName(toPersonal ? 'Ambil pribadi (prive)' : 'Setor modal').id;
    const gid = uuid(), now = new Date().toISOString();
    const base = { business_id: bid, kind: 'transfer', amount: body.amount, category_id: cat, note: body.note || '', source: 'manual',
      occurred_at: now, created_at: now, deleted_at: null, transfer_group_id: gid, receipt_id: null };
    const out = { ...base, id: uuid(), wallet: toPersonal ? 'business' : 'personal', direction: 'out', client_id: body.client_id || null };
    const inn = { ...base, id: uuid(), wallet: toPersonal ? 'personal' : 'business', direction: 'in', client_id: null };
    // Dua baris ditulis dalam SATU transaksi IndexedDB: atomik, tak mungkin tersimpan setengah.
    await V().commit([{ type: 'tx', id: out.id, obj: out }, { type: 'tx', id: inn.id, obj: inn }]);
    S().transactions.push(out, inn);
    return { created: true, transactions: [txApi(out), txApi(inn)] };
  }

  function listTransactions(bid, q) {
    biz(bid);
    const wallet = q.get('wallet') || '', kind = q.get('kind') || '', cat = q.get('category_id') || '';
    if (wallet && wallet !== 'business' && wallet !== 'personal') throw bad("dompet harus 'business' atau 'personal'");
    if (kind && !['income', 'expense', 'transfer'].includes(kind)) throw bad("jenis harus 'income', 'expense', atau 'transfer'");
    if (cat && !UUID_RE.test(cat)) throw bad('category_id tidak valid');
    let from = null, to = null;
    for (const [name, val] of [['from', q.get('from')], ['to', q.get('to')]]) {
      if (!val) continue;
      if (!/^\d{4}-\d{2}-\d{2}$/.test(val) || Number.isNaN(wibStart(val).getTime())) throw bad('format tanggal harus YYYY-MM-DD');
      if (name === 'from') from = wibStart(val).getTime(); else to = wibStart(val).getTime() + 86400000; // akhir inklusif
    }
    if (from !== null && to !== null && to <= from) throw bad('tanggal akhir tidak boleh sebelum tanggal awal');
    let limit = parseInt(q.get('limit') || '30', 10); if (!(limit > 0)) limit = 30; limit = Math.min(limit, 100);

    let arr = active(bid).filter((t) => (!wallet || t.wallet === wallet) && (!kind || t.kind === kind) && (!cat || t.category_id === cat) &&
      (from === null || Date.parse(t.occurred_at) >= from) && (to === null || Date.parse(t.occurred_at) < to));
    arr.sort(byTimeDesc);
    const cursor = q.get('cursor');
    if (cursor) {
      let ts, id;
      try { [ts, id] = atob(cursor.replace(/-/g, '+').replace(/_/g, '/')).split('|'); } catch { throw bad('cursor tidak valid'); }
      const c = Date.parse(ts);
      if (Number.isNaN(c) || !UUID_RE.test(id || '')) throw bad('cursor tidak valid');
      arr = arr.filter((t) => { const x = Date.parse(t.occurred_at); return x < c || (x === c && t.id < id); });
    }
    const page = arr.slice(0, limit);
    let next = '';
    if (arr.length > limit) {
      const last = page[page.length - 1];
      next = btoa(`${last.occurred_at}|${last.id}`).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }
    return { transactions: page.map(txApi), next_cursor: next };
  }

  async function updateTransaction(bid, id, body) {
    biz(bid);
    strict(body, ['note', 'category_id', 'occurred_at']);
    const t = active(bid).find((x) => x.id === id);
    if (!t) throw notFound('transaksi tidak ditemukan');
    const note = body.note || '';
    checkNote(note); checkOptUUID(body.category_id, 'category_id tidak valid');
    if (body.occurred_at && !dateOk(body.occurred_at, nowMs())) throw bad('tanggal transaksi di luar rentang yang diizinkan');
    const when = body.occurred_at ? new Date(body.occurred_at).toISOString() : null;
    const targets = t.kind === 'transfer' ? active(bid).filter((x) => x.transfer_group_id === t.transfer_group_id) : [t];
    const cat = t.kind === 'transfer' ? null : validateCategory(body.category_id, t.kind, t.wallet);
    const next = targets.map((x) => ({ ...x, note, occurred_at: when || x.occurred_at, ...(t.kind === 'transfer' ? {} : { category_id: cat }) }));
    await V().commit(next.map((x) => ({ type: 'tx', id: x.id, obj: x })));
    next.forEach((x) => Object.assign(S().transactions.find((y) => y.id === x.id), x));
    return txApi(next.find((x) => x.id === id));
  }

  // Hapus lunak (bisa dipulihkan: undo sungguhan). Transfer menghapus kedua sisinya.
  async function deleteTransaction(bid, id) {
    biz(bid);
    const t = active(bid).find((x) => x.id === id);
    if (!t) throw notFound('transaksi tidak ditemukan');
    const now = new Date().toISOString();
    const targets = t.kind === 'transfer' ? active(bid).filter((x) => x.transfer_group_id === t.transfer_group_id) : [t];
    const next = targets.map((x) => ({ ...x, deleted_at: now }));
    await V().commit(next.map((x) => ({ type: 'tx', id: x.id, obj: x })));
    next.forEach((x) => Object.assign(S().transactions.find((y) => y.id === x.id), x));
    return null;
  }

  // ---------- laporan ----------
  function monthly(bid, month) {
    biz(bid);
    month = month || wibDay(new Date()).slice(0, 7);
    if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(month)) throw bad('format bulan harus YYYY-MM');
    const m = { month, revenue: 0, expenses: 0, profit: 0, prive: 0, capital_in: 0, net_prive: 0, leak_detected: false, leak_amount: 0 };
    for (const t of active(bid)) {
      if (t.wallet !== 'business' || wibDay(t.occurred_at).slice(0, 7) !== month) continue;
      if (t.kind === 'income') m.revenue += t.amount;
      else if (t.kind === 'expense') m.expenses += t.amount;
      else if (t.kind === 'transfer') { if (t.direction === 'out') m.prive += t.amount; else m.capital_in += t.amount; }
    }
    m.profit = m.revenue - m.expenses;
    m.net_prive = m.prive - m.capital_in;
    if (m.net_prive > m.profit) { m.leak_detected = true; m.leak_amount = m.net_prive - m.profit; }
    return m;
  }

  function daily(bid, days) {
    biz(bid);
    if (!(days >= 1 && days <= 31)) throw bad('days harus 1-31');
    const today = wibDay(new Date());
    const out = [];
    for (let i = days - 1; i >= 0; i--) {
      out.push({ date: new Date(Date.parse(today + 'T00:00:00Z') - i * 86400000).toISOString().slice(0, 10), income: 0, expense: 0 });
    }
    const idx = new Map(out.map((d, i) => [d.date, i]));
    for (const t of active(bid)) {
      if (t.wallet !== 'business' || (t.kind !== 'income' && t.kind !== 'expense')) continue;
      const i = idx.get(wibDay(t.occurred_at));
      if (i !== undefined) out[i][t.kind] += t.amount;
    }
    return { days: out };
  }

  // Rincian pengeluaran usaha per kategori (hanya mode perangkat).
  function categoryBreakdown(bid, month) {
    biz(bid);
    month = month || wibDay(new Date()).slice(0, 7);
    if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(month)) throw bad('format bulan harus YYYY-MM');
    const sums = new Map();
    for (const t of active(bid)) {
      if (t.wallet !== 'business' || t.kind !== 'expense' || wibDay(t.occurred_at).slice(0, 7) !== month) continue;
      sums.set(t.category_id || '', (sums.get(t.category_id || '') || 0) + t.amount);
    }
    const total = [...sums.values()].reduce((a, b) => a + b, 0);
    const items = [...sums.entries()].map(([id, amount]) => ({
      category_id: id || null, name: id ? (catById(id) || {}).name : 'Tanpa kategori', amount, pct: total ? Math.round((amount / total) * 100) : 0,
    })).sort((a, b) => b.amount - a.amount);
    return { month, total, items };
  }

  // ---------- nota (foto tetap di perangkat; hanya foto yang dikirim ke gerbang OCR, tanpa disimpan di server) ----------
  function sniff(b) {
    if (b.length > 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return 'image/jpeg';
    if (b.length > 4 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return 'image/png';
    return '';
  }

  // Perkecil di perangkat (maks 1600 px, JPEG 85%): hemat kuota data & biaya OCR, sekaligus membuang metadata EXIF/GPS.
  async function prepareImage(bytes, type) {
    try {
      if (typeof g.createImageBitmap !== 'function' || (typeof g.OffscreenCanvas === 'undefined' && !g.document)) return { bytes, type };
      const bmp = await g.createImageBitmap(new Blob([bytes], { type }));
      const scale = Math.min(1, 1600 / Math.max(bmp.width, bmp.height));
      const w = Math.max(1, Math.round(bmp.width * scale)), h = Math.max(1, Math.round(bmp.height * scale));
      const canvas = typeof g.OffscreenCanvas !== 'undefined' ? new g.OffscreenCanvas(w, h) : Object.assign(g.document.createElement('canvas'), { width: w, height: h });
      const ctx = canvas.getContext('2d');
      ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, w, h); ctx.drawImage(bmp, 0, 0, w, h);
      const blob = canvas.convertToBlob ? await canvas.convertToBlob({ type: 'image/jpeg', quality: 0.85 })
        : await new Promise((res) => canvas.toBlob(res, 'image/jpeg', 0.85));
      if (!blob) return { bytes, type };
      return { bytes: new Uint8Array(await blob.arrayBuffer()), type: 'image/jpeg' };
    } catch { return { bytes, type }; }
  }

  async function callOcr(img) {
    const fd = new FormData();
    fd.append('image', new Blob([img.bytes], { type: img.type }), 'nota.jpg');
    const headers = {};
    const code = S().settings && S().settings.ocrCode;
    if (code) headers['X-Access-Code'] = code;
    let res;
    try { res = await fetch('/v1/ocr/extract', { method: 'POST', headers, body: fd }); }
    catch { return null; } // offline: buat draf kosong agar nota tetap bisa diisi manual
    let data = null;
    try { data = await res.json(); } catch { /* bukan JSON */ }
    if (res.status === 200 && data) return data;
    const msg = (data && data.error && data.error.message) || 'Layanan OCR sedang tidak tersedia.';
    if ([400, 401, 413, 429].includes(res.status)) throw new ApiError(msg, res.status);
    return null; // galat server/jaringan: turun ke input manual
  }

  const receiptApi = (r) => ({
    id: r.id, status: r.status, engine: r.engine, merchant: r.merchant || '', receipt_date: r.receipt_date || null,
    total: r.total || 0, confidence: r.confidence || 0, created_at: r.created_at,
    items: r.items.map((i) => ({ name: i.name, qty: i.qty, unit_price: i.unit_price, subtotal: lineTotal(i.qty, i.unit_price), category_id: i.category_id || null })),
  });

  async function receiptUpload(bid, formData) {
    biz(bid);
    const file = formData && formData.get && formData.get('image');
    if (!file || typeof file.arrayBuffer !== 'function') throw bad("field 'image' wajib ada");
    if (file.size > MAX_UPLOAD) throw new ApiError('ukuran foto maksimal 5 MB', 413);
    const bytes = new Uint8Array(await file.arrayBuffer());
    const type = sniff(bytes);
    if (!type) throw bad('format gambar harus JPEG atau PNG');
    const img = await prepareImage(bytes, type);
    const ex = await callOcr(img);

    const id = uuid();
    const hints = new Map(CATEGORIES.filter((c) => c.kind === 'expense' && c.scope !== 'personal').map((c) => [c.name, c.id]));
    const items = ex && !ex.failed ? (ex.items || []).slice(0, MAX_ITEMS).map((i) => ({
      name: cleanText(i.name, 100), qty: Number(i.qty), unit_price: Math.round(Number(i.unit_price)), category_id: hints.get(i.category_hint) || null,
    })).filter((i) => i.name && i.qty > 0 && i.qty <= 100000 && i.unit_price >= 0 && i.unit_price <= MAX_AMOUNT) : [];
    const ok = !!(ex && !ex.failed && items.length);
    const r = {
      id, business_id: bid, status: ok ? 'needs_review' : 'failed', engine: (ex && ex.engine) || 'offline',
      merchant: ok ? cleanText(ex.merchant, 80) : '', receipt_date: ok && /^\d{4}-\d{2}-\d{2}$/.test(ex.date || '') ? ex.date : null,
      confidence: ok ? Math.min(1, Math.max(0, Number(ex.confidence) || 0)) : 0, items,
      total: items.reduce((s, i) => s + lineTotal(i.qty, i.unit_price), 0), created_at: new Date().toISOString(),
    };
    await V().putImage(id, img.bytes);
    await V().commit([{ type: 'receipt', id, obj: r }]);
    S().receipts.push(r);
    return receiptApi(r);
  }

  function findReceipt(bid, rid) {
    biz(bid);
    const r = S().receipts.find((x) => x.id === rid && x.business_id === bid);
    if (!r) throw notFound('nota tidak ditemukan');
    return r;
  }

  async function receiptImage(bid, rid) {
    findReceipt(bid, rid);
    const bytes = await V().getImage(rid);
    if (!bytes) throw notFound('foto nota tidak ditemukan');
    return new Blob([bytes], { type: sniff(bytes) || 'image/jpeg' });
  }

  async function receiptConfirm(bid, rid, body) {
    const r = findReceipt(bid, rid);
    strict(body, ['wallet', 'merchant', 'date', 'items']);
    const wallet = body.wallet || 'business';
    if (wallet !== 'business' && wallet !== 'personal') throw bad("dompet harus 'business' atau 'personal'");
    if (!Array.isArray(body.items) || body.items.length < 1 || body.items.length > MAX_ITEMS) throw bad('jumlah item harus 1-50');
    if (runes(body.merchant) > 80) throw bad('nama toko terlalu panjang');
    if (r.status !== 'needs_review' && r.status !== 'failed') throw new ApiError('nota ini sudah dikonfirmasi atau belum siap ditinjau', 409);
    const fallback = catByName(wallet === 'personal' ? 'Lain-lain (pribadi)' : 'Lain-lain (usaha)').id;

    let total = 0;
    const items = body.items.map((it) => {
      const name = cleanText(it.name, 100);
      if (!name) throw bad('nama item wajib diisi');
      if (!(it.qty > 0) || it.qty > 100000 || Math.abs(it.qty * 1000 - Math.round(it.qty * 1000)) > 1e-9) throw bad('jumlah item tidak valid (maksimal 3 desimal)');
      if (!Number.isInteger(it.unit_price) || it.unit_price < 0 || it.unit_price > MAX_AMOUNT) throw bad('harga item tidak valid');
      checkOptUUID(it.category_id, 'category_id tidak valid');
      let cat = fallback;
      if (it.category_id) {
        const c = catById(it.category_id);
        if (!c || c.kind !== 'expense' || (c.scope !== 'any' && c.scope !== wallet)) throw bad('kategori tidak cocok dengan dompet tujuan');
        cat = c.id;
      }
      total += lineTotal(it.qty, it.unit_price);
      if (total > MAX_AMOUNT) throw bad('total nota melebihi batas');
      return { name, qty: it.qty, unit_price: it.unit_price, category_id: cat };
    });
    if (total < 1) throw bad('total nota harus lebih dari nol');

    const now = nowMs();
    let occurred = new Date(now);
    if (body.date) {
      if (!/^\d{4}-\d{2}-\d{2}$/.test(body.date) || Number.isNaN(wibStart(body.date).getTime())) throw bad('format tanggal harus YYYY-MM-DD');
      occurred = new Date(wibStart(body.date).getTime() + 5 * 3600000); // 12:00 WIB
      if (!dateOk(occurred.toISOString(), now)) throw bad('tanggal nota di luar rentang yang diizinkan');
    }
    const per = new Map();
    for (const it of items) per.set(it.category_id, (per.get(it.category_id) || 0) + lineTotal(it.qty, it.unit_price));
    const merchant = cleanText(body.merchant, 80);
    const created = [...per.keys()].sort().filter((c) => per.get(c) >= 1).map((c) => ({
      id: uuid(), business_id: bid, wallet, kind: 'expense', direction: 'out', amount: per.get(c), category_id: c,
      note: merchant || 'Nota belanja', source: 'ocr', client_id: null, occurred_at: occurred.toISOString(),
      created_at: new Date(now).toISOString(), deleted_at: null, transfer_group_id: null, receipt_id: rid,
    }));
    const next = { ...r, status: 'confirmed', merchant, receipt_date: body.date || r.receipt_date, items, total, confirmed_at: new Date(now).toISOString() };
    await V().commit([{ type: 'receipt', id: rid, obj: next }, ...created.map((t) => ({ type: 'tx', id: t.id, obj: t }))]);
    Object.assign(r, next);
    S().transactions.push(...created);
    return { receipt_id: rid, total, transactions: created.map((t) => ({ id: t.id, category_id: t.category_id, amount: t.amount })) };
  }

  // ---------- pengaturan (terenkripsi bersama data) ----------
  async function setSettings(patch) {
    const next = { ...S().settings, ...patch };
    await V().commit([{ type: 'settings', id: 'main', obj: next }]);
    V().state.settings = next;
    return next;
  }

  // ---------- pintu masuk: meniru REST ----------
  async function api(method, path, body, opts = {}) {
    const url = new URL(path, 'http://local');
    const p = url.pathname, q = url.searchParams;
    if (method === 'GET' && p === '/me') return me();
    if (p === '/businesses') {
      if (method === 'GET') return { businesses: S().businesses.slice().sort((a, b) => a.created_at.localeCompare(b.created_at)) };
      if (method === 'POST') return createBusiness(body);
    }
    const m = p.match(/^\/businesses\/([^/]+)\/(.+)$/);
    if (m) {
      const bid = m[1], rest = m[2];
      let r;
      if (method === 'GET' && rest === 'wallets') return wallets(bid);
      if (method === 'GET' && rest === 'categories') { biz(bid); return { categories: CATEGORIES }; }
      if (rest === 'transactions') {
        if (method === 'GET') return listTransactions(bid, q);
        if (method === 'POST') return (await addTransaction(bid, body)).tx;
      }
      if (method === 'POST' && rest === 'transfers') { const t = await transfer(bid, body); return { transactions: t.transactions }; }
      if ((r = rest.match(/^transactions\/([^/]+)$/))) {
        if (method === 'PUT') return updateTransaction(bid, r[1], body);
        if (method === 'DELETE') return deleteTransaction(bid, r[1]);
      }
      if (method === 'GET' && rest === 'reports/monthly') return monthly(bid, q.get('month'));
      if (method === 'GET' && rest === 'reports/daily') return daily(bid, parseInt(q.get('days') || '7', 10));
      if (method === 'GET' && rest === 'reports/categories') return categoryBreakdown(bid, q.get('month'));
      if (method === 'POST' && rest === 'receipts') return receiptUpload(bid, body);
      if ((r = rest.match(/^receipts\/([^/]+)$/)) && method === 'GET') return receiptApi(findReceipt(bid, r[1]));
      if ((r = rest.match(/^receipts\/([^/]+)\/image$/)) && method === 'GET') return receiptImage(bid, r[1]);
      if ((r = rest.match(/^receipts\/([^/]+)\/confirm$/)) && method === 'POST') return receiptConfirm(bid, r[1], body);
    }
    throw notFound('endpoint tidak ditemukan');
  }

  L.api = api;
  L.ApiError = ApiError;
  L.ledger = { CATEGORIES, wibDay, setSettings, monthly, categoryBreakdown, active, txApi, uuid };
})(typeof globalThis !== 'undefined' ? globalThis : window);
