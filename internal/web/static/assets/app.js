// Buku Kas Pintar: antarmuka Vue 3 (tanpa build tools).
// Dua mode data, dipilih server lewat /v1/config:
//   device (default): seluruh data keuangan terenkripsi di perangkat (IndexedDB + WebCrypto). Server tidak menyimpan apa pun.
//   server:           mode lama dengan akun + PostgreSQL (/v1/...).
// Kedua mode memakai fungsi api(method, path, body) yang sama, sehingga UI tidak perlu tahu data ada di mana.
const { createApp, ref, reactive, computed } = Vue;
const LL = window.LedgerLocal; // penyimpanan di perangkat (local-*.js)

const MAX_AMOUNT = 10_000_000_000;
const IDLE_MS = 5 * 60 * 1000; // kunci otomatis mode server; mode perangkat memakai pengaturan pengguna
const UNDO_MS = 5000;

const nf = new Intl.NumberFormat('id-ID');
const rp = (n) => 'Rp ' + nf.format(Math.round(n || 0));
const money = (n) => (n ? nf.format(n) : '');
const shortRp = (n) => (n >= 1000 ? nf.format(n / 1000) + 'rb' : String(n));
const todayLocal = () => new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 10);
// Tanggal kalender di WIB (zona usaha), dipakai untuk pengelompokan riwayat.
const wibKey = (iso) => new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Jakarta' }).format(new Date(iso));
const toLocalInput = (iso) => {
  const d = new Date(iso);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
};
const fmtBytes = (n) => (n == null ? '-' : n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB');

// UUID untuk client_id: membuat pengiriman ulang (mis. sinyal putus) tidak menggandakan catatan.
function uuid() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 15) | 64; b[8] = (b[8] & 63) | 128;
  const h = [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

createApp({
  setup() {
    const TOKEN_KEY = 'ledger.token', HIDE_KEY = 'ledger.hide', ACTIVE_KEY = 'ledger.active', MODE_KEY = 'ledger.mode';
    const mode = ref('boot'); // boot | device | server
    const fatal = ref('');
    const deviceState = ref('none'); // none (belum ada brankas) | locked | open
    const showRestore = ref(false);
    const settings = ref({});
    const ocrInfo = reactive({ engine: '', access_code_required: false });
    const storageInfo = reactive({ persisted: null, usage: null, quota: null });
    const installEvt = ref(null);
    const backupNeeded = ref(false);
    const backupInfo = reactive({ changes: 0, last: null });

    const token = ref(localStorage.getItem(TOKEN_KEY) || '');
    const me = ref(null), business = ref(null), loaded = ref(false);
    const wallets = ref([]), categories = ref([]);
    const txs = ref([]), nextCursor = ref('');
    const homeReport = ref(null), report = ref(null), daily = ref([]), catReport = ref(null);
    const tab = ref('home'), walletView = ref('business'), month = ref(todayLocal().slice(0, 7));
    const busy = ref(false), toast = ref(null);
    const hideBalance = ref(localStorage.getItem(HIDE_KEY) === '1');
    let toastTimer = null;

    const authMode = ref('login');
    const auth = reactive({ name: '', phone: '', pin: '' });
    const newBiz = reactive({ name: '', kind: 'warung_makan' });
    const form = reactive({ wallet: 'business', kind: 'income', amount: null, category_id: '', note: '', client_id: uuid() });
    const transfer = reactive({ direction: 'to_personal', amount: null, note: '', client_id: uuid() });
    const receipt = ref(null), receiptImg = ref('');
    const rform = reactive({ merchant: '', date: todayLocal(), wallet: 'business', items: [] });
    const filters = reactive({ kind: '', category_id: '', from: '', to: '', q: '' });
    const sheet = ref(null), editing = ref(false);
    const editForm = reactive({ note: '', category_id: '', when: '' });
    const locked = ref(false), lockPin = ref(''); // kunci layar mode server
    const quick = [10000, 20000, 50000, 100000];

    // mode perangkat
    const onboard = reactive({ step: 1, name: '', pin: '', pin2: '', recovery: '', saved: false });
    const lockForm = reactive({ pin: '', useRecovery: false, recovery: '', newPin: '', newPin2: '' });
    const pinForm = reactive({ old: '', next: '', next2: '' });
    const setForm = reactive({ name: '', ocrCode: '', recoveryPin: '', newRecovery: '', wipeText: '', csvMonth: todayLocal().slice(0, 7) });
    const backupOpts = reactive({ images: false });
    const rs = reactive({ summary: null, pin: '', recovery: '', useRecovery: false, ack: false });
    // File cadangan SENGAJA tidak reaktif: IndexedDB tidak bisa menyimpan Proxy Vue (DataCloneError), dan objeknya bisa puluhan MB.
    let pendingBackup = null;
    const canShareFiles = !!(navigator.canShare && typeof File === 'function' && (() => { try { return navigator.canShare({ files: [new File([''], 'a.bkp')] }); } catch { return false; } })());

    // ---------- util umum ----------
    function notify(text, kind = 'ok', opts = {}) {
      toast.value = { text, kind, action: opts.action || null };
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => (toast.value = null), opts.ms || 4000);
      if (kind === 'ok' && navigator.vibrate) navigator.vibrate(25);
    }

    async function api(method, path, body, opts = {}) {
      if (mode.value === 'device') {
        try {
          const r = await LL.api(method, path, body, opts);
          if (r === undefined || r === null) return null;
          if (r instanceof Blob) return r;
          return JSON.parse(JSON.stringify(r)); // salinan: UI tidak boleh memutasi data brankas secara langsung
        } catch (e) {
          if (e && e.status === 401) lockLocalState(); // brankas ternyata terkunci
          throw e;
        }
      }
      const headers = {};
      if (token.value) headers.Authorization = 'Bearer ' + token.value;
      let payload;
      if (body instanceof FormData) payload = body;
      else if (body !== undefined) { headers['Content-Type'] = 'application/json'; payload = JSON.stringify(body); }
      let res;
      try {
        res = await fetch('/v1' + path, { method, headers, body: payload });
      } catch {
        throw new Error('Tidak ada koneksi. Data belum terkirim, silakan coba lagi.');
      }
      if (res.status === 401 && token.value && !path.startsWith('/auth/')) { clearSession(); throw new Error('Sesi berakhir, silakan masuk lagi.'); }
      if (opts.blob) { if (!res.ok) throw new Error('Gagal memuat gambar.'); return res.blob(); }
      const text = await res.text();
      const data = text ? JSON.parse(text) : null;
      if (!res.ok) throw new Error((data && data.error && data.error.message) || 'Terjadi kesalahan.');
      return data;
    }

    // Aksi tulis: diblok saat ada aksi lain berjalan, supaya tidak terkirim ganda.
    async function run(fn) {
      if (busy.value) return;
      busy.value = true;
      try { await fn(); } catch (e) { notify(e.message, 'error'); } finally { busy.value = false; }
    }
    // Pemuatan data (baca): tidak boleh dibuang walau ada aksi lain berjalan.
    async function load(fn) {
      try { await fn(); } catch (e) { notify(e.message, 'error'); }
    }
    const loadMore = () => load(() => loadTxs(true));
    const bid = () => business.value.id;

    // ---------- layar yang tampil ----------
    const screen = computed(() => {
      if (fatal.value) return 'fatal';
      if (mode.value === 'boot') return 'boot';
      if (mode.value === 'device') {
        if (showRestore.value) return 'restore';
        if (deviceState.value === 'none' || onboard.step === 2) return 'onboard';
        if (deviceState.value === 'locked') return 'lock';
        if (!loaded.value) return 'boot';
        return business.value ? 'app' : 'setup';
      }
      if (!token.value) return 'auth';
      if (locked.value) return 'lock';
      if (loaded.value && !business.value) return 'setup';
      return business.value ? 'app' : 'boot';
    });

    // ---------- kunci layar & aktivitas ----------
    let lastActive = Date.now(), lastSaved = 0;
    function touch() {
      lastActive = Date.now();
      if (mode.value === 'server' && lastActive - lastSaved > 10000) { localStorage.setItem(ACTIVE_KEY, String(lastActive)); lastSaved = lastActive; }
    }
    ['pointerdown', 'keydown', 'touchstart', 'scroll'].forEach((ev) => window.addEventListener(ev, touch, { passive: true }));
    const authed = () => (mode.value === 'device' ? deviceState.value === 'open' : !!token.value && !locked.value);
    const idleMs = () => (mode.value === 'device' ? (settings.value.idleMinutes ?? 5) * 60000 : IDLE_MS); // 0 = nonaktif
    const idleCheck = () => { const ms = idleMs(); if (ms > 0 && authed() && Date.now() - lastActive > ms) lockNow(); };
    setInterval(idleCheck, 15000);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) idleCheck(); });

    // Hapus semua data terdekripsi dari memori UI.
    function clearData() {
      me.value = null; business.value = null; loaded.value = false;
      wallets.value = []; categories.value = []; txs.value = []; nextCursor.value = '';
      homeReport.value = null; report.value = null; daily.value = []; catReport.value = null;
      sheet.value = null; editing.value = false; settings.value = {}; backupNeeded.value = false;
      resetReceipt();
    }
    function lockLocalState() { clearData(); deviceState.value = 'locked'; }

    async function lockNow() {
      await commitPending();
      sheet.value = null; editing.value = false;
      if (mode.value === 'device') { LL.vault.lock(); lockLocalState(); resetLockForm(); }
      else { lockPin.value = ''; locked.value = true; }
    }
    function resetLockForm() { Object.assign(lockForm, { pin: '', useRecovery: false, recovery: '', newPin: '', newPin2: '' }); }

    // ---------- mode server: sesi ----------
    function unlock() { // kunci layar mode server: login ulang dengan PIN
      run(async () => {
        if (!me.value) throw new Error('Data akun belum dimuat. Coba lagi atau keluar.');
        const s = await api('POST', '/auth/login', { phone: me.value.phone, pin: lockPin.value });
        const old = token.value;
        token.value = s.token; localStorage.setItem(TOKEN_KEY, s.token);
        fetch('/v1/auth/logout', { method: 'POST', headers: { Authorization: 'Bearer ' + old } }).catch(() => {}); // cabut sesi lama
        locked.value = false; lockPin.value = '';
        touch();
      });
    }
    function clearSession() {
      token.value = ''; localStorage.removeItem(TOKEN_KEY); localStorage.removeItem(ACTIVE_KEY);
      clearData(); locked.value = false;
    }
    async function logout() {
      await commitPending();
      try { if (token.value) await api('POST', '/auth/logout'); } catch { /* sesi memang akan dihapus */ }
      clearSession();
    }
    async function submitAuth() {
      await run(async () => {
        if (authMode.value === 'register') await api('POST', '/auth/register', { name: auth.name, phone: auth.phone, pin: auth.pin });
        const s = await api('POST', '/auth/login', { phone: auth.phone, pin: auth.pin });
        token.value = s.token; localStorage.setItem(TOKEN_KEY, s.token);
        auth.pin = '';
        touch();
        await loadSession();
      });
    }

    // ---------- mode perangkat: onboarding, buka kunci ----------
    const pinStrength = computed(() => LL.crypto.strength(onboard.pin));
    const newPinStrength = computed(() => LL.crypto.strength(pinForm.next));
    const lockNewStrength = computed(() => LL.crypto.strength(lockForm.newPin));

    function checkNewPin(pin, pin2) {
      const st = LL.crypto.strength(pin);
      if (!st.ok) throw new Error(st.label);
      if (pin !== pin2) throw new Error('PIN dan konfirmasi tidak sama.');
    }

    function onboardCreate() {
      run(async () => {
        checkNewPin(onboard.pin, onboard.pin2);
        onboard.recovery = await LL.vault.create(onboard.pin);
        await LL.ledger.setSettings({ name: onboard.name.trim().slice(0, 40), idleMinutes: 5 });
        onboard.pin = ''; onboard.pin2 = '';
        deviceState.value = 'open';
        onboard.step = 2;
      });
    }
    function copyRecovery(code) {
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(code).then(() => notify('Kode disalin.'), () => notify('Tidak bisa menyalin, catat manual.', 'error'));
    }
    function downloadRecovery(code) {
      const text = `Kode pemulihan Buku Kas Pintar\r\n${code}\r\n\r\nSimpan di tempat aman (jangan di HP yang sama). Siapa pun yang memegang kode ini DAN file cadangan bisa membuka datamu.\r\n`;
      LL.backup.download(new Blob([text], { type: 'text/plain' }), 'kode-pemulihan-bukukas.txt');
    }
    function onboardFinish() {
      run(async () => {
        await LL.backup.requestPersist(); // minta agar browser tidak menghapus data saat ruang penuh
        onboard.recovery = ''; onboard.step = 1; onboard.saved = false;
        touch();
        await afterOpen();
      });
    }

    function deviceUnlock() {
      run(async () => {
        if (lockForm.useRecovery) {
          checkNewPin(lockForm.newPin, lockForm.newPin2);
          await LL.vault.unlock({ recovery: lockForm.recovery });
          await LL.vault.changePin({ recovery: lockForm.recovery }, lockForm.newPin);
          notify('PIN baru berhasil dibuat.');
        } else {
          await LL.vault.unlock({ pin: lockForm.pin });
        }
        resetLockForm();
        deviceState.value = 'open';
        touch();
        await afterOpen();
      });
    }
    async function afterOpen() {
      tab.value = 'home'; // setelah membuka kunci/pulihan selalu mulai dari Beranda (seperti aplikasi bank)
      settings.value = JSON.parse(JSON.stringify(LL.vault.state.settings || {}));
      setForm.name = settings.value.name || ''; setForm.ocrCode = settings.value.ocrCode || '';
      await loadSession();
      refreshBackupState();
      loadStorage();
    }

    // ---------- pengaturan & cadangan (mode perangkat) ----------
    function refreshBackupState() {
      if (mode.value !== 'device' || !LL.vault.isOpen()) return;
      backupNeeded.value = LL.backup.backupDue();
      const c = LL.vault.meta.counter;
      backupInfo.changes = c.changes; backupInfo.last = c.lastBackupAt;
    }
    async function loadStorage() { Object.assign(storageInfo, await LL.backup.storageStatus()); }
    const askPersist = () => run(async () => {
      const ok = await LL.backup.requestPersist();
      await loadStorage();
      notify(ok ? 'Penyimpanan permanen aktif.' : 'Browser belum memberi izin. Pasang aplikasi ke layar utama untuk hasil terbaik.', ok ? 'ok' : 'error');
    });

    const saveName = () => run(async () => {
      await LL.ledger.setSettings({ name: setForm.name.trim().slice(0, 40) });
      settings.value = { ...LL.vault.state.settings };
      me.value = await api('GET', '/me');
      notify('Nama disimpan.');
    });
    const setIdle = (m) => run(async () => { await LL.ledger.setSettings({ idleMinutes: Number(m) }); settings.value = { ...LL.vault.state.settings }; notify('Pengaturan kunci otomatis disimpan.'); });
    const saveOcrCode = () => run(async () => { await LL.ledger.setSettings({ ocrCode: setForm.ocrCode.trim() }); settings.value = { ...LL.vault.state.settings }; notify('Kode akses OCR disimpan.'); });

    const changePin = () => run(async () => {
      checkNewPin(pinForm.next, pinForm.next2);
      await LL.vault.changePin({ pin: pinForm.old }, pinForm.next);
      pinForm.old = pinForm.next = pinForm.next2 = '';
      notify('PIN berhasil diganti.');
    });
    const makeRecovery = () => run(async () => {
      setForm.newRecovery = await LL.vault.rotateRecovery(setForm.recoveryPin);
      setForm.recoveryPin = '';
    });

    async function doBackup(share) {
      await run(async () => {
        const { blob, filename, counts } = await LL.backup.exportBackup({ includeImages: backupOpts.images });
        let shared = false;
        if (share) { try { shared = await LL.backup.share(blob, filename); } catch { shared = false; } }
        if (!shared) LL.backup.download(blob, filename);
        refreshBackupState();
        notify(`Cadangan dibuat (${counts.records} catatan, ${fmtBytes(counts.bytes)}). Simpan file-nya di tempat aman.`);
      });
    }
    function exportCsv(all) {
      load(async () => {
        const text = LL.backup.toCsv(bid(), all ? '' : setForm.csvMonth);
        LL.backup.download(new Blob([text], { type: 'text/csv;charset=utf-8' }), `bukukas-${all ? 'semua' : setForm.csvMonth}.csv`);
        notify('File CSV diunduh.');
      });
    }

    function openRestore() { pendingBackup = null; Object.assign(rs, { summary: null, pin: '', recovery: '', useRecovery: false, ack: false }); showRestore.value = true; }
    function closeRestore() { showRestore.value = false; }
    function onBackupFile(ev) {
      const f = ev.target.files && ev.target.files[0];
      ev.target.value = '';
      if (!f) return;
      run(async () => {
        if (f.size > 80 * 1024 * 1024) throw new Error('File cadangan terlalu besar.');
        const b = LL.backup.parseBackup(await f.text());
        pendingBackup = b;
        rs.summary = { records: b.records.length, images: b.images.length, created: b.created_at };
      });
    }
    function doRestore() {
      run(async () => {
        if (!pendingBackup) throw new Error('Pilih file cadangan dulu.');
        if (deviceState.value !== 'none' && !rs.ack) throw new Error('Centang persetujuan: data di perangkat ini akan diganti.');
        await LL.backup.restore(pendingBackup, rs.useRecovery ? { recovery: rs.recovery } : { pin: rs.pin });
        const n = rs.summary.records;
        pendingBackup = null;
        Object.assign(rs, { summary: null, pin: '', recovery: '', ack: false });
        showRestore.value = false;
        clearData();
        deviceState.value = 'open';
        touch();
        await afterOpen();
        notify(`Data dipulihkan (${n} catatan).`);
      });
    }

    function wipeAll() {
      run(async () => {
        if (setForm.wipeText !== 'HAPUS') throw new Error('Ketik HAPUS (huruf besar) untuk konfirmasi.');
        await LL.vault.wipe();
        clearData(); setForm.wipeText = '';
        deviceState.value = 'none'; onboard.step = 1;
        notify('Semua data di perangkat ini sudah dihapus.');
      });
    }
    async function installApp() {
      if (!installEvt.value) return;
      installEvt.value.prompt();
      installEvt.value = null;
    }
    window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); installEvt.value = e; });

    // ---------- data ----------
    async function loadSession() {
      me.value = await api('GET', '/me');
      const { businesses } = await api('GET', '/businesses');
      business.value = businesses[0] || null;
      if (business.value) await loadAll();
      loaded.value = true;
    }
    async function loadAll() {
      const id = bid();
      const [w, c] = await Promise.all([api('GET', `/businesses/${id}/wallets`), api('GET', `/businesses/${id}/categories`)]);
      wallets.value = w.wallets; categories.value = c.categories;
      await Promise.all([loadTxs(false), loadHome()]);
    }
    async function loadHome() {
      const id = bid();
      const [r, d] = await Promise.all([api('GET', `/businesses/${id}/reports/monthly`), api('GET', `/businesses/${id}/reports/daily?days=7`)]);
      homeReport.value = r; daily.value = d.days;
    }
    async function loadReport() {
      report.value = await api('GET', `/businesses/${bid()}/reports/monthly?month=${month.value}`);
      if (mode.value === 'device') catReport.value = await api('GET', `/businesses/${bid()}/reports/categories?month=${month.value}`);
    }
    async function loadTxs(more) {
      const q = new URLSearchParams({ limit: '20', wallet: walletView.value });
      if (filters.kind) q.set('kind', filters.kind);
      if (filters.category_id) q.set('category_id', filters.category_id);
      if (filters.from) q.set('from', filters.from);
      if (filters.to) q.set('to', filters.to);
      if (more && nextCursor.value) q.set('cursor', nextCursor.value);
      const r = await api('GET', `/businesses/${bid()}/transactions?${q}`);
      txs.value = more ? txs.value.concat(r.transactions) : r.transactions;
      nextCursor.value = r.next_cursor || '';
    }
    async function refreshMoney() {
      const w = await api('GET', `/businesses/${bid()}/wallets`);
      wallets.value = w.wallets;
      await Promise.all([loadTxs(false), loadHome()]);
      refreshBackupState();
    }
    async function createBusiness() {
      await run(async () => {
        business.value = await api('POST', '/businesses', { name: newBiz.name, kind: newBiz.kind });
        await loadAll();
        refreshBackupState();
        notify('Usaha dibuat. Dompet Usaha dan Pribadi sudah siap.');
      });
    }

    // ---------- saldo, filter, pengelompokan ----------
    const walletBalance = (kind) => (wallets.value.find((w) => w.kind === kind) || { balance: 0 }).balance;
    const currentBalance = computed(() => walletBalance(walletView.value));
    const mask = (n) => (hideBalance.value ? 'Rp ••••••' : rp(n));
    function toggleHide() { hideBalance.value = !hideBalance.value; localStorage.setItem(HIDE_KEY, hideBalance.value ? '1' : '0'); }
    const today = computed(() => daily.value.length ? daily.value[daily.value.length - 1] : null);

    function setWalletView(k) { walletView.value = k; load(() => loadTxs(false)); }
    function setKindFilter(k) { filters.kind = k; load(() => loadTxs(false)); }
    const filtersActive = computed(() => !!(filters.category_id || filters.from || filters.to));
    function resetFilters() { filters.category_id = ''; filters.from = ''; filters.to = ''; load(() => loadTxs(false)); }

    // Pencarian teks berjalan di klien pada riwayat yang sudah dimuat (di mode server catatan terenkripsi sehingga
    // server tidak bisa mencarinya; di mode perangkat pun tetap sama agar perilakunya seragam).
    const visibleTxs = computed(() => {
      const q = filters.q.toLowerCase();
      if (!q) return txs.value;
      return txs.value.filter((t) => [t.category_name, t.note, String(t.amount), rp(t.amount)].some((x) => x && String(x).toLowerCase().includes(q)));
    });
    const dayLabel = (key) => {
      const now = wibKey(new Date().toISOString());
      const yest = wibKey(new Date(Date.now() - 86400000).toISOString());
      if (key === now) return 'Hari ini';
      if (key === yest) return 'Kemarin';
      return new Date(key + 'T00:00:00').toLocaleDateString('id-ID', { weekday: 'long', day: 'numeric', month: 'short', year: 'numeric' });
    };
    const groups = computed(() => {
      const map = new Map();
      for (const t of visibleTxs.value) {
        const key = wibKey(t.occurred_at);
        if (!map.has(key)) map.set(key, { key, label: dayLabel(key), items: [], income: 0, expense: 0 });
        const g = map.get(key);
        g.items.push(t);
        if (t.kind === 'income') g.income += t.amount;
        if (t.kind === 'expense') g.expense += t.amount;
      }
      return [...map.values()];
    });
    const barPct = (v) => {
      const max = Math.max(1, ...daily.value.map((d) => Math.max(d.income, d.expense)));
      return v > 0 ? Math.max(4, Math.round((v / max) * 100)) + '%' : '0%';
    };
    const weekday = (d) => new Date(d + 'T00:00:00').toLocaleDateString('id-ID', { weekday: 'short' });

    function go(t) {
      tab.value = t;
      if (t === 'home') load(refreshMoney);
      if (t === 'report') load(loadReport);
      if (t === 'more' && mode.value === 'device') { refreshBackupState(); loadStorage(); }
    }

    // ---------- input nominal ----------
    function onMoney(ev, obj, key) {
      const digits = ev.target.value.replace(/\D/g, '').slice(0, 11);
      const n = digits ? Math.min(Number(digits), MAX_AMOUNT) : null;
      obj[key] = n;
      ev.target.value = n ? nf.format(n) : '';
    }
    function addAmount(obj, key, n) { obj[key] = Math.min((obj[key] || 0) + n, MAX_AMOUNT); }

    // ---------- catat transaksi ----------
    const formCategories = computed(() =>
      categories.value.filter((c) => c.kind === form.kind && (c.scope === 'any' || c.scope === form.wallet)));
    function setKind(k) { form.kind = k; form.category_id = ''; }

    async function submitTx() {
      await run(async () => {
        await api('POST', `/businesses/${bid()}/transactions`, {
          wallet: form.wallet, kind: form.kind, amount: form.amount,
          category_id: form.category_id || null, note: form.note, client_id: form.client_id,
        });
        // client_id baru HANYA setelah sukses; bila gagal karena jaringan, kirim ulang aman.
        form.amount = null; form.note = ''; form.client_id = uuid();
        notify('Transaksi tersimpan.');
        await refreshMoney();
      });
    }
    async function submitTransfer() {
      await run(async () => {
        await api('POST', `/businesses/${bid()}/transfers`, {
          direction: transfer.direction, amount: transfer.amount, note: transfer.note, client_id: transfer.client_id,
        });
        transfer.amount = null; transfer.note = ''; transfer.client_id = uuid();
        notify('Uang dipindahkan.');
        await refreshMoney();
      });
    }

    // ---------- detail, edit, hapus (dengan urungkan) ----------
    const kindLabel = (t) => (t.kind === 'income' ? 'Pemasukan' : t.kind === 'expense' ? 'Pengeluaran' : t.direction === 'out' ? 'Pindah (keluar)' : 'Pindah (masuk)');
    function openSheet(t) { sheet.value = t; editing.value = false; }
    function closeSheet() { sheet.value = null; editing.value = false; }
    const editCategories = computed(() => sheet.value
      ? categories.value.filter((c) => c.kind === sheet.value.kind && (c.scope === 'any' || c.scope === sheet.value.wallet))
      : []);
    function startEdit() {
      editForm.note = sheet.value.note || '';
      editForm.category_id = sheet.value.category_id || '';
      editForm.when = toLocalInput(sheet.value.occurred_at);
      editing.value = true;
    }
    function saveEdit() {
      run(async () => {
        const body = { note: editForm.note, occurred_at: new Date(editForm.when).toISOString() };
        body.category_id = sheet.value.kind === 'transfer' ? null : (editForm.category_id || null);
        await api('PUT', `/businesses/${bid()}/transactions/${sheet.value.id}`, body);
        closeSheet();
        notify('Perubahan disimpan.');
        await refreshMoney();
      });
    }

    // Hapus dengan jendela urungkan: baris disembunyikan dulu, penghapusan sungguhan dikirim setelah UNDO_MS.
    // Bila tab ditutup sebelum itu, transaksi tetap aman (tidak terhapus).
    let pending = null;
    async function commitPending() {
      if (!pending) return;
      const p = pending; pending = null; clearTimeout(p.timer);
      try { await api('DELETE', `/businesses/${bid()}/transactions/${p.id}`); }
      catch (e) { notify(e.message, 'error'); }
      await load(refreshMoney);
    }
    function undoDelete() {
      if (!pending) return;
      clearTimeout(pending.timer);
      txs.value = txs.value.concat(pending.removed)
        .sort((a, b) => Date.parse(b.occurred_at) - Date.parse(a.occurred_at) || b.id.localeCompare(a.id));
      pending = null; toast.value = null;
    }
    function queueDelete(t) {
      commitPending();
      const removed = txs.value.filter((x) => x.id === t.id || (t.transfer_group_id && x.transfer_group_id === t.transfer_group_id));
      txs.value = txs.value.filter((x) => !removed.includes(x));
      pending = { id: t.id, removed, timer: setTimeout(() => { toast.value = null; commitPending(); }, UNDO_MS) };
      notify('Transaksi dihapus.', 'ok', { action: { label: 'Urungkan', fn: undoDelete }, ms: UNDO_MS });
    }
    function deleteFromSheet() { const t = sheet.value; closeSheet(); queueDelete(t); }

    // ---------- scan nota ----------
    const receiptCategories = computed(() =>
      categories.value.filter((c) => c.kind === 'expense' && (c.scope === 'any' || c.scope === rform.wallet)));
    const receiptTotal = computed(() => rform.items.reduce((s, it) => s + (Math.round(it.qty * it.unit_price) || 0), 0));

    function resetReceipt() {
      if (receiptImg.value) URL.revokeObjectURL(receiptImg.value);
      receipt.value = null; receiptImg.value = '';
      rform.merchant = ''; rform.date = todayLocal(); rform.wallet = 'business'; rform.items = [];
    }
    function addItem() { rform.items.push({ name: '', qty: 1, unit_price: 0, category_id: null }); }

    function onFile(ev) {
      const file = ev.target.files && ev.target.files[0];
      ev.target.value = '';
      if (!file) return;
      if (file.size > 5 * 1024 * 1024) { notify('Ukuran foto maksimal 5 MB.', 'error'); return; }
      run(async () => {
        const fd = new FormData();
        fd.append('image', file);
        const r = await api('POST', `/businesses/${bid()}/receipts`, fd);
        receipt.value = r;
        rform.merchant = r.merchant || '';
        rform.date = r.receipt_date || todayLocal();
        rform.items = r.items.map((i) => ({ name: i.name, qty: i.qty, unit_price: i.unit_price, category_id: i.category_id }));
        if (!rform.items.length) addItem();
        const blob = await api('GET', `/businesses/${bid()}/receipts/${r.id}/image`, undefined, { blob: true });
        receiptImg.value = URL.createObjectURL(blob);
      });
    }
    async function confirmReceipt() {
      await run(async () => {
        const res = await api('POST', `/businesses/${bid()}/receipts/${receipt.value.id}/confirm`, {
          wallet: rform.wallet, merchant: rform.merchant, date: rform.date,
          items: rform.items.map((i) => ({ name: i.name, qty: i.qty, unit_price: i.unit_price, category_id: i.category_id || null })),
        });
        notify(`Tersimpan: ${rp(res.total)} (${res.transactions.length} transaksi).`);
        resetReceipt();
        tab.value = 'home';
        await refreshMoney();
      });
    }

    // ---------- laporan ----------
    const fmtDate = (iso) => new Date(iso).toLocaleDateString('id-ID', { day: 'numeric', month: 'short', year: 'numeric' });
    const fmtTime = (iso) => new Date(iso).toLocaleTimeString('id-ID', { hour: '2-digit', minute: '2-digit' });
    const fmtDateTime = (iso) => (iso ? fmtDate(iso) + ', ' + fmtTime(iso) : 'belum pernah');
    function barWidth(v) {
      const r = report.value;
      const max = Math.max(r.profit, r.net_prive, 1);
      return Math.round((v / max) * 100) + '%';
    }
    // Dibagikan atas inisiatif pengguna (WhatsApp / menu bagikan HP); tidak ada data keluar otomatis.
    function shareReport() {
      const r = report.value;
      const text = [
        `Ringkasan ${business.value.name} (${r.month})`,
        `Penjualan: ${rp(r.revenue)}`, `Biaya usaha: ${rp(r.expenses)}`, `Untung bersih: ${rp(r.profit)}`,
        `Diambil pribadi: ${rp(r.prive)}`,
        r.leak_detected ? `Perhatian: uang diambil melebihi untung sebesar ${rp(r.leak_amount)}.` : '',
      ].filter(Boolean).join('\n');
      if (navigator.share) navigator.share({ title: 'Ringkasan keuangan', text }).catch(() => {});
      else window.open('https://wa.me/?text=' + encodeURIComponent(text), '_blank', 'noopener');
    }

    // ---------- start ----------
    async function boot() {
      if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {}); // bekerja offline (hanya cangkang aplikasi)
      let m = localStorage.getItem(MODE_KEY) || 'device';
      try {
        const res = await fetch('/v1/config');
        const cfg = await res.json();
        if (cfg && (cfg.mode === 'device' || cfg.mode === 'server')) {
          m = cfg.mode; localStorage.setItem(MODE_KEY, m);
          if (cfg.ocr) Object.assign(ocrInfo, cfg.ocr);
        }
      } catch { /* offline: pakai mode terakhir */ }
      mode.value = m;
      if (m === 'device') {
        if (!(window.crypto && window.crypto.subtle)) {
          fatal.value = 'Perangkat ini belum mendukung penyimpanan aman. Buka aplikasi lewat alamat HTTPS (atau localhost).';
          return;
        }
        try { deviceState.value = (await LL.vault.hasVault()) ? 'locked' : 'none'; }
        catch (e) { fatal.value = e.message; }
      } else if (token.value) {
        const saved = Number(localStorage.getItem(ACTIVE_KEY) || 0);
        if (saved && Date.now() - saved > IDLE_MS) locked.value = true; // dibuka lagi setelah lama: minta PIN
        run(loadSession);
      } else loaded.value = true;
    }
    boot();

    return {
      mode, fatal, screen, deviceState, showRestore, settings, ocrInfo, storageInfo, installEvt, backupNeeded, backupInfo, canShareFiles,
      token, me, business, loaded, wallets, categories, txs, nextCursor, homeReport, report, daily, catReport, tab, walletView, month, busy, toast,
      authMode, auth, newBiz, form, transfer, receipt, receiptImg, rform, filters, sheet, editing, editForm, locked, lockPin,
      onboard, lockForm, pinForm, setForm, backupOpts, rs, pinStrength, newPinStrength, lockNewStrength,
      hideBalance, quick, groups, filtersActive, today,
      rp, money, shortRp, mask, fmtDate, fmtTime, fmtDateTime, fmtBytes, barWidth, barPct, weekday, kindLabel,
      walletBalance, currentBalance, formCategories, editCategories, receiptCategories, receiptTotal,
      submitAuth, logout, lockNow, unlock, createBusiness, setWalletView, setKindFilter, resetFilters, toggleHide, go, setKind,
      onMoney, addAmount, submitTx, submitTransfer, openSheet, closeSheet, startEdit, saveEdit, deleteFromSheet,
      load, loadMore, loadTxs, refreshMoney, loadReport, onFile, confirmReceipt, resetReceipt, addItem, shareReport,
      onboardCreate, copyRecovery, downloadRecovery, onboardFinish, deviceUnlock, askPersist, saveName, setIdle, saveOcrCode,
      changePin, makeRecovery, doBackup, exportCsv, openRestore, closeRestore, onBackupFile, doRestore, wipeAll, installApp,
    };
  },
}).mount('#app');
