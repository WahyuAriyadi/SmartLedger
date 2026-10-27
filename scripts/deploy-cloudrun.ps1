<#
.SYNOPSIS
  Deploys Smart Micro-Ledger to Google Cloud Run in one command.
  OCR backend: Gemini API (default, free tier available) or Vertex AI (paid, data not used for model training).

.EXAMPLE
  .\scripts\deploy-cloudrun.ps1 -Project my-gcp-project
  Prompts for GEMINI_API_KEY (create one for free at https://aistudio.google.com).

.EXAMPLE
  .\scripts\deploy-cloudrun.ps1 -Project my-gcp-project -Backend vertex -DailyCap 200

.NOTES
  Requires the gcloud CLI, logged in (`gcloud auth login`), and an existing GCP project with billing enabled
  (Cloud Run needs billing; the Gemini free tier does not, but the project is still required).
  Idempotent: if the service already exists it is updated and redeployed.

  Privacy: with -Backend gemini and a key without billing (free tier), Google may use receipt photos to improve
  its products. Suitable for demos and trials; use a billed key or -Backend vertex for real user data.
#>
param(
  [Parameter(Mandatory = $true)][string]$Project,
  [ValidateSet("gemini", "vertex")][string]$Backend = "gemini",
  [string]$Region = "asia-southeast2",          # Jakarta: closest to users in Indonesia
  [string]$Service = "smartledger",
  [string]$GeminiApiKey = "",                   # empty = prompted at run time (hidden as you type)
  [string]$Model = "",                          # empty = code default (model IDs are retired periodically)
  [string]$VertexLocation = "global",           # only for -Backend vertex
  [int]$DailyCap = -1,                          # max scans per day in total; default 100 (gemini) / 300 (vertex)
  [string]$AccessCode = "",                     # empty = a random code is generated; share it with users allowed to scan
  [switch]$NoAccessCode                         # public app without an access code (not recommended)
)
$ErrorActionPreference = "Stop"

function Run { param([string]$Desc, [scriptblock]$Cmd)
  Write-Host ">> $Desc" -ForegroundColor Cyan
  & $Cmd
  if ($LASTEXITCODE -ne 0) { throw "Gagal: $Desc (kode $LASTEXITCODE)" }
}

if (-not (Get-Command gcloud -ErrorAction SilentlyContinue)) {
  throw "gcloud belum terpasang. Pasang dulu: https://cloud.google.com/sdk/docs/install"
}
if ($DailyCap -lt 0) { $DailyCap = if ($Backend -eq "gemini") { 100 } else { 300 } }

if ($Backend -eq "gemini" -and $GeminiApiKey -eq "") {
  $secure = Read-Host "Tempel GEMINI_API_KEY (dari aistudio.google.com)" -AsSecureString
  $GeminiApiKey = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
  if ($GeminiApiKey -eq "") { throw "GEMINI_API_KEY wajib diisi untuk -Backend gemini" }
}

Run "Memilih project $Project" { gcloud config set project $Project --quiet }

# Cloud Build + Artifact Registry are used only while building the image, not at runtime.
$apis = @("run.googleapis.com", "cloudbuild.googleapis.com", "artifactregistry.googleapis.com")
if ($Backend -eq "vertex") { $apis += "aiplatform.googleapis.com" }
Run "Mengaktifkan API yang dibutuhkan" { gcloud services enable @apis --quiet }

# Dedicated service account for this app: no roles for gemini (the API key is enough);
# roles/aiplatform.user only for vertex.
$sa = "$Service-runtime"
$saEmail = "$sa@$Project.iam.gserviceaccount.com"
gcloud iam service-accounts describe $saEmail --quiet 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) {
  Run "Membuat service account $saEmail" {
    gcloud iam service-accounts create $sa --display-name "Smart Micro-Ledger runtime" --quiet
  }
}
if ($Backend -eq "vertex") {
  Run "Memberi peran roles/aiplatform.user (hanya itu)" {
    gcloud projects add-iam-policy-binding $Project --member "serviceAccount:$saEmail" --role roles/aiplatform.user --condition=None --quiet | Out-Null
  }
}

# OCR access code: with no accounts, this is the only gate besides the daily cap and rate limit.
if ($NoAccessCode) { $AccessCode = "" }
elseif ($AccessCode -eq "") {
  $bytes = New-Object byte[] 12
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
  $AccessCode = ([Convert]::ToBase64String($bytes)).TrimEnd("=").Replace("+", "A").Replace("/", "B")
}

# Env values use the "^@^" delimiter so any character is safe. --max-instances=1 makes the daily cap and
# in-memory rate limits global, and also bounds cost.
$envVars = @("OCR_BACKEND=$Backend", "OCR_DAILY_CAP=$DailyCap", "TRUSTED_PROXY_HOPS=1")
if ($Backend -eq "gemini") {
  $envVars += "GEMINI_API_KEY=$GeminiApiKey"
  if ($Model -ne "") { $envVars += "GEMINI_MODEL=$Model" }
} else {
  $envVars += "VERTEX_PROJECT=$Project", "VERTEX_LOCATION=$VertexLocation"
  if ($Model -ne "") { $envVars += "VERTEX_MODEL=$Model" }
}
if ($AccessCode -ne "") { $envVars += "OCR_ACCESS_CODE=$AccessCode" }
$envArg = "^@^" + ($envVars -join "@")

Run "Build + deploy ke Cloud Run ($Region)" {
  gcloud run deploy $Service --source . --region $Region `
    --service-account $saEmail --allow-unauthenticated `
    --min-instances 0 --max-instances 1 --memory 512Mi --cpu 1 --concurrency 20 --timeout 60 `
    --set-env-vars $envArg --quiet
}

$url = gcloud run services describe $Service --region $Region --format "value(status.url)"
Write-Host ""
Write-Host "Selesai! Aplikasi bisa dibuka di:" -ForegroundColor Green
Write-Host "  $url"
if ($AccessCode -ne "") {
  Write-Host ""
  Write-Host "Kode akses scan struk (simpan, jangan di-commit ke GitHub):" -ForegroundColor Yellow
  Write-Host "  $AccessCode"
}
if ($Backend -eq "gemini") {
  Write-Host ""
  Write-Host "Ingat: kunci Gemini tersimpan sebagai env var di Cloud Run (terlihat oleh yang punya akses ke project)." -ForegroundColor Yellow
}
Write-Host ""
Write-Host "Cek log kalau scan struk gagal:  gcloud run services logs read $Service --region $Region --limit 30"
