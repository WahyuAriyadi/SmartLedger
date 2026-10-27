# Cloud deployment

Step-by-step guide for deploying Smart Micro-Ledger to Google Cloud Run.

The app only needs the cloud for two things: **being reachable from anywhere** (Cloud Run) and **reading receipts** (a Gemini model). Everything else stays on the user's phone. For how the pieces fit together, see [Cloud architecture](../README.md#cloud-architecture) in the README.

**What you need:** a GCP project with billing enabled (Cloud Run needs it, even though a tiny app costs next to nothing), the [gcloud CLI](https://cloud.google.com/sdk/docs/install) installed and logged in (`gcloud auth login`), and a free Gemini API key from [Google AI Studio](https://aistudio.google.com) (no credit card).

## Pick your OCR backend

The default is the Gemini API with a free key. Compare the options in [Choosing the OCR backend](../README.md#choosing-the-ocr-backend) first. The short version: the free tier is fine for demos and your own receipts, but Google may use what you send to improve its products, so for real shop owners put billing on the key or use `vertex`.

The free quota is set by Google per model and changes over time, so check your actual limits in AI Studio. Keep `OCR_DAILY_CAP` below it. If the quota runs out, Google answers `429` and the app falls back to manual entry, so nobody sees an error.

## Step 1: Try the real OCR from your laptop first (optional, but saves headaches)

This uses the exact same extractor code as the server, so if it passes here it'll pass on Cloud Run.

```powershell
$env:GEMINI_API_KEY = "your-key-from-ai-studio"
go run ./scripts/ocrcheck                                # blank image: checks key + model ID + quota
go run ./scripts/ocrcheck -image C:\photos\nota.jpg     # a real receipt: checks reading quality
```

For Vertex instead: set `VERTEX_PROJECT` and `VERTEX_ACCESS_TOKEN` (from `gcloud auth print-access-token`, valid ~1 hour), then add `-backend vertex`.

Want the whole app on Docker with real OCR? Put `OCR_BACKEND=gemini` and `GEMINI_API_KEY=...` in `.env` and run `docker compose up --build -d` as usual.

## Step 2: Deploy

```powershell
.\scripts\deploy-cloudrun.ps1 -Project your-gcp-project-id
```

It asks for your Gemini key (hidden as you type), enables the needed APIs, creates a service account with **no permissions at all** (the API key is all the app needs), generates a random OCR access code, deploys to Cloud Run in Jakarta (`asia-southeast2`), and prints the URL plus the code. Want Vertex instead? Add `-Backend vertex`; the service account then gets only `roles/aiplatform.user`. Other flags: `-Region`, `-DailyCap`, `-Model`, `-AccessCode`.

Not on Windows? Same thing by hand:

```bash
gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com
gcloud iam service-accounts create smartledger-runtime

gcloud run deploy smartledger --source . --region asia-southeast2 \
  --service-account smartledger-runtime@YOUR_PROJECT.iam.gserviceaccount.com \
  --allow-unauthenticated --min-instances 0 --max-instances 1 \
  --memory 512Mi --concurrency 20 --timeout 60 \
  --set-env-vars "^@^OCR_BACKEND=gemini@GEMINI_API_KEY=YOUR_KEY@OCR_DAILY_CAP=100@TRUSTED_PROXY_HOPS=1@OCR_ACCESS_CODE=pick-a-random-code"
```

For `vertex`, also run `gcloud services enable aiplatform.googleapis.com`, grant the service account `roles/aiplatform.user`, and swap the OCR env vars for `OCR_BACKEND=vertex@VERTEX_PROJECT=YOUR_PROJECT`.

About the key: it lives as a plain env var on the Cloud Run service, so anyone with access to your GCP project can read it. That's the trade-off for not adding Secret Manager. Don't commit it, don't paste it into issues, and if it ever leaks, delete it in AI Studio and make a new one.

## Step 3: Check it works

Open the printed URL (Cloud Run gives you HTTPS, which the app needs for WebCrypto), create a PIN, and save the recovery code. Then paste the access code into the OCR access code field in the app's settings page (the field only shows up when the server has a code set), and scan a receipt. If scanning falls back to manual entry, look at the logs:

```bash
gcloud run services logs read smartledger --region asia-southeast2 --limit 30
```

## What the settings do

Each flag and environment variable is explained in [Runtime configuration](../README.md#runtime-configuration).

## Keeping the bill small

- Cloud Run scales to zero, and nothing else runs when nobody is using the app. With the free Gemini key, OCR costs nothing.
- If you switch to a billed key or Vertex, each scan is one model call on a downscaled JPEG. Prices depend on the model, so check the current pricing page, and keep `OCR_DAILY_CAP` at a number you'd be happy to pay for in the worst case.
- Set a **budget alert** in Cloud Billing. It takes a minute and is the best safety net you have.

## Heads-up: model IDs expire

Gemini models get retired on a schedule (the old default, `gemini-2.5-flash`, was scheduled to retire on Vertex AI on 20 Oct 2026). The default is now `gemini-3.5-flash-lite`, set in `internal/config/config.go`. Before deploying, check Google's model and deprecation pages, and pass `GEMINI_MODEL` / `VERTEX_MODEL` if you want something else. When a model disappears, the symptom is the app quietly falling back to manual entry and a `status 404` line in the logs. `ocrcheck` tells you the same thing in five seconds.

## Troubleshooting

| What you see | Likely cause |
| --- | --- |
| Logs: `status 404` | Model ID is wrong or retired. Change `GEMINI_MODEL` / `VERTEX_MODEL`. |
| Logs: `gemini: status 400` or `403` | API key is wrong, restricted, or the project/key isn't allowed to use that model. |
| Logs: `status 429` | Free quota used up for now. Wait, lower `OCR_DAILY_CAP`, or put billing on the key. |
| Logs: `vertex: status 403` | Service account is missing `roles/aiplatform.user`, or the Vertex AI API isn't enabled. |
| Logs: `vertex: status 401` (local) | `VERTEX_ACCESS_TOKEN` expired. Generate a new one. |
| Logs: `metadata server tidak terjangkau` | You set `OCR_BACKEND=vertex` outside Cloud Run without a token. Set `VERTEX_ACCESS_TOKEN` for local tests. |
| App says "kuota OCR harian sudah habis" | `OCR_DAILY_CAP` reached. Raise it or wait for tomorrow (WIB). |
| Browser refuses to store data | Page isn't on HTTPS. Use the `run.app` URL, not plain HTTP. |

## Taking it down

```bash
gcloud run services delete smartledger --region asia-southeast2
```

Users' data is on their own devices, so deleting the service loses nothing of theirs. The app just stops loading for new visits. Also delete the Gemini API key in AI Studio if you're done with it.

> **Note:** only device mode goes to the cloud. Server mode (accounts + PostgreSQL) is for local or a VPS; running it on Cloud Run would need Cloud SQL, which is exactly what this setup avoids.
