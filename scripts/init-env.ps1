# Membuat file .env berisi rahasia acak (CSPRNG). Tidak menimpa .env yang sudah ada.
# Jalankan dari folder proyek:  powershell -ExecutionPolicy Bypass -File scripts\init-env.ps1
$ErrorActionPreference = 'Stop'
$envFile = Join-Path (Split-Path -Parent $PSScriptRoot) '.env'

if (Test-Path $envFile) {
    Write-Host ".env sudah ada, tidak ditimpa."
    exit 0
}

function New-Secret {
    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    [Convert]::ToBase64String($bytes)
}

# Password database dibuat alfanumerik agar aman dipakai di dalam URL koneksi.
$dbPassword = (New-Secret).Replace('+', 'x').Replace('/', 'y').Replace('=', '')

$lines = @(
    "DATA_KEYS=1:$(New-Secret)",
    "DATA_KEY_ACTIVE=1",
    "PHONE_PEPPER=$(New-Secret)",
    "POSTGRES_PASSWORD=$dbPassword"
)
[IO.File]::WriteAllText($envFile, ($lines -join "`n") + "`n", (New-Object Text.UTF8Encoding($false)))

Write-Host ".env dibuat di $envFile"
Write-Host "PENTING: cadangkan file ini. Kehilangan DATA_KEYS berarti data terenkripsi tidak dapat dibaca lagi."
