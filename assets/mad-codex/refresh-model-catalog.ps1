param([string]$CodexHome)

$ErrorActionPreference = 'Stop'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$utf8Strict = New-Object System.Text.UTF8Encoding($false, $true)
$userHome = [Environment]::GetFolderPath('UserProfile')
if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $CodexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
}
$configPath = Join-Path $CodexHome 'config.toml'
$authPath = Join-Path $CodexHome 'auth.json'
$catalogPath = Join-Path $CodexHome 'madapi-cockpit-model-catalog.json'
$modelsCachePath = Join-Path $CodexHome 'models_cache.json'
if (-not (Test-Path -LiteralPath $configPath)) { exit 0 }

$config = [IO.File]::ReadAllText($configPath, $utf8Strict)
if ($config -notmatch 'https://mad\.myddns\.me/codex/(?:cockpit/)?v1') { exit 0 }
$providerMatch = [regex]::Match($config, '(?m)^\s*model_provider\s*=\s*"([A-Za-z0-9_-]+)"\s*$')
if (-not $providerMatch.Success) { throw 'Active model provider not found in config.toml' }
$providerId = [regex]::Escape($providerMatch.Groups[1].Value)
$providerBlock = [regex]::Match($config, '(?ms)^\s*\[model_providers\.' + $providerId + '\]\s*$.*?(?=^\s*\[|\z)')
$authBlock = [regex]::Match($config, '(?ms)^\s*\[model_providers\.' + $providerId + '\.auth\]\s*$.*?(?=^\s*\[|\z)')
$keyMatch = [regex]::Match($providerBlock.Value, '(?m)^\s*experimental_bearer_token\s*=\s*"([^"]+)"\s*$')
if (-not $keyMatch.Success -and $authBlock.Success) {
    $keyMatch = [regex]::Match($authBlock.Value, "(?m)^\s*args\s*=.*\[Console\]::Out\.Write\('([^']+)'\)")
}
if (-not $keyMatch.Success) { throw 'MadAPI key not found in config.toml' }

$authKind = ([string]$env:MADAPI_CODEX_AUTH_KIND).Trim().ToLowerInvariant()
if ([string]::IsNullOrWhiteSpace($authKind)) {
    $authKind = 'unconfigured'
    if (Test-Path -LiteralPath $authPath) {
        try { $auth = [IO.File]::ReadAllText($authPath, $utf8Strict) | ConvertFrom-Json }
        catch { throw 'Codex Desktop authentication state is unreadable.' }
        $mode = if ($null -eq $auth.PSObject.Properties['auth_mode']) { '' } else { [string]$auth.auth_mode }
        $openAIKey = if ($null -eq $auth.PSObject.Properties['OPENAI_API_KEY']) { '' } else { [string]$auth.OPENAI_API_KEY }
        $tokens = if ($null -eq $auth.PSObject.Properties['tokens']) { $null } else { $auth.tokens }
        $accessToken = if ($null -eq $tokens -or $null -eq $tokens.PSObject.Properties['access_token']) { '' } else { [string]$tokens.access_token }
        $refreshToken = if ($null -eq $tokens -or $null -eq $tokens.PSObject.Properties['refresh_token']) { '' } else { [string]$tokens.refresh_token }
        if ($mode -ne 'apikey' -and -not [string]::IsNullOrWhiteSpace($accessToken) -and -not [string]::IsNullOrWhiteSpace($refreshToken)) {
            $authKind = 'oauth'
        } elseif ($mode -eq 'apikey' -or -not [string]::IsNullOrWhiteSpace($openAIKey)) {
            $authKind = 'apikey'
        }
    }
}
if (@('oauth', 'apikey', 'unconfigured') -notcontains $authKind) { throw 'MADAPI_CODEX_AUTH_KIND is invalid.' }

$catalogUri = if ($authKind -eq 'apikey') { 'https://mad.myddns.me/codex/cockpit/v1/models' } else { 'https://mad.myddns.me/codex/v1/models' }
$desiredBaseUrl = if ($authKind -eq 'apikey') { 'https://mad.myddns.me/codex/cockpit/v1' } else { 'https://mad.myddns.me/codex/v1' }
$desiredAuth = if ($authKind -eq 'apikey') { 'false' } else { 'true' }
$nextProviderBlock = $providerBlock.Value
$nextProviderBlock = [regex]::Replace($nextProviderBlock, '(?m)^(\s*base_url\s*=\s*)"[^"]*"\s*$', '${1}"' + $desiredBaseUrl + '"')
$nextProviderBlock = [regex]::Replace($nextProviderBlock, '(?m)^(\s*requires_openai_auth\s*=\s*)(?:true|false)\s*$', '${1}' + $desiredAuth)
$configChanged = $nextProviderBlock -ne $providerBlock.Value
if ($configChanged) {
    $nextConfig = $config.Substring(0, $providerBlock.Index) + $nextProviderBlock + $config.Substring($providerBlock.Index + $providerBlock.Length)
    $configTempPath = $configPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        [IO.File]::WriteAllText($configTempPath, $nextConfig, $utf8NoBom)
        Move-Item -LiteralPath $configTempPath -Destination $configPath -Force
    } finally {
        if (Test-Path -LiteralPath $configTempPath) { Remove-Item -LiteralPath $configTempPath -Force }
    }
}

if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_REFRESH_RESPONSE_FILE)) {
    $catalog = [IO.File]::ReadAllText([string]$env:MADAPI_REFRESH_RESPONSE_FILE, $utf8Strict) | ConvertFrom-Json
} else {
    $headers = @{ Authorization = 'Bearer ' + $keyMatch.Groups[1].Value }
    $catalog = Invoke-RestMethod -Uri $catalogUri -Headers $headers -Method Get
}
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
if (Test-Path -LiteralPath $modelsCachePath) { Remove-Item -LiteralPath $modelsCachePath -Force }
Write-Output ('MadAPI Codex model catalog refreshed: ' + $authKind + ', ' + $modelCount)
