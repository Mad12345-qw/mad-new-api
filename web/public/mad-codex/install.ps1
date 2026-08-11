$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function ConvertTo-TomlBasicString([string]$Value) {
    $escaped = $Value.Replace('\', '\\').Replace('"', '\"')
    $escaped = $escaped.Replace("`r", '\r').Replace("`n", '\n').Replace("`t", '\t')
    return '"' + $escaped + '"'
}

function ConvertTo-XmlText([string]$Value) {
    return [Security.SecurityElement]::Escape($Value)
}

function Write-HiddenRefreshLauncher([string]$LauncherPath, [string]$RefreshScriptPath) {
    $powerShellPath = Join-Path $PSHOME 'powershell.exe'
    $command = '"' + $powerShellPath + '" -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $RefreshScriptPath + '"'
    $escapedCommand = $command.Replace('"', '""')
    $launcher = "Option Explicit`r`nDim shell`r`nSet shell = CreateObject(`"WScript.Shell`")`r`nWScript.Quit shell.Run(`"$escapedCommand`", 0, True)`r`n"
    [IO.File]::WriteAllText($LauncherPath, $launcher, [Text.Encoding]::Unicode)
}

function Register-CatalogRefreshTask([string]$RefreshLauncherPath) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $sid = $identity.User.Value
    $taskName = 'MadAPI Codex Model Catalog Refresh - ' + $sid
    $arguments = '//B //NoLogo "' + $RefreshLauncherPath + '"'
    $taskXml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>$(ConvertTo-XmlText $sid)</UserId></LogonTrigger>
  </Triggers>
  <Principals><Principal id="Author"><UserId>$(ConvertTo-XmlText $sid)</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><StartWhenAvailable>true</StartWhenAvailable><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>true</Hidden><ExecutionTimeLimit>PT2M</ExecutionTimeLimit></Settings>
  <Actions Context="Author"><Exec><Command>wscript.exe</Command><Arguments>$(ConvertTo-XmlText $arguments)</Arguments></Exec></Actions>
</Task>
"@
    $taskXmlPath = Join-Path ([IO.Path]::GetTempPath()) ('madapi-codex-task-' + [guid]::NewGuid().ToString('N') + '.xml')
    try {
        [IO.File]::WriteAllText($taskXmlPath, $taskXml, [Text.Encoding]::Unicode)
        & schtasks.exe /Create /TN $taskName /XML $taskXmlPath /F | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Unable to register the MadAPI model catalog refresh task.' }
    } finally {
        if (Test-Path -LiteralPath $taskXmlPath) { Remove-Item -LiteralPath $taskXmlPath -Force }
    }
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

function Close-CodexDesktop {
    $processes = @(Get-Process -Name 'Codex' -ErrorAction SilentlyContinue)
    if ($processes.Count -eq 0) { return }
    foreach ($process in $processes) { [void]$process.CloseMainWindow() }
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline -and @(Get-Process -Name 'Codex' -ErrorAction SilentlyContinue).Count -gt 0) { Start-Sleep -Milliseconds 250 }
    $remaining = @(Get-Process -Name 'Codex' -ErrorAction SilentlyContinue)
    if ($remaining.Count -gt 0) {
        $remaining | Stop-Process -Force
        Start-Sleep -Milliseconds 500
    }
}

