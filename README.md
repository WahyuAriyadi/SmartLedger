# Smart Micro-Ledger

**A local-first bookkeeping app for micro-businesses that answers one question: *"Sales are busy, so why is the cash drawer empty?"***

It keeps a **Business Wallet** and a **Personal Wallet** side by side, turns a photo of a purchase receipt into expenses, and flags **cash leaks**: months where the owner took out more money than the business actually earned.

Its defining design choice: **your financial data never lives on my server.** Everything is stored **encrypted on the user's own device** (IndexedDB + WebCrypto). The server only serves the app and acts as a stateless OCR gateway, so a server breach has no financial data to steal.

Built with Go (standard library first), vanilla-served Vue 3 (no build step), and PostgreSQL for an optional server mode. It runs **fully locally with zero cloud services**; the GCP version is already in the code as commented lines.

---

## Why I built this

Street-market vendors and food-stall owners in Indonesia usually track money in a paper notebook, with business money and household money in the same pocket. Full accounting tools such as Jurnal or Accurate are far too heavy for them. The familiar result: the stall is always busy, yet there is no cash left to restock.

The root cause is rarely low sales. It is usually **owner withdrawals quietly exceeding profit**. So the product is built around one signal:

> `profit = revenue − business expenses` vs. `net owner withdrawals (prive)`
> If withdrawals > profit, the business is being drained, and the app says so in plain language.

Because this is real people's money, I treated **privacy, security, and reliability as requirements from day one**, and then went one step further: if the server never holds the data, it cannot leak it.

---

## Features

| Area                                     | What it does                                                                                                                                                                       |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Two wallets, one app**           | Business and Personal wallets with balances always computed from the ledger, never stored.                                                                                         |
| **Quick entry**                    | Income/expense with amount, category and note. Formatted amount input with quick-add chips (+10k/+20k/+50k/+100k).                                                                 |
| **Owner draw / capital injection** | Business → Personal is recorded as*prive*, Personal → Business as a capital deposit; both ledger rows are written atomically.                                                  |
| **Receipt scanning**               | Photo → on-device downscale (strips EXIF/GPS) → OCR → review next to the photo → correct → confirm. One expense per category. Works offline as manual entry.                  |
| **Cash-leak detection**            | Monthly report compares profit with net owner withdrawals and highlights the gap.                                                                                                  |
| **Banking-app ergonomics**         | Hide/show balance, auto-lock with PIN re-entry, history grouped by day with daily totals, 7-day income/expense chart, share the monthly summary to WhatsApp.                       |
| **Money-tracker ergonomics**       | Transaction detail + editing (amounts immutable by design), type/category/date filters, text search,**5-second Undo** on delete, spending-by-category breakdown, CSV export. |
| **Own your data**                  | Encrypted backup/restore file, backup reminders, recovery code, persistent-storage request, installable PWA that opens offline.                                                    |

---

## Architecture

```mermaid
flowchart TB
    subgraph Device["User's device (browser / installed PWA)"]
        UI["Vue 3 UI"]
        ENG["On-device engine<br/>ledger logic, reports, search"]
        VAULT[("IndexedDB<br/>ciphertext only")]
        UI --> ENG --> VAULT
    end

    subgraph Server["Go server (stateless in device mode)"]
        STATIC["Static app + PWA"]
        GW["OCR gateway<br/>no accounts, no database, no storage"]
    end

    OCR["Gemini on Vertex AI<br/>(mock engine locally)"]

    UI -->|"load app"| STATIC
    ENG -->|"receipt photo only"| GW
    GW --> OCR
```

There are two data modes behind one UI function, `api(method, path, body)`:

|                     | **Device mode** (default)       | **Server mode** (optional)                         |
| ------------------- | ------------------------------------- | -------------------------------------------------------- |
| Data location       | Encrypted IndexedDB on the device     | PostgreSQL (field-level encryption + Row Level Security) |
| Accounts            | None; unlocked with a PIN             | Phone + PIN, sessions                                    |
| Server stores data? | **No** (no database, no volume) | Yes                                                      |
| Multi-device        | Via encrypted backup file             | Yes                                                      |
| Device lost         | **Data lost unless backed up**  | Data safe on the server                                  |

