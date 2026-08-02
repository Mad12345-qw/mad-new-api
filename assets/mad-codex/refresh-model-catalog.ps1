param([string]$CodexHome)

$ErrorActionPreference = 'Stop'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$userHome = [Environment]::GetFolderPath('UserProfile')
if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $CodexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
}
$configPath = Join-Path $CodexHome 'config.toml'
$catalogPath = Join-Path $CodexHome 'madapi-cockpit-model-catalog.json'
if (-not (Test-Path -LiteralPath $configPath)) { exit 0 }

$config = [IO.File]::ReadAllText($configPath)
if ($config -notmatch 'https://mad\.myddns\.me/codex/cockpit/v1') { exit 0 }
$providerMatch = [regex]::Match($config, '(?m)^\s*model_provider\s*=\s*"([A-Za-z0-9_-]+)"\s*$')
if (-not $providerMatch.Success) { throw 'Active model provider not found in config.toml' }
$providerId = [regex]::Escape($providerMatch.Groups[1].Value)
$providerBlock = [regex]::Match($config, '(?ms)^\s*\[model_providers\.' + $providerId + '\]\s*$.*?(?=^\s*\[|\z)')
$authBlock = [regex]::Match($config, '(?ms)^\s*\[model_providers\.' + $providerId + '\.auth\]\s*$.*?(?=^\s*\[|\z)')
$keyMatch = if ($authBlock.Success) {
    [regex]::Match($authBlock.Value, "(?m)^\s*args\s*=.*\[Console\]::Out\.Write\('([^']+)'\)")
} else {
    [regex]::Match($providerBlock.Value, '(?m)^\s*experimental_bearer_token\s*=\s*"([^"]+)"\s*$')
}
if (-not $keyMatch.Success) { throw 'MadAPI key not found in config.toml' }

$headers = @{ Authorization = 'Bearer ' + $keyMatch.Groups[1].Value }
$catalog = Invoke-RestMethod -Uri 'https://mad.myddns.me/codex/cockpit/v1/models' -Headers $headers -Method Get
$modelCount = @($catalog.models).Count
if ($modelCount -lt 1) { throw 'MadAPI returned an empty Codex model catalog' }

New-Item -ItemType Directory -Path $CodexHome -Force | Out-Null
$tempPath = Join-Path $CodexHome ('madapi-cockpit-model-catalog.' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    [IO.File]::WriteAllText($tempPath, ($catalog | ConvertTo-Json -Depth 100), $utf8NoBom)
    Move-Item -LiteralPath $tempPath -Destination $catalogPath -Force
} finally {
    if (Test-Path -LiteralPath $tempPath) { Remove-Item -LiteralPath $tempPath -Force }
}
Write-Output ('MadAPI Codex model catalog refreshed: ' + $modelCount)
