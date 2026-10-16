require('fake-indexeddb/auto');
const { JSDOM } = require('jsdom');
const { webcrypto } = require('node:crypto');
const BASE = process.env.BASE_URL || 'http://127.0.0.1:8080'; // server MODE PERANGKAT
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let fails = 0;
const check = (name, ok, extra = '') => { console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok ? '' : '  -> ' + String(extra).slice(0, 300))); if (!ok) fails++; };
const ocrOk = { engine: 'mock', failed: false, merchant: 'Toko Sumber Rejeki', date: '', confidence: 0.94, items: [
  { name: 'Beras 25 kg', qty: 1, unit_price: 310000, category_hint: 'Bahan baku' }, { name: 'Plastik kemasan', qty: 2, unit_price: 11000, category_hint: 'Kemasan' }] };

async function openApp({ withCrypto = true } = {}) {
  const html = await (await fetch(BASE + '/')).text();
  const calls = [], downloads = [], blobs = new Map();
  const dom = new JSDOM(html, {
    url: BASE + '/', runScripts: 'dangerously', resources: 'usable', pretendToBeVisual: true,
    beforeParse(w) {
      w.fetch = async (u, o) => {
        const url = String(u); calls.push(url);
        if (url === '/v1/ocr/extract') return new Response(JSON.stringify(ocrOk), { status: 200 }); // gerbang OCR diuji di tes Go
        return fetch(new URL(url, BASE), o);
      };
      if (withCrypto) Object.defineProperty(w, 'crypto', { value: webcrypto, configurable: true });
      else Object.defineProperty(w, 'crypto', { value: { getRandomValues: webcrypto.getRandomValues.bind(webcrypto) }, configurable: true });
      w.indexedDB = indexedDB; w.IDBKeyRange = IDBKeyRange;
      // gunakan kelas realm utama agar kompatibel dengan WebCrypto/IndexedDB tiruan
      for (const k of ['Uint8Array', 'ArrayBuffer', 'TextEncoder', 'TextDecoder', 'Blob', 'File', 'FormData', 'Response']) w[k] = globalThis[k];
      w.confirm = () => true; w.open = () => {};
      let n = 0;
      w.URL.createObjectURL = (b) => { const id = 'blob:test/' + (++n); blobs.set(id, b); return id; };
      w.URL.revokeObjectURL = () => {};
      w.HTMLAnchorElement.prototype.click = function () { downloads.push({ name: this.download, blob: blobs.get(this.href) }); };
    },
  });
  const w = dom.window, d = w.document, errors = [];
  w.addEventListener('error', (e) => errors.push(e.message));
  for (let i = 0; i < 80 && d.querySelector('[v-cloak]'); i++) await sleep(100);
  const T = {
    w, d, calls, downloads, errors,
    text: () => d.body.textContent.replace(/\s+/g, ' '),
    waitFor: async (pred, ms = 8000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { if (pred()) return true; } catch {} await sleep(50); } return false; },
    set: (el, v) => { el.value = v; el.dispatchEvent(new w.Event('input', { bubbles: true })); el.dispatchEvent(new w.Event('change', { bubbles: true })); },
    click: (el) => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true })),
    submit: (f) => f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true })),
    btn: (label, root = d) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim().startsWith(label)),
    inp: (label, root = d) => [...root.querySelectorAll('label')].find((l) => l.textContent.trim().startsWith(label)).querySelector('input,select'),
    li: (needle) => [...d.querySelectorAll('.list li')].find((l) => l.textContent.includes(needle)),
    setFile: (input, file) => { Object.defineProperty(input, 'files', { value: [file], configurable: true }); input.dispatchEvent(new w.Event('change', { bubbles: true })); },
    idle: () => T.waitFor(() => ![...d.querySelectorAll('button')].some((b) => b.disabled && /Buat brankas|Lanjut|Simpan|Pindahkan|Buka kunci|Pulihkan|Ganti PIN|Mulai|Unduh/.test(b.textContent)), 5000),
  };
  await T.waitFor(() => !T.text().includes('Memuat...'), 8000); // boot async: tunggu config + status brankas
  return T;
}