The on-device engine mimics the server's REST contract (same JSON shapes, validation rules and error messages), so the UI does not care where data lives. Parity tests on both sides keep the two implementations from drifting.

### Key hierarchy (device mode)

```mermaid
flowchart LR
    PIN["PIN / passphrase"] -->|"PBKDF2-SHA256<br/>600k iterations"| KEKP["KEK (PIN)"]
    REC["Recovery code<br/>160-bit, shown once"] -->|"HKDF"| KEKR["KEK (recovery)"]
    DEK["DEK<br/>random 256-bit"]
    KEKP -->|"wraps"| DEK
    KEKR -->|"wraps"| DEK
    DEK -->|"AES-256-GCM<br/>AAD = type:id"| REC1["Encrypted records<br/>transactions, receipts, settings, photos"]
```

- Every record is sealed with AES-256-GCM, and its **AAD binds it to its slot**, so records cannot be swapped or silently modified.
- Changing the PIN only re-wraps the DEK; no data is re-encrypted.
- Forgot the PIN? The recovery code unwraps the same DEK. Lose both and the data is unrecoverable by anyone, including me.

### How a receipt becomes expenses

```mermaid
sequenceDiagram
    actor U as User
    participant D as Device engine
    participant G as OCR gateway
    participant O as OCR engine

    U->>D: Photo
    D->>D: Validate by content, downscale, re-encode (EXIF/GPS gone)
    D->>G: Photo only
    G->>G: Re-validate, rate limit, daily cap
    G->>O: Extract items
    O-->>G: Untrusted JSON
    G->>G: Normalize: clamp, sanitize, drop invalid items
    G-->>D: Draft items (nothing stored on the server)
    D->>D: Save encrypted photo + draft
    U->>D: Review and correct, then confirm
    D->>D: One atomic write: N expenses (one per category) + receipt confirmed
```

---

## Security design

| Threat                                                | Defence                                                                                                                                                                                                       |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Server breached**                             | In device mode the server holds**no financial data**: no accounts, no database, no storage. What remains is a stateless OCR gateway.                                                                    |
| **Someone holds an unlocked phone**             | Auto-lock (1 / 5 / 15 min or off), manual lock, decrypted data dropped from memory on lock, balance can be hidden.                                                                                            |
| **PIN guessing in the app**                     | Escalating delays after 3 wrong attempts.                                                                                                                                                                     |
| **Browser files copied, then attacked offline** | Contents are ciphertext.**Real protection = PIN length + KDF cost.** A 6-digit PIN can be brute-forced in minutes on a GPU, so the UI shows a strength meter and recommends an 8+ character passphrase. |
| **Tampered or swapped data**                    | AES-GCM authentication + per-record AAD: modification is detected on unlock.                                                                                                                                  |
| **Malicious or corrupt backup file**            | Strict validation, credentials verified**before** local data is replaced, sample decryption test.                                                                                                       |
| **Malicious uploads to the OCR gateway**        | Type detected from content, size/pixel limits (decompression bombs), re-encode to JPEG, hard 5 MB cap.                                                                                                        |
| **Prompt injection via text on a receipt**      | OCR output is untrusted input: sanitised, length-limited, validated; totals recomputed from items; a human always confirms.                                                                                   |
| **Abuse of the account-less OCR gateway**       | Per-IP rate limit, global daily cap (cost guard), optional access code, constant-time comparison.                                                                                                             |
| **CSV formula injection**                       | Cells starting with `= + - @` are neutralised on export.                                                                                                                                                    |
| **XSS**                                         | Strict CSP (no inline scripts/styles), Vue output escaping, self-hosted Vue (no CDN in the supply chain).                                                                                                     |
| **Service worker leaking data**                 | The SW caches only the static app shell;`/v1/*` is never cached.                                                                                                                                            |
| **Compromised container**                       | Distroless non-root image, read-only filesystem, all capabilities dropped, no data volume in device mode.                                                                                                     |

