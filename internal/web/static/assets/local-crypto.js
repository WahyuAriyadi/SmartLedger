// Kripto untuk penyimpanan di perangkat (WebCrypto). Tidak ada kunci yang pernah meninggalkan perangkat.
//
// Desain kunci (seperti pengelola kata sandi):
//   DEK  = kunci acak 256-bit yang mengenkripsi semua data (AES-256-GCM).
//   KEK_pin = PBKDF2-SHA256(PIN, salt, 600.000 iterasi) -> membungkus DEK  ("wrappedPin")
//   KEK_rec = HKDF(kode pemulihan 160-bit)              -> membungkus DEK  ("wrappedRec")
// Ganti PIN hanya membungkus ulang DEK (data tidak perlu dienkripsi ulang). Lupa PIN? Pakai kode pemulihan.
(function (g) {
  'use strict';
  const L = (g.LedgerLocal = g.LedgerLocal || {});
  const enc = new TextEncoder();
  const dec = new TextDecoder();
  const PBKDF2_ITER = 600000; // rekomendasi OWASP untuk PBKDF2-SHA256

  const b64 = {
    enc(buf) {
      const bytes = new Uint8Array(buf.buffer ? new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength) : buf);
      let s = '';
      for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
      return btoa(s);
    },
    dec(str) {
      const s = atob(str);
      const out = new Uint8Array(s.length);
      for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
      return out;
    },
  };

  const subtle = () => {
    if (!g.crypto || !g.crypto.subtle) {
      throw new Error('Perangkat ini belum mendukung penyimpanan aman. Buka lewat HTTPS atau localhost.');
    }
    return g.crypto.subtle;
  };
  const rand = (n) => g.crypto.getRandomValues(new Uint8Array(n));

  // ---------- kode pemulihan (base32 RFC 4648, 160-bit) ----------
  const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  function toBase32(bytes) {
    let bits = 0, val = 0, out = '';
    for (const b of bytes) {
      val = (val << 8) | b; bits += 8;
      while (bits >= 5) { out += B32[(val >>> (bits - 5)) & 31]; bits -= 5; }
    }
    if (bits > 0) out += B32[(val << (5 - bits)) & 31];
    return out;
  }
  function fromBase32(str) {
    let bits = 0, val = 0;
    const out = [];
    for (const ch of str) {
      const i = B32.indexOf(ch);
      if (i < 0) throw new Error('Kode pemulihan tidak valid.');
      val = (val << 5) | i; bits += 5;
      if (bits >= 8) { out.push((val >>> (bits - 8)) & 255); bits -= 8; }
    }
    return new Uint8Array(out);
  }
  const groupCode = (s) => s.match(/.{1,4}/g).join('-');
  const normalizeCode = (s) => String(s || '').toUpperCase().replace(/[^A-Z2-7]/g, '');

  // ---------- turunan kunci ----------
  async function kekFromPin(pin, salt, iterations) {
    const base = await subtle().importKey('raw', enc.encode(pin), 'PBKDF2', false, ['deriveKey']);
    return subtle().deriveKey({ name: 'PBKDF2', hash: 'SHA-256', salt, iterations }, base,
      { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  }
  async function kekFromRecovery(code, salt) {
    const raw = fromBase32(normalizeCode(code));
    if (raw.length !== 20) throw new Error('Kode pemulihan tidak valid.');
    const base = await subtle().importKey('raw', raw, 'HKDF', false, ['deriveKey']);
    return subtle().deriveKey({ name: 'HKDF', hash: 'SHA-256', salt, info: enc.encode('bukukas-recovery-v1') }, base,
      { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  }

  async function wrap(kek, rawDek, aad) {
    const iv = rand(12);
    const ct = await subtle().encrypt({ name: 'AES-GCM', iv, additionalData: enc.encode(aad) }, kek, rawDek);
    return { iv: b64.enc(iv), ct: b64.enc(ct) };
  }
  async function unwrap(kek, w, aad) {
    return new Uint8Array(await subtle().decrypt(
      { name: 'AES-GCM', iv: b64.dec(w.iv), additionalData: enc.encode(aad) }, kek, b64.dec(w.ct)));
  }
  const importDek = (raw) => subtle().importKey('raw', raw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);

  // ---------- brankas (vault) ----------
  async function createVault(pin, opts = {}) {
    const iterations = opts.iterations || PBKDF2_ITER;
    const rawDek = rand(32);
    const salt = rand(16), recSalt = rand(16);
    const recoveryCode = groupCode(toBase32(rand(20)));
    const [kekP, kekR] = await Promise.all([kekFromPin(pin, salt, iterations), kekFromRecovery(recoveryCode, recSalt)]);
    const meta = {
      v: 1,
      kdf: { name: 'PBKDF2-SHA256', iter: iterations, salt: b64.enc(salt) },
      rec: { salt: b64.enc(recSalt) },
      wrappedPin: await wrap(kekP, rawDek, 'dek-pin-v1'),
      wrappedRec: await wrap(kekR, rawDek, 'dek-rec-v1'),
    };
    const dek = await importDek(rawDek);
    rawDek.fill(0);
    return { meta, dek, recoveryCode };
  }

  async function openWithPin(meta, pin) {
    try {
      const kek = await kekFromPin(pin, b64.dec(meta.kdf.salt), meta.kdf.iter);
      const raw = await unwrap(kek, meta.wrappedPin, 'dek-pin-v1');
      const dek = await importDek(raw);
      raw.fill(0);
      return dek;
    } catch (e) {
      if (/HTTPS|localhost/.test(e.message)) throw e;
      throw new Error('PIN salah.');
    }
  }
  async function openWithRecovery(meta, code) {
    try {
      const kek = await kekFromRecovery(code, b64.dec(meta.rec.salt));
      const raw = await unwrap(kek, meta.wrappedRec, 'dek-rec-v1');
      const dek = await importDek(raw);
      raw.fill(0);
      return dek;
    } catch (e) {
      if (/HTTPS|localhost/.test(e.message)) throw e;
      throw new Error('Kode pemulihan salah.');
    }
  }

  // Ganti PIN: butuh PIN lama ATAU kode pemulihan (bila lupa). Hanya DEK yang dibungkus ulang.
  async function rewrapPin(meta, credential, newPin, opts = {}) {
    let raw;
    try {
      if (credential.pin != null) {
        raw = await unwrap(await kekFromPin(credential.pin, b64.dec(meta.kdf.salt), meta.kdf.iter), meta.wrappedPin, 'dek-pin-v1');
      } else {
        raw = await unwrap(await kekFromRecovery(credential.recovery, b64.dec(meta.rec.salt)), meta.wrappedRec, 'dek-rec-v1');
      }
    } catch { throw new Error(credential.pin != null ? 'PIN lama salah.' : 'Kode pemulihan salah.'); }
    const iterations = opts.iterations || PBKDF2_ITER;
    const salt = rand(16);
    const kek = await kekFromPin(newPin, salt, iterations);
    const next = { ...meta, kdf: { name: 'PBKDF2-SHA256', iter: iterations, salt: b64.enc(salt) }, wrappedPin: await wrap(kek, raw, 'dek-pin-v1') };
    raw.fill(0);
    return next;
  }

  // Buat kode pemulihan baru (yang lama otomatis tidak berlaku).
  async function rotateRecovery(meta, pin) {
    let raw;
    try { raw = await unwrap(await kekFromPin(pin, b64.dec(meta.kdf.salt), meta.kdf.iter), meta.wrappedPin, 'dek-pin-v1'); }
    catch { throw new Error('PIN salah.'); }
    const recSalt = rand(16);
    const code = groupCode(toBase32(rand(20)));
    const kek = await kekFromRecovery(code, recSalt);
    const next = { ...meta, rec: { salt: b64.enc(recSalt) }, wrappedRec: await wrap(kek, raw, 'dek-rec-v1') };
    raw.fill(0);
    return { meta: next, recoveryCode: code };
  }

  // ---------- enkripsi rekaman ----------
  // AAD = "<jenis>:<id>" mengikat ciphertext pada tempatnya: rekaman tidak bisa ditukar/disalin ke slot lain tanpa terdeteksi.
  async function seal(dek, aad, plainBytes) {
    const iv = rand(12);
    const ct = new Uint8Array(await subtle().encrypt({ name: 'AES-GCM', iv, additionalData: enc.encode(aad) }, dek, plainBytes));
    return { iv, ct };
  }
  async function open(dek, aad, rec) {
    try {
      return new Uint8Array(await subtle().decrypt({ name: 'AES-GCM', iv: rec.iv, additionalData: enc.encode(aad) }, dek, rec.ct));
    } catch { throw new Error('Data rusak atau telah diubah (verifikasi gagal).'); }
  }
  const sealJSON = (dek, aad, obj) => seal(dek, aad, enc.encode(JSON.stringify(obj)));
  const openJSON = async (dek, aad, rec) => JSON.parse(dec.decode(await open(dek, aad, rec)));

  // ---------- kekuatan PIN ----------
  const COMMON = new Set(['123456', '654321', '111111', '000000', '123123', '121212', '12345678', 'password', 'qwerty123']);
  function strength(pin) {
    const p = String(pin || '');
    if (p.length < 6) return { score: 0, ok: false, label: 'Minimal 6 karakter' };
    if (COMMON.has(p.toLowerCase()) || new Set(p).size === 1) return { score: 0, ok: false, label: 'Terlalu mudah ditebak' };
    const digitsOnly = /^\d+$/.test(p);
    let pool = 0;
    if (/\d/.test(p)) pool += 10;
    if (/[a-z]/.test(p)) pool += 26;
    if (/[A-Z]/.test(p)) pool += 26;
    if (/[^A-Za-z0-9]/.test(p)) pool += 32;
    const bits = Math.log2(pool) * p.length;
    if (digitsOnly && p.length <= 8) return { score: 1, ok: true, label: 'Lemah: angka saja bisa ditebak cepat bila HP dicuri. Disarankan 8+ karakter dengan huruf.' };
    if (bits < 45) return { score: 2, ok: true, label: 'Cukup. Tambah panjang agar lebih aman.' };
    if (bits < 60) return { score: 3, ok: true, label: 'Baik' };
    return { score: 4, ok: true, label: 'Kuat' };
  }

  L.crypto = {
    PBKDF2_ITER, b64, createVault, openWithPin, openWithRecovery, rewrapPin, rotateRecovery,
    seal, open, sealJSON, openJSON, strength, normalizeCode, rand,
  };
})(typeof globalThis !== 'undefined' ? globalThis : window);