function Restore-HistoryBackup([string]$CodexHome, [string]$HistoryBackupPath) {
    if ([string]::IsNullOrWhiteSpace($HistoryBackupPath) -or -not (Test-Path -LiteralPath $HistoryBackupPath -PathType Container)) { return }
    foreach ($name in @('session_index.jsonl', '.codex-global-state.json', 'state_5.sqlite', 'state_5.sqlite-wal', 'state_5.sqlite-shm')) {
        $source = Join-Path $HistoryBackupPath $name
        $destination = Join-Path $CodexHome $name
        if (Test-Path -LiteralPath $source) { [IO.File]::Copy($source, $destination, $true) }
        elseif (Test-Path -LiteralPath $destination) { Remove-Item -LiteralPath $destination -Force }
    }
}

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey) -or $apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY is missing or invalid.'
}
$gatewayKeyEnvName = 'MADAPI_API_KEY'
$madapiBaseUrl = ([string]$env:MADAPI_BASE_URL).Trim().TrimEnd('/')
if ([string]::IsNullOrWhiteSpace($madapiBaseUrl)) { $madapiBaseUrl = 'https://mad.myddns.me' }
if ($madapiBaseUrl -notmatch '^https?://[^/]+(?::[0-9]+)?$') { throw 'MADAPI_BASE_URL is invalid.' }
$codexBaseUrl = $madapiBaseUrl + '/codex/v1'
$requestedLoginMode = ([string]$env:MADAPI_CODEX_LOGIN_MODE).Trim().ToLowerInvariant()
if ([string]::IsNullOrWhiteSpace($requestedLoginMode)) { $requestedLoginMode = 'auto' }
if (@('auto', 'oauth', 'apikey') -notcontains $requestedLoginMode) {
    throw 'MADAPI_CODEX_LOGIN_MODE must be auto, oauth, or apikey.'
}

$userHome = [Environment]::GetFolderPath('UserProfile')
$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
$configPath = Join-Path $codexHome 'config.toml'
$authPath = Join-Path $codexHome 'auth.json'
$modelsCachePath = Join-Path $codexHome 'models_cache.json'
$catalogPath = Join-Path $codexHome 'madapi-cockpit-model-catalog.json'
$refreshScriptPath = Join-Path $codexHome 'madapi-refresh-model-catalog.ps1'
$refreshLauncherPath = Join-Path $codexHome 'madapi-refresh-model-catalog.vbs'
$historyScriptPath = Join-Path $codexHome 'madapi-restore-history.ps1'
$transactionId = [guid]::NewGuid().ToString('N')
$tempConfigPath = Join-Path $codexHome ("config.toml.madapi.$transactionId.tmp")
$tempRefreshPath = Join-Path $codexHome ("madapi-refresh-model-catalog.$transactionId.ps1")
$tempRefreshLauncherPath = Join-Path $codexHome ("madapi-refresh-model-catalog.$transactionId.vbs")
$tempHistoryPath = Join-Path $codexHome ("madapi-restore-history.$transactionId.ps1")
$tempCatalogPath = Join-Path $codexHome ("madapi-cockpit-model-catalog.$transactionId.tmp")
$tempAuthPath = Join-Path $codexHome ("auth.json.madapi.$transactionId.tmp")
$backupPath = $null
$authBackupPath = $null
$historyBackupPath = $null
$hadConfig = Test-Path -LiteralPath $configPath
$hadAuth = Test-Path -LiteralPath $authPath
$configInstalled = $false
$authChanged = $false
$testMode = [string]$env:MADAPI_INSTALL_TEST_MODE -eq '1'