Server mode keeps its earlier defences: field-level AES-256-GCM, argon2id, hashed session tokens, PostgreSQL Row Level Security on every table, a restricted DB role, an immutable ledger trigger, and an audit log.

### Honest limitations

- **No backup = no recovery.** Lose the device without a backup and the data is gone. iOS may evict site data not opened for 7 days unless the app is installed to the home screen. The app mitigates this with backup reminders, a persistent-storage request, and an install prompt, but cannot remove the risk.
- **Offline PIN cracking** against a copied browser profile is only as hard as the PIN is long. PBKDF2 is not memory-hard; Argon2 via WASM would be stronger.
- **Receipt photos leave the device** when OCR is used (device → my gateway → Gemini). The gateway does not store them; how the OCR provider handles data is governed by its terms, so check Vertex AI's retention/training settings before launch.
- **HTTPS is required** (WebCrypto is unavailable on plain-HTTP origins other than `localhost`). The app shows a clear message instead of failing silently.
- The CSP needs `'unsafe-eval'` because Vue compiles templates in the browser; removing it requires pre-compiling templates.
- Text search runs in the browser over loaded history. No automatic multi-device sync.

---

## Reliability

- **Atomic writes.** Every IndexedDB write that spans several rows (transfers, receipt confirmation) is a single transaction; data is encrypted *before* the transaction opens.
- **Graceful shutdown.** On `SIGTERM` readiness flips to "not ready", the process waits for the load balancer to drain, finishes in-flight requests, then exits.
- **Health endpoints** `/healthz` and `/readyz`, plus a `healthcheck` subcommand for distroless Docker.
- **Offline-capable.** The service worker keeps the app shell available without internet; ledger operations never need the network. Only OCR does, and it degrades to manual entry.
- **Bounded resources.** Server timeouts, request deadlines, body limits, concurrency limits around image decoding, rate limiters.
- **Server mode:** deadlock/serialization retry, idempotency keys, advisory-locked migrations.

---

## Local-first, cloud-ready

Everything runs on a laptop: the app, the on-device storage, and a **mock OCR engine** that picks a sample receipt from the image hash. The upload → review → correct → confirm flow is real; only "reading the pixels" is simulated, and the UI says so.

The GCP version is **already in the source as commented lines prefixed with `// CLOUD: `**. Because the server holds no data, the cloud footprint is tiny: **Cloud Run + Vertex AI** (no Cloud SQL, no Cloud Storage for user data).

```bash
go run ./scripts/cloudcheck -list   # how many CLOUD lines per file
go run ./scripts/cloudcheck         # enables every CLOUD line in a temp copy, then go build + go vet
```

> The Gemini model ID is configurable (`VERTEX_MODEL`). Check Google's model lifecycle page for the current stable Flash model and its retirement date before deploying.

---

## Quick start

Requirements: Docker (with Compose). Node 20+ and Go 1.22+ are only needed to run tests.

```bash
git clone https://github.com/<your-username>/smartledger.git
cd smartledger

docker compose up --build -d      # device mode: a single container, no database
open http://localhost:8080
```

On first run, create a PIN and **save the recovery code** shown once. To try the optional server mode (accounts + PostgreSQL):

```bash
# create .env with random secrets (Windows: scripts/init-env.ps1), then:
docker compose -f docker-compose.yml -f docker-compose.server.yml up --build -d
```

To test on a phone, use an HTTPS tunnel (e.g. cloudflared) or `adb reverse tcp:8080 tcp:8080`; plain `http://192.168.x.x` will be refused by the browser's crypto APIs.

---

## Testing

```bash
go mod tidy && go test ./...                 # Go: unit + integration (integration needs the server-mode PostgreSQL)

cd tests/web && npm install
npm run test:local                           # on-device engine: crypto, vault, ledger parity, receipts, backup, CSV
npm run test:device                          # full UI flow against the running device-mode server
BASE_URL=http://127.0.0.1:8080 npm run test:server   # full UI flow against server mode
```

