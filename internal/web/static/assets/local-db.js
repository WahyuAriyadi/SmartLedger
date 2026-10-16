// Penyimpanan di perangkat: IndexedDB berisi HANYA ciphertext. Satu-satunya data terbaca adalah metadata kunci
// (garam, parameter KDF, DEK yang sudah dibungkus) dan penghitung pengunci; tanpa PIN/kode pemulihan semuanya tak berguna.
(function (g) {
  'use strict';
  const L = (g.LedgerLocal = g.LedgerLocal || {});
  const C = () => L.crypto;
  const DB_NAME = 'bukukas', DB_VERSION = 1;
  let dbp = null;

  function openDb() {
    if (dbp) return dbp;
    dbp = new Promise((resolve, reject) => {
      if (!g.indexedDB) return reject(new Error('Browser ini tidak mendukung penyimpanan lokal (IndexedDB).'));
      const req = g.indexedDB.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = () => {
        const db = req.result;
        db.createObjectStore('meta', { keyPath: 'k' });
        db.createObjectStore('records', { keyPath: 'id' }); // { id: "<jenis>:<uuid>", iv, ct }
        db.createObjectStore('images', { keyPath: 'id' });  // { id, iv, ct }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error || new Error('Gagal membuka penyimpanan lokal.'));
      req.onblocked = () => reject(new Error('Penyimpanan lokal terblokir oleh tab lain. Tutup tab lain lalu coba lagi.'));
    });
    dbp.catch(() => { dbp = null; });
    return dbp;
  }

  const wrapReq = (r) => new Promise((res, rej) => { r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });

  // Semua penulisan dalam satu transaksi IndexedDB = atomik (transfer dua baris tidak mungkin setengah tersimpan).
  async function tx(stores, mode, fn) {
    const db = await openDb();
    return new Promise((resolve, reject) => {
      const t = db.transaction(stores, mode);
      let result;
      t.oncomplete = () => resolve(result);
      t.onerror = () => reject(t.error || new Error('Transaksi penyimpanan gagal.'));
      t.onabort = () => reject(t.error || new Error('Penyimpanan dibatalkan (ruang penuh?).'));
      Promise.resolve(fn(...stores.map((s) => t.objectStore(s)))).then((r) => { result = r; }, (e) => { try { t.abort(); } catch { /* sudah selesai */ } reject(e); });
    });
  }

  const getMeta = async () => (await tx(['meta'], 'readonly', (m) => wrapReq(m.get('main')))) || null;
  const putMeta = (obj) => tx(['meta'], 'readwrite', (m) => wrapReq(m.put({ k: 'main', ...obj })));
  const hasVault = async () => !!(await getMeta());

  // ---------- brankas dalam memori ----------
  // `state` hanya ada saat terbuka; lock() menghapus kunci dan seluruh data terdekripsi dari memori.
  const V = { dek: null, state: null, meta: null };

  const emptyState = () => ({ settings: {}, businesses: [], transactions: [], receipts: [] });
  const TYPES = { b: 'business', t: 'tx', r: 'receipt', s: 'settings' };

  async function loadAll() {
    const dek = V.dek;
    const rows = await tx(['records'], 'readonly', (r) => wrapReq(r.getAll()));
    const st = emptyState();
    for (const row of rows) {
      const obj = await C().openJSON(dek, row.id, row);
      const type = row.id.slice(0, row.id.indexOf(':'));
      if (type === 'business') st.businesses.push(obj);
      else if (type === 'tx') st.transactions.push(obj);
      else if (type === 'receipt') st.receipts.push(obj);
      else if (type === 'settings') st.settings = obj;
    }
    V.state = st;
  }

  async function create(pin, opts = {}) {
    const { meta, dek, recoveryCode } = await C().createVault(pin, opts);
    meta.createdAt = new Date().toISOString();
    meta.counter = { changes: 0, lastBackupAt: null };
    meta.lock = { fails: 0, nextTry: 0 };
    await putMeta(meta);
    V.meta = meta; V.dek = dek; V.state = emptyState();
    return recoveryCode;
  }

  // Pembatas percobaan lokal: hanya penghalang bagi orang iseng. Perlindungan sebenarnya terhadap serangan offline
  // adalah biaya PBKDF2 + panjang PIN, bukan penghitung ini (penyerang yang memegang berkas bisa melewatinya).
  const delayFor = (fails) => (fails < 3 ? 0 : Math.min(300000, 1000 * 2 ** (fails - 2)));

  async function unlock(credential) {
    const meta = await getMeta();
    if (!meta) throw new Error('Belum ada data di perangkat ini.');
    const now = Date.now();
    if (meta.lock && meta.lock.nextTry > now) {
      throw new Error(`Terlalu banyak percobaan. Coba lagi dalam ${Math.ceil((meta.lock.nextTry - now) / 1000)} detik.`);
    }
    let dek;
    try {
      dek = credential.recovery != null ? await C().openWithRecovery(meta, credential.recovery) : await C().openWithPin(meta, credential.pin);
    } catch (e) {
      if (/HTTPS|localhost/.test(e.message)) throw e;
      const fails = ((meta.lock && meta.lock.fails) || 0) + 1;
      meta.lock = { fails, nextTry: Date.now() + delayFor(fails) };
      await putMeta(meta);
      const wait = delayFor(fails);
      throw new Error(e.message + (wait ? ` Tunggu ${Math.ceil(wait / 1000)} detik sebelum mencoba lagi.` : ''));
    }
    meta.lock = { fails: 0, nextTry: 0 };
    await putMeta(meta);
    V.meta = meta; V.dek = dek;
    await loadAll();
  }

  function lock() { V.dek = null; V.state = null; V.meta = null; }
  const isOpen = () => !!(V.dek && V.state);

  // Simpan/hapus rekaman dalam SATU transaksi. `puts`: [{type,id,obj}], `dels`: ["tx:<id>", ...].
  async function commit(puts, dels = []) {
    if (!isOpen()) throw new Error('Aplikasi terkunci.');
    const sealed = [];
    for (const p of puts) {
      const id = `${p.type}:${p.id}`;
      sealed.push({ id, ...(await C().sealJSON(V.dek, id, p.obj)) });
    }
    V.meta.counter.changes += puts.length + dels.length;
    await tx(['records', 'meta'], 'readwrite', (rec, meta) => {
      for (const s of sealed) rec.put(s);
      for (const d of dels) rec.delete(d);
      meta.put({ k: 'main', ...V.meta });
    });
  }

  async function putImage(id, bytes) {
    // Enkripsi DULU, baru buka transaksi: transaksi IndexedDB menutup sendiri bila menunggu sesuatu selain request IDB.
    const sealed = await C().seal(V.dek, `image:${id}`, bytes);
    await tx(['images'], 'readwrite', (im) => wrapReq(im.put({ id, ...sealed })));
  }
  async function getImage(id) {
    const row = await tx(['images'], 'readonly', (im) => wrapReq(im.get(id)));
    return row ? C().open(V.dek, `image:${id}`, row) : null;
  }

  async function changePin(credential, newPin) {
    const meta = await getMeta();
    const next = await C().rewrapPin(meta, credential, newPin);
    next.counter = meta.counter; next.lock = { fails: 0, nextTry: 0 }; next.createdAt = meta.createdAt;
    await putMeta(next);
    if (V.meta) V.meta = next;
  }
  async function rotateRecovery(pin) {
    const meta = await getMeta();
    const r = await C().rotateRecovery(meta, pin);
    r.meta.counter = meta.counter; r.meta.lock = meta.lock; r.meta.createdAt = meta.createdAt;
    await putMeta(r.meta);
    if (V.meta) V.meta = r.meta;
    return r.recoveryCode;
  }

  async function markBackup() {
    V.meta.counter = { changes: 0, lastBackupAt: new Date().toISOString() };
    await putMeta(V.meta);
  }

  async function wipe() {
    lock();
    const db = await openDb();
    await new Promise((res, rej) => {
      const t = db.transaction(['meta', 'records', 'images'], 'readwrite');
      for (const s of ['meta', 'records', 'images']) t.objectStore(s).clear();
      t.oncomplete = res; t.onerror = () => rej(t.error);
    });
  }

  // Dipakai pemulihan cadangan: ganti seluruh isi dengan data cadangan (ciphertext apa adanya).
  async function replaceAll(meta, records, images) {
    const db = await openDb();
    await new Promise((res, rej) => {
      const t = db.transaction(['meta', 'records', 'images'], 'readwrite');
      t.objectStore('meta').clear(); t.objectStore('records').clear(); t.objectStore('images').clear();
      t.objectStore('meta').put({ k: 'main', ...meta });
      for (const r of records) t.objectStore('records').put(r);
      for (const i of images) t.objectStore('images').put(i);
      t.oncomplete = res; t.onerror = () => rej(t.error); t.onabort = () => rej(t.error);
    });
  }
  const rawDump = async () => ({
    meta: await getMeta(),
    records: await tx(['records'], 'readonly', (r) => wrapReq(r.getAll())),
    images: await tx(['images'], 'readonly', (r) => wrapReq(r.getAll())),
  });

  L.vault = {
    V, hasVault, getMeta, create, unlock, lock, isOpen, commit, putImage, getImage,
    changePin, rotateRecovery, markBackup, wipe, replaceAll, rawDump, loadAll, delayFor,
    get state() { return V.state; }, get meta() { return V.meta; }, get dek() { return V.dek; },
  };
  L.TYPES = TYPES;
})(typeof globalThis !== 'undefined' ? globalThis : window);