if (-not $testMode) { Close-CodexDesktop }

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$utf8Strict = New-Object System.Text.UTF8Encoding($false, $true)
$authKind = 'unconfigured'
if ($hadAuth) {
    try {
        $existingAuth = [IO.File]::ReadAllText($authPath, $utf8Strict) | ConvertFrom-Json
    } catch {
        throw 'Codex Desktop authentication state is unreadable. No files were changed.'
    }
    $modeProperty = $existingAuth.PSObject.Properties['auth_mode']
    $apiKeyProperty = $existingAuth.PSObject.Properties['OPENAI_API_KEY']
    $tokensProperty = $existingAuth.PSObject.Properties['tokens']
    $existingMode = if ($null -eq $modeProperty) { '' } else { [string]$modeProperty.Value }
    $existingApiKey = if ($null -eq $apiKeyProperty) { '' } else { [string]$apiKeyProperty.Value }
    $existingTokens = if ($null -eq $tokensProperty) { $null } else { $tokensProperty.Value }
    $accessProperty = if ($null -eq $existingTokens) { $null } else { $existingTokens.PSObject.Properties['access_token'] }
    $refreshProperty = if ($null -eq $existingTokens) { $null } else { $existingTokens.PSObject.Properties['refresh_token'] }
    $existingAccessToken = if ($null -eq $accessProperty) { '' } else { [string]$accessProperty.Value }
    $existingRefreshToken = if ($null -eq $refreshProperty) { '' } else { [string]$refreshProperty.Value }
    $hasOAuthTokens = -not [string]::IsNullOrWhiteSpace($existingAccessToken) -and -not [string]::IsNullOrWhiteSpace($existingRefreshToken)
    if ($existingMode -ne 'apikey' -and $hasOAuthTokens) {
        $authKind = 'oauth'
    } elseif ($existingMode -eq 'apikey' -or -not [string]::IsNullOrWhiteSpace($existingApiKey)) {
        $authKind = 'apikey'
    }
}
$existingAuthKind = $authKind
$authMutation = 'none'
if ($requestedLoginMode -eq 'oauth') {
    $authKind = 'oauth'
} elseif ($requestedLoginMode -eq 'apikey') {
    $authKind = 'apikey'
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
$keptLines = New-Object 'System.Collections.Generic.List[string]'
$currentSection = ''
$skipSection = $false
$featuresSectionFound = $false
foreach ($line in $sourceLines) {
    if ($line -match '^\s*\[([^]]+)\]\s*(?:#.*)?$') {
        $currentSection = $Matches[1].Trim()
        $skipSection = $currentSection -eq 'model_providers.madapi' -or $currentSection.StartsWith('model_providers.madapi.') -or $currentSection -eq $targetProviderSection -or $currentSection.StartsWith($targetProviderSection + '.')
        if ($skipSection) { continue }
        if ($currentSection -eq 'features') {
            $featuresSectionFound = $true
            $keptLines.Add($line)
            $keptLines.Add('image_generation = true')
            continue
        }
    }
    if ($skipSection) { continue }
    if ($currentSection -eq 'features' -and $line -match '^\s*image_generation\s*=') { continue }
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
$configLines.Add('model_catalog_json = "madapi-cockpit-model-catalog.json"')
if (-not $hadConfig) {
    $configLines.Add('model = "gpt-5.6-sol"')
    $configLines.Add('model_reasoning_effort = "high"')
    $configLines.Add('model_auto_compact_token_limit = 500000')
}
$configLines.Add('')
foreach ($line in $keptLines) { $configLines.Add($line) }
$configLines.Add('')
if (-not $featuresSectionFound) {
    $configLines.Add('[features]')
    $configLines.Add('image_generation = true')
    $configLines.Add('')
}
$configLines.Add('')
$configLines.Add('[' + $targetProviderSection + ']')
$configLines.Add('name = ' + (ConvertTo-TomlBasicString $providerDisplayName))
$configLines.Add('base_url = ' + (ConvertTo-TomlBasicString $codexBaseUrl))
$configLines.Add('wire_api = "responses"')
$configLines.Add('requires_openai_auth = false')
$configLines.Add('env_key = ' + (ConvertTo-TomlBasicString $gatewayKeyEnvName))
$configLines.Add('http_headers = { "x-openai-actor-authorization" = "madapi-gateway" }')
$configLines.Add('supports_websockets = false')
$configLines.Add('stream_idle_timeout_ms = 360000')
$configLines.Add('request_max_retries = 3')
$configLines.Add('context_window_override = 1048576')

try {
    [IO.File]::WriteAllText($tempConfigPath, (($configLines -join [Environment]::NewLine).Trim() + [Environment]::NewLine), $utf8NoBom)
    $refreshSource = [string]$env:MADAPI_REFRESH_SCRIPT_SOURCE
    if ([string]::IsNullOrWhiteSpace($refreshSource) -and -not [string]::IsNullOrWhiteSpace($PSScriptRoot)) {
        $candidateSource = Join-Path $PSScriptRoot 'refresh-model-catalog.ps1'
        if (Test-Path -LiteralPath $candidateSource) { $refreshSource = $candidateSource }
    }
    if (-not [string]::IsNullOrWhiteSpace($refreshSource)) {
        [IO.File]::WriteAllBytes($tempRefreshPath, [IO.File]::ReadAllBytes($refreshSource))
    } elseif ($testMode) {
        throw 'MADAPI_REFRESH_SCRIPT_SOURCE is required in installer test mode.'
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri ($madapiBaseUrl + '/mad-codex/refresh-model-catalog.ps1') -OutFile $tempRefreshPath
    }
    if (-not (Test-Path -LiteralPath $tempRefreshPath) -or (Get-Item -LiteralPath $tempRefreshPath).Length -lt 100) {
        throw 'The MadAPI model catalog refresh script is invalid.'
    }
    $historySource = [string]$env:MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE
    if ([string]::IsNullOrWhiteSpace($historySource) -and -not [string]::IsNullOrWhiteSpace($PSScriptRoot)) {
        $candidateHistorySource = Join-Path $PSScriptRoot 'restore-history.ps1'
        if (Test-Path -LiteralPath $candidateHistorySource) { $historySource = $candidateHistorySource }
    }
    if (-not [string]::IsNullOrWhiteSpace($historySource)) {
        [IO.File]::WriteAllBytes($tempHistoryPath, [IO.File]::ReadAllBytes($historySource))
    } elseif ($testMode) {
        throw 'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE is required in installer test mode.'
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri ($madapiBaseUrl + '/mad-codex/restore-history.ps1') -OutFile $tempHistoryPath
    }
    if (-not (Test-Path -LiteralPath $tempHistoryPath) -or (Get-Item -LiteralPath $tempHistoryPath).Length -lt 100) {
        throw 'The MadAPI history restore script is invalid.'
    }
    Write-HiddenRefreshLauncher $tempRefreshLauncherPath $refreshScriptPath
    if ($testMode) {
        [IO.File]::WriteAllText($tempCatalogPath, '{"models":[{"slug":"gpt-5.6-sol","display_name":"gpt-5.6-sol"}]}', $utf8NoBom)
    } else {
        $stagingHome = Join-Path $codexHome ('madapi-catalog-stage-' + $transactionId)
        New-Item -ItemType Directory -Path $stagingHome -Force | Out-Null
        try {
            [IO.File]::Copy($tempConfigPath, (Join-Path $stagingHome 'config.toml'), $true)
            $oldGatewayKey = [string]$env:MADAPI_API_KEY
            $oldBaseUrl = [string]$env:MADAPI_BASE_URL
            try {
                $env:MADAPI_API_KEY = $apiKey
                $env:MADAPI_BASE_URL = $madapiBaseUrl
                & "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $tempRefreshPath -CodexHome $stagingHome
            } finally {
                $env:MADAPI_API_KEY = $oldGatewayKey
                $env:MADAPI_BASE_URL = $oldBaseUrl
            }
            if ($LASTEXITCODE -ne 0) { throw 'Unable to download the initial MadAPI model catalog.' }
            Move-Item -LiteralPath (Join-Path $stagingHome 'madapi-cockpit-model-catalog.json') -Destination $tempCatalogPath -Force
        } finally {
            if (Test-Path -LiteralPath $stagingHome) { Remove-Item -LiteralPath $stagingHome -Recurse -Force }
        }
    }
    if ($hadConfig) {
        $backupPath = '{0}.madapi-backup-{1}' -f $configPath, (Get-Date -Format 'yyyyMMdd-HHmmss-fff')
        [IO.File]::Copy($configPath, $backupPath, $false)
    }
    Move-Item -LiteralPath $tempConfigPath -Destination $configPath -Force
    $configInstalled = $true
    Move-Item -LiteralPath $tempRefreshPath -Destination $refreshScriptPath -Force
    Move-Item -LiteralPath $tempRefreshLauncherPath -Destination $refreshLauncherPath -Force
    Move-Item -LiteralPath $tempCatalogPath -Destination $catalogPath -Force
    Move-Item -LiteralPath $tempHistoryPath -Destination $historyScriptPath -Force
    if (-not $testMode) { [Environment]::SetEnvironmentVariable($gatewayKeyEnvName, $apiKey, 'User') }
    $env:MADAPI_API_KEY = $apiKey
    if (Test-Path -LiteralPath $modelsCachePath) { Remove-Item -LiteralPath $modelsCachePath -Force }
    if (-not $testMode) { Register-CatalogRefreshTask $refreshLauncherPath }
    $historyBackupPath = Join-Path $codexHome ('madapi-install-history-backup-' + $transactionId)
    & "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $historyScriptPath -CodexHome $codexHome -ProviderId $providerId -BackupPath $historyBackupPath
    if ($LASTEXITCODE -ne 0) { throw 'MadAPI local history recovery did not complete.' }
} catch {
    Restore-HistoryBackup $codexHome $historyBackupPath
    if ($authChanged) {
        if ($hadAuth -and $null -ne $authBackupPath -and (Test-Path -LiteralPath $authBackupPath)) { [IO.File]::Copy($authBackupPath, $authPath, $true) }
        elseif (-not $hadAuth -and (Test-Path -LiteralPath $authPath)) { Remove-Item -LiteralPath $authPath -Force }
    }
    if ($configInstalled) {
        if ($hadConfig -and $null -ne $backupPath -and (Test-Path -LiteralPath $backupPath)) { [IO.File]::Copy($backupPath, $configPath, $true) }
        elseif (-not $hadConfig -and (Test-Path -LiteralPath $configPath)) { Remove-Item -LiteralPath $configPath -Force }
    }
    throw
} finally {
    if (Test-Path -LiteralPath $tempConfigPath) { Remove-Item -LiteralPath $tempConfigPath -Force }
    if (Test-Path -LiteralPath $tempRefreshPath) { Remove-Item -LiteralPath $tempRefreshPath -Force }
    if (Test-Path -LiteralPath $tempRefreshLauncherPath) { Remove-Item -LiteralPath $tempRefreshLauncherPath -Force }
    if (Test-Path -LiteralPath $tempHistoryPath) { Remove-Item -LiteralPath $tempHistoryPath -Force }
    if (Test-Path -LiteralPath $tempCatalogPath) { Remove-Item -LiteralPath $tempCatalogPath -Force }
    if (Test-Path -LiteralPath $tempAuthPath) { Remove-Item -LiteralPath $tempAuthPath -Force }
}

Write-Host "MadAPI Codex desktop configuration installed: $configPath"
if ($null -ne $backupPath) { Write-Host "Backup created: $backupPath" }
if ($null -ne $authBackupPath) { Write-Host "Authentication backup created: $authBackupPath" }
if ($null -ne $historyBackupPath) { Write-Host "History backup created: $historyBackupPath" }
if ($requestedLoginMode -eq 'oauth' -and $existingAuthKind -ne 'oauth') { Write-Host 'MadAPI installed. Sign in with ChatGPT after Codex restarts to keep an OAuth account connected.' }
elseif ($requestedLoginMode -eq 'apikey') { Write-Host 'MadAPI API Key mode installed without changing Codex account state.' }
elseif ($authKind -eq 'oauth') { Write-Host 'Existing ChatGPT OAuth session preserved.' }
elseif ($authKind -eq 'apikey') { Write-Host 'Existing Codex Desktop API Key sign-in preserved.' }
else { Write-Host 'Codex Desktop sign-in was not changed. Choose ChatGPT OAuth or API Key when Codex opens.' }
Write-Host 'Restart Codex Desktop to refresh the model list.'