What the suites verify:

- **Crypto & vault:** tamper and slot-swap detection, key rotation, PIN change, recovery code, attempt throttling, and that no plaintext appears in stored bytes or in backup files.
- **Ledger parity:** the on-device engine reproduces the server's results for the cash-leak scenario, pagination, filters, edits, reports and validation.
- **Backups:** export → wipe → restore (PIN and recovery code), wrong credentials leave local data untouched, malicious files rejected, CSV injection neutralised.
- **UI end-to-end (device mode):** onboarding, weak-PIN rejection, lock/unlock, PIN change, forgot-PIN recovery, idle auto-lock, backup, wipe and restore, undo delete, receipt flow, plus the key proof: **the only endpoints called are `/v1/config` and `/v1/ocr/extract`**, never a data endpoint.
- **Gateway & server:** access code, daily cap, hostile uploads, no data endpoints in device mode, PWA assets and CSP; and in server mode, cross-user isolation, encryption at rest, immutable ledger, lockout and rate limiting.

---

## Project structure

```
cmd/api/             entry point: serve | migrate | genkey | healthcheck
internal/
  app/               wiring; device.go = stateless device-mode server; local/cloud backend factory
  receipt/           OCR gateway (no storage), mock/vertex engines, image sanitiser
  auth/ ledger/      server mode: accounts, wallets, transactions (PostgreSQL + RLS)
  platform/          db (pool, RLS context, migrator), httpx (strict JSON, middleware, rate limiter)
  config/            env parsing with fail-fast validation (STORAGE_MODE)
  web/static/        Vue UI + PWA
    assets/local-crypto.js   DEK/KEK, PBKDF2, recovery code, AES-GCM with AAD
    assets/local-db.js       encrypted IndexedDB, vault, attempt throttling
    assets/local-ledger.js   on-device ledger engine (mirrors the REST contract)
    assets/local-backup.js   backup, restore, CSV, storage status
    assets/app.js            UI; one api() for both modes
    sw.js, manifest.webmanifest
migrations/          server-mode schema
tests/web/           on-device engine + UI tests (Node, jsdom, fake-indexeddb)
scripts/             init-env.ps1, cloudcheck
```

---

## Trade-offs I made on purpose

- **Local-first over cloud sync.** It removes the biggest risk (a server full of financial data) at the price of no automatic multi-device sync and a real data-loss risk. I offset that with encrypted backups and reminders instead of hiding it.
- **Two implementations of the ledger rules (Go and JS).** Server mode is kept as an option, so rules live in two places; parity tests guard against drift.
- **Gemini for OCR, not a self-trained model.** Receipt OCR (including handwriting) is a solved problem for large multimodal models, and the per-receipt cost is tiny compared with the effort and accuracy risk of training my own. The OCR engine sits behind a small interface, so it stays swappable.
- **Standard library first.** Server mode uses two runtime dependencies (`lib/pq`, `x/crypto/argon2`); device mode needs none beyond the Go standard library.
- **Vue without a build step.** Approachable and no toolchain, at the cost of `'unsafe-eval'` in the CSP.
- **Amounts are not editable.** Fixing an amount means delete + re-record, keeping the ledger trustworthy.
- **Account-less OCR gateway guarded by caps.** Simple and private, but abuse resistance is rate limits + a daily cap + an optional access code rather than identity.

---

## Showcase

![1792142602232](image/README/1792142602232.png)

![1792142713496](image/README/1792142713496.png)

![1792142728610](image/README/1792142728610.png)

![1792142742312](image/README/1792142742312.png)

![1792142768355](image/README/1792142768355.png)

![1792142791586](image/README/1792142791586.png)

---

## Tech stack

Go 1.22 · Vue 3 (self-hosted) · IndexedDB · WebCrypto (AES-256-GCM, PBKDF2, HKDF) · Service Worker / PWA · Docker Compose · PostgreSQL 16 + Row Level Security (server mode) · Mermaid (docs)

Cloud target: Cloud Run · Vertex AI (Gemini)
