// Buku Kas Pintar: antarmuka Vue 3 (tanpa build tools). Semua komunikasi lewat /v1 pada origin yang sama.
const { createApp, ref, reactive, computed } = Vue;

const rp = (n) => 'Rp ' + new Intl.NumberFormat('id-ID').format(Math.round(n || 0));
const todayLocal = () => new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 10);

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
    const TOKEN_KEY = 'ledger.token';
    const token = ref(localStorage.getItem(TOKEN_KEY) || '');
    const me = ref(null), business = ref(null), loaded = ref(false);
    const wallets = ref([]), categories = ref([]);
    const txs = ref([]), nextCursor = ref('');
    const homeReport = ref(null), report = ref(null);
    const tab = ref('home'), walletView = ref('business'), month = ref(todayLocal().slice(0, 7));
    const busy = ref(false), toast = ref(null);
    let toastTimer = null;

    const authMode = ref('login');
    const auth = reactive({ name: '', phone: '', pin: '' });
    const newBiz = reactive({ name: '', kind: 'warung_makan' });
    const form = reactive({ wallet: 'business', kind: 'income', amount: null, category_id: '', note: '', client_id: uuid() });
    const transfer = reactive({ direction: 'to_personal', amount: null, note: '', client_id: uuid() });
    const receipt = ref(null), receiptImg = ref('');
    const rform = reactive({ merchant: '', date: todayLocal(), wallet: 'business', items: [] });

    function notify(text, kind = 'ok') {
      toast.value = { text, kind };
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => (toast.value = null), 4000);
    }

    async function api(method, path, body, opts = {}) {
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

    // Membungkus aksi: menampilkan status sibuk dan pesan error yang ramah.
    async function run(fn) {
      if (busy.value) return;
      busy.value = true;
      try { await fn(); } catch (e) { notify(e.message, 'error'); } finally { busy.value = false; }
    }

    // Pemuatan data (baca) tidak boleh dibuang walau ada aksi lain berjalan, beda dengan aksi tulis di run().
    async function load(fn) {
      try { await fn(); } catch (e) { notify(e.message, 'error'); }
    }
    const loadMore = () => load(() => loadTxs(true));

    const bid = () => business.value.id;

    function clearSession() {
      token.value = ''; localStorage.removeItem(TOKEN_KEY);
      me.value = null; business.value = null; loaded.value = false;
      wallets.value = []; txs.value = []; homeReport.value = null; report.value = null;
      resetReceipt();
    }

    async function logout() {
      try { if (token.value) await api('POST', '/auth/logout'); } catch { /* sesi memang akan dihapus */ }
      clearSession();
    }

    async function submitAuth() {
      await run(async () => {
        if (authMode.value === 'register') {
          await api('POST', '/auth/register', { name: auth.name, phone: auth.phone, pin: auth.pin });
        }
        const s = await api('POST', '/auth/login', { phone: auth.phone, pin: auth.pin });
        token.value = s.token; localStorage.setItem(TOKEN_KEY, s.token);
        auth.pin = '';
        await loadSession();
      });
    }

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

    async function loadHome() { homeReport.value = await api('GET', `/businesses/${bid()}/reports/monthly`); }
    async function loadReport() { report.value = await api('GET', `/businesses/${bid()}/reports/monthly?month=${month.value}`); }

    async function loadTxs(more) {
      let q = `?limit=20&wallet=${walletView.value}`;
      if (more && nextCursor.value) q += `&cursor=${encodeURIComponent(nextCursor.value)}`;
      const r = await api('GET', `/businesses/${bid()}/transactions${q}`);
      txs.value = more ? txs.value.concat(r.transactions) : r.transactions;
      nextCursor.value = r.next_cursor || '';
    }

    async function refreshMoney() {
      const w = await api('GET', `/businesses/${bid()}/wallets`);
      wallets.value = w.wallets;
      await Promise.all([loadTxs(false), loadHome()]);
    }

    async function createBusiness() {
      await run(async () => {
        business.value = await api('POST', '/businesses', { name: newBiz.name, kind: newBiz.kind });
        await loadAll();
        notify('Usaha dibuat. Dompet Usaha dan Pribadi sudah siap.');
      });
    }

    const walletBalance = (kind) => (wallets.value.find((w) => w.kind === kind) || { balance: 0 }).balance;
    const currentBalance = computed(() => walletBalance(walletView.value));

    function setWalletView(k) { walletView.value = k; load(() => loadTxs(false)); }

    function go(t) {
      tab.value = t;
      if (t === 'home') load(refreshMoney);
      if (t === 'report') load(loadReport);
    }

    // ---- catat transaksi ----
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

    function removeTx(t) {
      if (!confirm('Hapus transaksi ini?' + (t.transfer_group_id ? ' Kedua sisi transfer ikut terhapus.' : ''))) return;
      run(async () => {
        await api('DELETE', `/businesses/${bid()}/transactions/${t.id}`);
        notify('Transaksi dihapus.');
        await refreshMoney();
      });
    }

    // ---- scan nota ----
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

    // ---- tampilan ----
    const fmtDate = (iso) => new Date(iso).toLocaleDateString('id-ID', { day: 'numeric', month: 'short', year: 'numeric' });
    function barWidth(v) {
      const r = report.value;
      const max = Math.max(r.profit, r.net_prive, 1);
      return Math.round((v / max) * 100) + '%';
    }

    if (token.value) run(loadSession); else loaded.value = true;

    return {
      token, me, business, loaded, wallets, txs, nextCursor, homeReport, report, tab, walletView, month, busy, toast,
      authMode, auth, newBiz, form, transfer, receipt, receiptImg, rform,
      rp, fmtDate, barWidth, walletBalance, currentBalance, formCategories, receiptCategories, receiptTotal,
      submitAuth, logout, createBusiness, setWalletView, go, setKind, submitTx, submitTransfer, removeTx,
      load, loadMore, loadReport, onFile, confirmReceipt, resetReceipt, addItem,
    };
  },
}).mount('#app');
