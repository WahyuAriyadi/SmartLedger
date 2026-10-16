const { JSDOM } = require('jsdom');
const BASE = process.env.BASE_URL || 'http://127.0.0.1:8080'; // server MODE SERVER (docker-compose.server.yml)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let fails = 0;
const check = (name, ok, extra = '') => { console.log((ok ? 'PASS ' : 'FAIL ') + name + (ok ? '' : '  -> ' + String(extra).slice(0, 300))); if (!ok) fails++; };

(async () => {
  const html = await (await fetch(BASE + '/')).text();
  let opened = null;
  const dom = new JSDOM(html, {
    url: BASE + '/', runScripts: 'dangerously', resources: 'usable', pretendToBeVisual: true,
    beforeParse(w) { w.fetch = (u, o) => fetch(new URL(u, BASE), o); w.confirm = () => true; w.open = (u) => { opened = u; }; w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder; },
  });
  const w = dom.window, d = w.document;
  const errors = [];
  w.addEventListener('error', (e) => errors.push(e.message));
  for (let i = 0; i < 50 && d.querySelector('[v-cloak]'); i++) await sleep(100);
  const text = () => d.body.textContent.replace(/\s+/g, ' ');
  const waitFor = async (pred, ms = 6000) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { if (pred()) return true; } catch {} await sleep(50); } return false; };
  const set = (el, v) => { el.value = v; el.dispatchEvent(new w.Event('input', { bubbles: true })); el.dispatchEvent(new w.Event('change', { bubbles: true })); };
  const click = (el) => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  const submit = (f) => f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  const btn = (label, root = d) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim().startsWith(label));
  const li = (needle) => [...d.querySelectorAll('.list li')].find((l) => l.textContent.includes(needle));
  const idle = () => waitFor(() => ![...d.querySelectorAll('button')].some((b) => b.disabled && /Simpan|Pindahkan|Masuk|Daftar|Mulai|Buka kunci/.test(b.textContent)), 3000);

  await waitFor(() => !text().includes('Memuat...'), 8000); // boot async (config + status)
  check('UI termuat tanpa error JS', errors.length === 0, errors.join('; '));

  // --- daftar + usaha ---
  click(btn('Belum punya akun')); await sleep(100);
  const phone = '08' + String(Math.floor(Math.random() * 1e10)).padStart(10, '0');
  const ai = d.querySelectorAll('.auth input');
  set(ai[0], 'Bu Sari'); set(ai[1], phone); set(ai[2], '482913');
  submit(d.querySelector('.auth form'));
  check('daftar lalu masuk', await waitFor(() => text().includes('Usaha kamu')));
  await idle(); await sleep(200);
  set(d.querySelector('input[placeholder="Warung Bu Sari"]'), 'Warung Bu Sari');
  submit(d.querySelector('form'));
  check('usaha dibuat', await waitFor(() => text().includes('Halo, Bu Sari')));
  check('saldo awal Rp 0 + ringkasan hari ini', await waitFor(() => text().includes('Rp 0') && text().includes('Masuk hari ini')));
  check('grafik 7 hari tampil', d.querySelectorAll('.week .day').length === 7);
  await idle(); await sleep(300);

  // --- input nominal berformat + tombol cepat ---
  click(btn('Catat')); await sleep(150);
  const txForm = d.querySelectorAll('form.card')[0];
  const amt = txForm.querySelector('input.amount');
  set(amt, '9800000');
  check('nominal diformat 9.800.000', amt.value === '9.800.000', amt.value);
  set(amt, '9.8x00.0a00'); // karakter non-angka dibuang
  check('karakter non-angka dibuang', amt.value === '9.800.000', amt.value);
  const trForm = d.querySelectorAll('form.card')[1];
  click(btn('+50rb', trForm)); click(btn('+50rb', trForm)); await sleep(100);
  check('tombol cepat menambah nominal', trForm.querySelector('input.amount').value === '100.000', trForm.querySelector('input.amount').value);
  click(btn('Hapus', trForm)); await sleep(100);
  check('tombol Hapus mengosongkan nominal', trForm.querySelector('input.amount').value === '');
  check('tombol Simpan nonaktif tanpa nominal', btn('Pindahkan', trForm).disabled);

  submit(txForm);
  check('pemasukan tersimpan', await waitFor(() => text().includes('Transaksi tersimpan')));
  await idle(); await sleep(300);
  click([...txForm.querySelectorAll('.seg button')].find((b) => b.textContent.includes('Pengeluaran'))); await sleep(100);
  set(txForm.querySelector('input.amount'), '7800000');
  const catSel = txForm.querySelectorAll('select')[1];
  const bahan = [...catSel.options].find((o) => o.textContent.trim() === 'Bahan baku');
  set(catSel, bahan.value); await sleep(50);
  submit(txForm); await sleep(700); await idle();
  set(trForm.querySelector('input.amount'), '2450000');
  submit(trForm);
  check('transfer prive tersimpan', await waitFor(() => text().includes('Uang dipindahkan')));
  await idle(); await sleep(300);

  // --- beranda ---
  click(btn('Beranda'));
  check('peringatan kebocoran kas', await waitFor(() => text().includes('Kas usaha bocor')));
  check('riwayat dikelompokkan per hari (Hari ini)', await waitFor(() => text().includes('Hari ini') && d.querySelectorAll('.group').length >= 1));
  check('saldo usaha -Rp 450.000', /-450\.000/.test(text()), text().slice(0, 300));
  check('ringkasan hari ini terisi', text().includes('Masuk hari ini Rp 9.800.000'));
  click(btn('Sembunyikan'));
  check('saldo bisa disembunyikan', await waitFor(() => text().includes('Rp ••••••') && !/-450\.000.*Masuk hari ini/.test(text().slice(0, 400))));
  check('preferensi sembunyi tersimpan', w.localStorage.getItem('ledger.hide') === '1');
  click(btn('Tampilkan'));
  check('saldo bisa ditampilkan lagi', await waitFor(() => /-450\.000/.test(text())));

  // --- filter dan pencarian ---
  click(btn('Keluar', d.querySelector('.chips'))); // chip filter "Keluar"
  check('filter jenis Keluar (server)', await waitFor(() => d.querySelectorAll('.list li').length === 1 && li('Bahan baku')), text().slice(0, 400));
  click(btn('Semua')); await waitFor(() => d.querySelectorAll('.list li').length >= 3);
  set(d.querySelector('input.search'), 'prive'); await sleep(150);
  check('pencarian teks (klien)', d.querySelectorAll('.list li').length === 1 && !!li('Ambil pribadi'));
  set(d.querySelector('input.search'), 'zzzz'); await sleep(150);
  check('pencarian tanpa hasil memberi pesan', text().includes('Tidak ada hasil untuk'));
  set(d.querySelector('input.search'), ''); await sleep(100);

  // --- detail + edit ---
  click(li('Pemasukan')); 
  check('lembar detail terbuka', await waitFor(() => !!d.querySelector('.sheet') && text().includes('Dicatat manual')));
  click(btn('Ubah', d.querySelector('.sheet')));
  await sleep(100);
  const sheetForm = d.querySelector('.sheet form');
  set(sheetForm.querySelector('input[maxlength="280"]'), 'jual pagi');
  submit(sheetForm);
  check('edit catatan tersimpan', await waitFor(() => text().includes('Perubahan disimpan')));
  check('catatan baru tampil di riwayat', await waitFor(() => !!li('jual pagi')));

  // --- hapus + urungkan + hapus permanen ---
  click(li('Ambil pribadi')); await waitFor(() => !!d.querySelector('.sheet'));
  click(btn('Hapus', d.querySelector('.sheet')));
  check('hapus menyembunyikan baris + tombol Urungkan', await waitFor(() => !li('Ambil pribadi') && !!btn('Urungkan')));
  click(btn('Urungkan'));
  check('urungkan mengembalikan baris', await waitFor(() => !!li('Ambil pribadi')));
  click(li('Ambil pribadi')); await waitFor(() => !!d.querySelector('.sheet'));
  click(btn('Hapus', d.querySelector('.sheet')));
  await sleep(5800);
  check('penghapusan terkirim setelah jendela urungkan & saldo pulih', await waitFor(() => !li('Ambil pribadi') && /Rp 2\.000\.000/.test(text()), 6000), text().slice(0, 300));

  // --- laporan + bagikan ---
  click(btn('Laporan'));
  check('laporan bulanan', await waitFor(() => text().includes('Untung bersih') && text().includes('Rp 2.000.000')));
  click(btn('Bagikan ringkasan'));
  check('bagikan membuka WhatsApp dengan ringkasan', !!opened && opened.startsWith('https://wa.me/?text=') && decodeURIComponent(opened).includes('Warung Bu Sari'), opened);

  // --- kunci layar ---
  click(btn('Beranda')); await sleep(200);
  click(btn('Kunci'));
  check('layar terkunci menyembunyikan data', await waitFor(() => text().includes('Layar terkunci') && !text().includes('Warung Bu Sari')));
  set(d.querySelector('.lock input'), '000999'); submit(d.querySelector('.lock form'));
  check('PIN salah tetap terkunci', await waitFor(() => text().includes('PIN salah')) && text().includes('Layar terkunci'));
  await idle(); await sleep(300);
  set(d.querySelector('.lock input'), '482913'); submit(d.querySelector('.lock form'));
  check('PIN benar membuka kunci', await waitFor(() => text().includes('Halo, Bu Sari') && !text().includes('Layar terkunci')));

  // --- keluar ---
  click(btn('Keluar', d.querySelector('.top-actions')));
  check('keluar -> layar masuk', await waitFor(() => !!d.querySelector('.auth')));
  check('token dihapus', !w.localStorage.getItem('ledger.token'));
  check('tidak ada error JS selama alur', errors.length === 0, errors.join('; '));

  console.log(fails ? `\n${fails} GAGAL` : '\nSEMUA PASS');
  process.exit(fails ? 1 : 0);
})().catch((e) => { console.error('CRASH', e); process.exit(2); });
