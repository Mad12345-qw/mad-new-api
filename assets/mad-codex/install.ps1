$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function ConvertTo-TomlBasicString([string]$Value) {
    $escaped = $Value.Replace('\', '\\').Replace('"', '\"')
    $escaped = $escaped.Replace("`r", '\r').Replace("`n", '\n').Replace("`t", '\t')
    return '"' + $escaped + '"'
}

function Get-RootTomlString([string[]]$Lines, [string]$Key) {
    foreach ($line in $Lines) {
        if ($line -match '^\s*\[') { break }
        if ($line -match ('^\s*' + [regex]::Escape($Key) + '\s*=\s*"([^"]+)"')) {
            return $Matches[1]
        }
    }
    return $null
}

function Get-ProviderDisplayName([string[]]$Lines, [string]$ProviderId) {
    $targetSection = 'model_providers.' + $ProviderId
    $currentSection = ''
    foreach ($line in $Lines) {
        if ($line -match '^\s*\[([^]]+)\]\s*(?:#.*)?$') {
            $currentSection = $Matches[1].Trim()
            continue
        }
        if ($currentSection -eq $targetSection -and $line -match '^\s*name\s*=\s*"([^"]+)"') {
            return $Matches[1]
        }
    }
    return $null
}

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey) -or $apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY is missing or invalid.'
}

$userHome = [Environment]::GetFolderPath('UserProfile')
$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
$configPath = Join-Path $codexHome 'config.toml'
$authPath = Join-Path $codexHome 'auth.json'
$modelsCachePath = Join-Path $codexHome 'models_cache.json'
$transactionId = [guid]::NewGuid().ToString('N')
$tempConfigPath = Join-Path $codexHome ("config.toml.madapi.$transactionId.tmp")
$tempAuthPath = Join-Path $codexHome ("auth.json.madapi.$transactionId.tmp")
$backupPath = $null
$authBackupPath = $null
$hadConfig = Test-Path -LiteralPath $configPath
$hadAuth = Test-Path -LiteralPath $authPath
$configInstalled = $false
$authInstalled = $false

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$utf8Strict = New-Object System.Text.UTF8Encoding($false, $true)
$authKind = 'apikey'
if ($hadAuth) {
    try {
        $existingAuth = [IO.File]::ReadAllText($authPath, $utf8Strict) | ConvertFrom-Json
    } catch {
        throw 'Codex Desktop authentication state is unreadable. No files were changed.'
    }
    $modeProperty = $existingAuth.PSObject.Properties['auth_mode']
    $tokensProperty = $existingAuth.PSObject.Properties['tokens']
    $existingMode = if ($null -eq $modeProperty) { '' } else { [string]$modeProperty.Value }
    $existingTokens = if ($null -eq $tokensProperty) { $null } else { $tokensProperty.Value }
    $accessProperty = if ($null -eq $existingTokens) { $null } else { $existingTokens.PSObject.Properties['access_token'] }
    $refreshProperty = if ($null -eq $existingTokens) { $null } else { $existingTokens.PSObject.Properties['refresh_token'] }
    $existingAccessToken = if ($null -eq $accessProperty) { '' } else { [string]$accessProperty.Value }
    $existingRefreshToken = if ($null -eq $refreshProperty) { '' } else { [string]$refreshProperty.Value }
    $hasOAuthTokens = -not [string]::IsNullOrWhiteSpace($existingAccessToken) -and -not [string]::IsNullOrWhiteSpace($existingRefreshToken)
    if ($existingMode -ne 'apikey' -and $hasOAuthTokens) {
        $authKind = 'oauth'
    } elseif ($existingMode -eq 'chatgpt') {
        throw 'The existing ChatGPT OAuth session is incomplete. Sign in again or sign out before using API Key setup. No files were changed.'
    }
}

New-Item -ItemType Directory -Path $codexHome -Force | Out-Null
$sourceLines = @()
if ($hadConfig) { $sourceLines = @([IO.File]::ReadAllLines($configPath, $utf8Strict)) }

$providerSourceLines = $sourceLines
$providerId = Get-RootTomlString $sourceLines 'model_provider'
if ([string]::IsNullOrWhiteSpace($providerId) -or $providerId -eq 'madapi') {
    $backupPattern = ([IO.Path]::GetFileName($configPath)) + '.madapi-backup-*'
    foreach ($backup in @(Get-ChildItem -LiteralPath $codexHome -Filter $backupPattern -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)) {
        $backupLines = @([IO.File]::ReadAllLines($backup.FullName, $utf8Strict))
        $candidate = Get-RootTomlString $backupLines 'model_provider'
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and $candidate -ne 'madapi') {
            $providerId = $candidate
            $providerSourceLines = $backupLines
            break
        }
    }
}
if ([string]::IsNullOrWhiteSpace($providerId) -or @('openai', 'ollama', 'lmstudio', 'amazon-bedrock') -contains $providerId) {
    $providerId = 'custom'
    $providerSourceLines = @()
}
if ($providerId -notmatch '^[A-Za-z0-9_-]+$') { throw 'The existing model provider identifier is not supported. No files were changed.' }
$providerDisplayName = Get-ProviderDisplayName $providerSourceLines $providerId
if ([string]::IsNullOrWhiteSpace($providerDisplayName)) { $providerDisplayName = $providerId }
$targetProviderSection = 'model_providers.' + $providerId
$authCommand = "[Console]::Out.Write('$apiKey')"

