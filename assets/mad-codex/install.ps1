$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey)) {
    throw 'MADAPI_KEY is missing.'
}
if ($apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY contains unsupported characters.'
}

$userHome = [Environment]::GetFolderPath('UserProfile')
$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) {
    Join-Path $userHome '.codex'
} else {
    [string]$env:CODEX_HOME
}
$configPath = Join-Path $codexHome 'config.toml'
$tempPath = Join-Path $codexHome 'config.toml.madapi.tmp'
$backupPath = $null

New-Item -ItemType Directory -Path $codexHome -Force | Out-Null

$sourceLines = @()
if (Test-Path -LiteralPath $configPath) {
    $backupPath = '{0}.madapi-backup-{1}' -f $configPath, (Get-Date -Format 'yyyyMMdd-HHmmss')
    Copy-Item -LiteralPath $configPath -Destination $backupPath -Force
    $sourceLines = @(Get-Content -LiteralPath $configPath)
}

$keptLines = New-Object 'System.Collections.Generic.List[string]'
$currentSection = ''
$skipMadApiSection = $false

foreach ($line in $sourceLines) {
    if ($line -match '^\s*\[([^]]+)\]\s*(?:#.*)?$') {
        $currentSection = $Matches[1].Trim()
        $skipMadApiSection =
            $currentSection -eq 'model_providers.madapi' -or
            $currentSection.StartsWith('model_providers.madapi.')
        if ($skipMadApiSection) {
            continue
        }
    }

    if ($skipMadApiSection) {
        continue
    }

    if (
        $currentSection -eq '' -and
        $line -match '^\s*(model_provider|model)\s*='
    ) {
        continue
    }

    $keptLines.Add($line)
}

$providerBlock = @"
model_provider = "madapi"
model = "gpt-5.6-sol"

$($keptLines -join [Environment]::NewLine)

[model_providers.madapi]
name = "MadAPI"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
stream_idle_timeout_ms = 360000
request_max_retries = 0
context_window_override = 1048576

[model_providers.madapi.auth]
command = "powershell.exe"
args = ["-NoProfile", "-Command", "[Console]::Out.Write('$apiKey')"]
timeout_ms = 5000
refresh_interval_ms = 300000
"@

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText($tempPath, $providerBlock.Trim() + [Environment]::NewLine, $utf8NoBom)
Move-Item -LiteralPath $tempPath -Destination $configPath -Force

Write-Host "MadAPI Codex configuration installed: $configPath"
if ($null -ne $backupPath) {
    Write-Host "Backup created: $backupPath"
}
Write-Host 'Restart Codex to load the model catalog.'