(async () => {
  const T = await openApp();
  const { w, d, text, waitFor, set, click, submit, btn, inp, li, idle } = T;
  check('UI termuat tanpa error JS; mode device dari /v1/config', T.errors.length === 0 && w.localStorage.getItem('ledger.mode') === 'device', T.errors.join(';'));

  // ===== onboarding =====
  check('pengguna baru melihat pembuatan brankas (bukan login/daftar server)', text().includes('terenkripsi di HP ini saja') && !text().includes('Nomor HP'));
  const ob = () => d.querySelector('section.card form');
  for (const [pin, pin2, expect, label] of [['123456', '123456', 'mudah ditebak', 'PIN umum ditolak'], ['abc', 'abc', 'Minimal 6', 'PIN pendek ditolak'], ['Kopi-Susu-2026!', 'Kopi-Susu-2027!', 'tidak sama', 'konfirmasi beda ditolak']]) {
    set(inp('PIN atau kata sandi', ob()), pin); set(inp('Ulangi PIN', ob()), pin2); submit(ob());
    check(label, await waitFor(() => text().includes(expect)) && text().includes('terenkripsi di HP ini saja'), text().slice(0, 200));
    await idle();
  }
  set(inp('Nama panggilan', ob()), 'Bu Sari'); set(inp('PIN atau kata sandi', ob()), 'Kopi-Susu-2026!'); set(inp('Ulangi PIN', ob()), 'Kopi-Susu-2026!');
  check('indikator kekuatan PIN muncul', text().includes('Kuat') || text().includes('Baik'));
  submit(ob());
  check('kode pemulihan ditampilkan', await waitFor(() => !!d.querySelector('.codebox')), text().slice(0, 200));
  const recovery = d.querySelector('.codebox').textContent.trim();
  check('format kode pemulihan', /^([A-Z2-7]{4}-){7}[A-Z2-7]{4}$/.test(recovery), recovery);
  check('tombol Lanjut nonaktif sebelum kode disimpan', btn('Lanjut').disabled);
  click(d.querySelector('label.check input')); d.querySelector('label.check input').dispatchEvent(new w.Event('change', { bubbles: true }));
  await new Promise((r) => setTimeout(r, 150));
  click(btn('Lanjut'));
  check('lanjut ke pembuatan usaha', await waitFor(() => text().includes('Usaha kamu')), text().slice(0, 200));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  set(d.querySelector('input[placeholder="Warung Bu Sari"]'), 'Warung Bu Sari'); submit(d.querySelector('form'));
  check('usaha dibuat di perangkat', await waitFor(() => text().includes('Halo, Bu Sari') && text().includes('Saldo dompet usaha')), text().slice(0, 200));
  await idle(); await new Promise((r) => setTimeout(r, 300));

  // ===== transaksi =====
  click(btn('Catat')); await new Promise((r) => setTimeout(r, 150));
  const txForm = d.querySelectorAll('form.card')[0], trForm = d.querySelectorAll('form.card')[1];
  set(txForm.querySelector('input.amount'), '9800000'); submit(txForm);
  check('pemasukan tersimpan lokal', await waitFor(() => text().includes('Transaksi tersimpan')));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  click([...txForm.querySelectorAll('.seg button')].find((b) => b.textContent.includes('Pengeluaran'))); await new Promise((r) => setTimeout(r, 100));
  set(txForm.querySelector('input.amount'), '7800000');
  const catSel = txForm.querySelectorAll('select')[1]; set(catSel, [...catSel.options].find((o) => o.textContent.trim() === 'Bahan baku').value);
  submit(txForm); await new Promise((r) => setTimeout(r, 700)); await idle();
  set(trForm.querySelector('input.amount'), '2450000'); submit(trForm);
  check('transfer prive tersimpan', await waitFor(() => text().includes('Uang dipindahkan')));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  click(btn('Beranda'));
  check('peringatan kebocoran kas (dihitung di perangkat)', await waitFor(() => text().includes('Kas usaha bocor')));
  check('saldo usaha -Rp 450.000', /-450\.000/.test(text()));
  check('riwayat per hari + grafik 7 hari', text().includes('Hari ini') && d.querySelectorAll('.week .day').length === 7);
  click(btn('Sembunyikan')); check('saldo bisa disembunyikan', await waitFor(() => text().includes('Rp ••••••'))); click(btn('Tampilkan'));

  // ===== edit & undo =====
  click(li('Pemasukan')); await waitFor(() => !!d.querySelector('.sheet'));
  click(btn('Ubah', d.querySelector('.sheet'))); await new Promise((r) => setTimeout(r, 100));
  set(d.querySelector('.sheet form input[maxlength="280"]'), 'jual pagi'); submit(d.querySelector('.sheet form'));
  check('edit catatan lokal', await waitFor(() => !!li('jual pagi')));
  click(li('Ambil pribadi')); await waitFor(() => !!d.querySelector('.sheet')); click(btn('Hapus', d.querySelector('.sheet')));
  check('hapus + Urungkan muncul', await waitFor(() => !li('Ambil pribadi') && !!btn('Urungkan')));
  click(btn('Urungkan')); check('urungkan memulihkan baris', await waitFor(() => !!li('Ambil pribadi')));

  // ===== scan nota (OCR via gerbang, foto tetap di perangkat) =====
  click(btn('Scan')); await new Promise((r) => setTimeout(r, 150));
  T.setFile(d.querySelector('input[type=file]'), new File([Uint8Array.from([0xff, 0xd8, 0xff, 0xe0, ...Array(400).fill(9)])], 'nota.jpg', { type: 'image/jpeg' }));
  check('hasil scan tampil + peringatan mode uji', await waitFor(() => text().includes('Periksa hasil scan') && text().includes('Mode uji lokal')), text().slice(0, 300));
  check('foto nota dimuat dari penyimpanan lokal', await waitFor(() => !!d.querySelector('img.receipt-img')));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  submit(d.querySelector('section.card form'));
  check('konfirmasi nota membuat pengeluaran', await waitFor(() => text().includes('Tersimpan: Rp 332.000')), text().slice(0, 300));

  // ===== cadangan =====
  await new Promise((r) => setTimeout(r, 300));
  check('pengingat cadangan muncul di beranda', await waitFor(() => text().includes('Cadangkan sekarang')));
  click(btn('Lainnya'));
  check('halaman Lainnya menjelaskan lokasi data', await waitFor(() => text().includes('Di mana datamu?') && text().includes('foto nota')));
  click(btn('Unduh cadangan terenkripsi'));
  check('cadangan diunduh', await waitFor(() => T.downloads.some((x) => /^bukukas-cadangan-.*\.bkp$/.test(x.name))), JSON.stringify(T.downloads.map((x) => x.name)));
  const bkp = T.downloads.find((x) => x.name.endsWith('.bkp')); const bkpText = await bkp.blob.text();
  check('cadangan tidak memuat plaintext', !/Warung|Bu Sari|jual pagi|Toko Sumber|9800000/.test(bkpText) && bkpText.includes('bukukas-backup'));
  check('penghitung perubahan direset', await waitFor(() => text().includes('Perubahan sejak itu: 0')));

  // ===== kunci, PIN salah, ganti PIN, lupa PIN =====
  click(btn('Kunci', d.querySelector('.top-actions')));
  check('terkunci: data hilang dari layar', await waitFor(() => text().includes('Buku Kas terkunci') && !text().includes('Warung Bu Sari') && !/Rp 9\.800\.000/.test(text())));
  set(inp('PIN', d.querySelector('.lock form')), 'salah-salah'); submit(d.querySelector('.lock form'));
  check('PIN salah ditolak', await waitFor(() => text().includes('PIN salah')) && text().includes('Buku Kas terkunci'));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  set(inp('PIN', d.querySelector('.lock form')), 'Kopi-Susu-2026!'); submit(d.querySelector('.lock form'));
  check('PIN benar membuka data lagi', await waitFor(() => text().includes('Halo, Bu Sari') && !text().includes('Buku Kas terkunci')), text().slice(0, 200));
  await idle(); await new Promise((r) => setTimeout(r, 300));

  click(btn('Lainnya')); await waitFor(() => text().includes('Ganti PIN'));
  const pf = [...d.querySelectorAll('form')].find((f) => f.textContent.includes('PIN lama'));
  set(inp('PIN lama', pf), 'Kopi-Susu-2026!'); set(inp('PIN baru', pf), 'Teh-Manis-Dingin-77'); set(inp('Ulangi PIN baru', pf), 'Teh-Manis-Dingin-77'); submit(pf);
  check('ganti PIN berhasil', await waitFor(() => text().includes('PIN berhasil diganti')));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  click(btn('Kunci', d.querySelector('.top-actions'))); await waitFor(() => text().includes('Buku Kas terkunci'));
  set(inp('PIN', d.querySelector('.lock form')), 'Kopi-Susu-2026!'); submit(d.querySelector('.lock form'));
  check('PIN lama tidak berlaku lagi', await waitFor(() => text().includes('PIN salah')));
  await idle();
  // lupa PIN -> kode pemulihan + PIN baru
  click(btn('Lupa PIN')); await new Promise((r) => setTimeout(r, 150));
  const lf = d.querySelector('.lock form');
  set(inp('Kode pemulihan', lf), recovery.toLowerCase()); set(inp('PIN baru', lf), 'Susu-Jahe-Hangat-55'); set(inp('Ulangi PIN baru', lf), 'Susu-Jahe-Hangat-55'); submit(lf);
  check('kode pemulihan membuka akses & memasang PIN baru', await waitFor(() => text().includes('Halo, Bu Sari')), text().slice(0, 250));

  // ===== kunci otomatis saat tidak aktif =====
  await idle(); await new Promise((r) => setTimeout(r, 400));
  click(btn('Lainnya')); await waitFor(() => text().includes('Kunci otomatis'));
  set(inp('Kunci otomatis', d), '1');
  await waitFor(() => text().includes('Pengaturan kunci otomatis disimpan'));
  const realNow = w.Date.now; w.Date.now = () => realNow() + 2 * 60000; // maju 2 menit tanpa aktivitas
  d.dispatchEvent(new w.Event('visibilitychange'));
  check('terkunci otomatis setelah tidak aktif', await waitFor(() => text().includes('Buku Kas terkunci') && !text().includes('Warung Bu Sari')));
  w.Date.now = realNow;
  set(inp('PIN', d.querySelector('.lock form')), 'Susu-Jahe-Hangat-55'); submit(d.querySelector('.lock form'));
  await waitFor(() => text().includes('Halo, Bu Sari')); await idle(); await new Promise((r) => setTimeout(r, 400));

  // ===== hapus semua, lalu pulihkan dari cadangan (PIN saat cadangan dibuat) =====
  click(btn('Lainnya')); await waitFor(() => text().includes('Zona bahaya'));
  check('tombol hapus nonaktif sebelum konfirmasi', btn('Hapus semua data').disabled);
  set(inp('Ketik HAPUS', d), 'HAPUS'); await new Promise((r) => setTimeout(r, 100));
  click(btn('Hapus semua data'));
  check('semua data terhapus -> kembali ke onboarding', await waitFor(() => text().includes('terenkripsi di HP ini saja')), text().slice(0, 200));
  click(btn('Sudah punya cadangan'));
  check('layar pemulihan', await waitFor(() => text().includes('Pulihkan dari cadangan')));
  T.setFile(d.querySelector('section input[type=file]'), new File([bkpText], 'cadangan.bkp'));
  check('cadangan valid dikenali', await waitFor(() => text().includes('Cadangan valid')), text().slice(0, 200));
  const rf = d.querySelector('section form');
  set(inp('PIN saat cadangan', rf), 'pin-yang-salah'); submit(rf);
  check('PIN cadangan salah ditolak', await waitFor(() => text().includes('PIN salah')) && text().includes('Cadangan valid'));
  await idle(); await new Promise((r) => setTimeout(r, 300));
  set(inp('PIN saat cadangan', rf), 'Kopi-Susu-2026!'); submit(rf);
  check('pemulihan berhasil: data kembali', await waitFor(() => text().includes('Halo, Bu Sari')), 'TOAST=' + (d.querySelector('.toast') ? d.querySelector('.toast').textContent.trim() : 'none') + ' | errors=' + T.errors.join(';'));
  check('saldo & riwayat pulih', await waitFor(() => /-782\.000/.test(text()) && !!li('Ambil pribadi') && !!li('Toko Sumber Rejeki')), text().slice(0, 300));

  // ===== bukti: tidak ada data keuangan yang dikirim ke server =====
  const bad = T.calls.filter((u) => !['/v1/config', '/v1/ocr/extract'].includes(u));
  check('HANYA /v1/config dan /v1/ocr/extract yang dipanggil (tidak ada endpoint data)', bad.length === 0, bad.join(', '));
  check('tidak ada error JS selama alur', T.errors.length === 0, T.errors.join('; '));

  // ===== lingkungan tanpa WebCrypto (mis. HTTP non-localhost) =====
  const T2 = await openApp({ withCrypto: false });
  check('tanpa WebCrypto: pesan jelas, bukan crash', await T2.waitFor(() => T2.text().includes('Tidak bisa dibuka') && T2.text().includes('HTTPS')), T2.text().slice(0, 200));

  console.log(fails ? `\n${fails} GAGAL` : '\nSEMUA PASS');
  process.exit(fails ? 1 : 0);
})().catch((e) => { console.error('CRASH', e); process.exit(2); });