$keptLines = New-Object 'System.Collections.Generic.List[string]'
$currentSection = ''
$skipSection = $false
foreach ($line in $sourceLines) {
    if ($line -match '^\s*\[([^]]+)\]\s*(?:#.*)?$') {
        $currentSection = $Matches[1].Trim()
        $skipSection = $currentSection -eq 'model_providers.madapi' -or $currentSection.StartsWith('model_providers.madapi.') -or $currentSection -eq $targetProviderSection -or $currentSection.StartsWith($targetProviderSection + '.')
        if ($skipSection) { continue }
    }
    if ($skipSection) { continue }
    if ($currentSection -eq '') {
        $assignmentIndex = $line.IndexOf('=')
        if ($assignmentIndex -ge 0) {
            $rootKey = $line.Substring(0, $assignmentIndex).Trim().Trim('"', "'")
            if (@('model_provider', 'model_catalog_json') -contains $rootKey) { continue }
        }
    }
    $keptLines.Add($line)
}

$configLines = New-Object 'System.Collections.Generic.List[string]'
$configLines.Add('model_provider = ' + (ConvertTo-TomlBasicString $providerId))
if (-not $hadConfig) {
    $configLines.Add('model = "gpt-5.6-sol"')
    $configLines.Add('model_reasoning_effort = "high"')
    $configLines.Add('model_auto_compact_token_limit = 500000')
}
$configLines.Add('')
foreach ($line in $keptLines) { $configLines.Add($line) }
$configLines.Add('')
$configLines.Add('[' + $targetProviderSection + ']')
$configLines.Add('name = ' + (ConvertTo-TomlBasicString $providerDisplayName))
$configLines.Add('base_url = "https://mad.myddns.me/codex/v1"')
$configLines.Add('wire_api = "responses"')
$configLines.Add('requires_openai_auth = ' + $(if ($authKind -eq 'oauth') { 'true' } else { 'false' }))
if ($authKind -eq 'oauth') {
    $configLines.Add('experimental_bearer_token = ' + (ConvertTo-TomlBasicString $apiKey))
}
$configLines.Add('stream_idle_timeout_ms = 360000')
$configLines.Add('request_max_retries = 3')
$configLines.Add('context_window_override = 1048576')
if ($authKind -eq 'apikey') {
    $configLines.Add('')
    $configLines.Add('[' + $targetProviderSection + '.auth]')
    $configLines.Add('command = "powershell.exe"')
    $configLines.Add('args = ["-NoProfile", "-NonInteractive", "-Command", ' + (ConvertTo-TomlBasicString $authCommand) + ']')
    $configLines.Add('timeout_ms = 5000')
    $configLines.Add('refresh_interval_ms = 300000')
}

try {
    [IO.File]::WriteAllText($tempConfigPath, (($configLines -join [Environment]::NewLine).Trim() + [Environment]::NewLine), $utf8NoBom)
    if ($authKind -eq 'apikey') {
        $apiAuth = [ordered]@{ auth_mode = 'apikey'; OPENAI_API_KEY = $apiKey } | ConvertTo-Json -Compress
        [IO.File]::WriteAllText($tempAuthPath, $apiAuth, $utf8NoBom)
    }
    if ($hadConfig) {
        $backupPath = '{0}.madapi-backup-{1}' -f $configPath, (Get-Date -Format 'yyyyMMdd-HHmmss-fff')
        [IO.File]::Copy($configPath, $backupPath, $false)
    }
    if ($authKind -eq 'apikey' -and $hadAuth) {
        $authBackupPath = '{0}.madapi-backup-{1}' -f $authPath, (Get-Date -Format 'yyyyMMdd-HHmmss-fff')
        [IO.File]::Copy($authPath, $authBackupPath, $false)
    }
    if ($authKind -eq 'apikey') {
        Move-Item -LiteralPath $tempAuthPath -Destination $authPath -Force
        $authInstalled = $true
    }
    Move-Item -LiteralPath $tempConfigPath -Destination $configPath -Force
    $configInstalled = $true
    if (Test-Path -LiteralPath $modelsCachePath) { Remove-Item -LiteralPath $modelsCachePath -Force }
} catch {
    if ($configInstalled) {
        if ($hadConfig -and $null -ne $backupPath -and (Test-Path -LiteralPath $backupPath)) { [IO.File]::Copy($backupPath, $configPath, $true) }
        elseif (-not $hadConfig -and (Test-Path -LiteralPath $configPath)) { Remove-Item -LiteralPath $configPath -Force }
    }
    if ($authInstalled) {
        if ($hadAuth -and $null -ne $authBackupPath -and (Test-Path -LiteralPath $authBackupPath)) { [IO.File]::Copy($authBackupPath, $authPath, $true) }
        elseif (-not $hadAuth -and (Test-Path -LiteralPath $authPath)) { Remove-Item -LiteralPath $authPath -Force }
    }
    throw
} finally {
    if (Test-Path -LiteralPath $tempConfigPath) { Remove-Item -LiteralPath $tempConfigPath -Force }
    if (Test-Path -LiteralPath $tempAuthPath) { Remove-Item -LiteralPath $tempAuthPath -Force }
}

Write-Host "MadAPI Codex desktop configuration installed: $configPath"
if ($null -ne $backupPath) { Write-Host "Backup created: $backupPath" }
if ($null -ne $authBackupPath) { Write-Host "Authentication backup created: $authBackupPath" }
if ($authKind -eq 'oauth') { Write-Host 'Existing ChatGPT OAuth session preserved.' } else { Write-Host 'Codex Desktop API Key sign-in configured.' }
Write-Host 'Restart Codex Desktop to refresh the model list.'
