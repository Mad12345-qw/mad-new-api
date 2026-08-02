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

function Register-CatalogRefreshTask([string]$RefreshScriptPath) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $sid = $identity.User.Value
    $taskName = 'MadAPI Codex Model Catalog Refresh - ' + $sid
    $startBoundary = (Get-Date).AddMinutes(1).ToString('s')
    $arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $RefreshScriptPath + '"'
    $taskXml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled><UserId>$(ConvertTo-XmlText $sid)</UserId></LogonTrigger>
    <CalendarTrigger>
      <Repetition><Interval>PT5M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>
      <StartBoundary>$startBoundary</StartBoundary><Enabled>true</Enabled>
      <ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>
    </CalendarTrigger>
  </Triggers>
  <Principals><Principal id="Author"><UserId>$(ConvertTo-XmlText $sid)</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><StartWhenAvailable>true</StartWhenAvailable><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><ExecutionTimeLimit>PT2M</ExecutionTimeLimit></Settings>
  <Actions Context="Author"><Exec><Command>powershell.exe</Command><Arguments>$(ConvertTo-XmlText $arguments)</Arguments></Exec></Actions>
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

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey) -or $apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY is missing or invalid.'
}

$userHome = [Environment]::GetFolderPath('UserProfile')
$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
$configPath = Join-Path $codexHome 'config.toml'
$authPath = Join-Path $codexHome 'auth.json'
$modelsCachePath = Join-Path $codexHome 'models_cache.json'
$catalogPath = Join-Path $codexHome 'madapi-cockpit-model-catalog.json'
$refreshScriptPath = Join-Path $codexHome 'madapi-refresh-model-catalog.ps1'
$transactionId = [guid]::NewGuid().ToString('N')
$tempConfigPath = Join-Path $codexHome ("config.toml.madapi.$transactionId.tmp")
$tempRefreshPath = Join-Path $codexHome ("madapi-refresh-model-catalog.$transactionId.ps1")
$tempCatalogPath = Join-Path $codexHome ("madapi-cockpit-model-catalog.$transactionId.tmp")
$backupPath = $null
$hadConfig = Test-Path -LiteralPath $configPath
$hadAuth = Test-Path -LiteralPath $authPath
$configInstalled = $false
$testMode = [string]$env:MADAPI_INSTALL_TEST_MODE -eq '1'

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
$configLines.Add('model_catalog_json = "madapi-cockpit-model-catalog.json"')
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
$configLines.Add('base_url = "https://mad.myddns.me/codex/cockpit/v1"')
$configLines.Add('wire_api = "responses"')
$configLines.Add('requires_openai_auth = ' + $(if ($authKind -eq 'apikey') { 'false' } else { 'true' }))
if ($authKind -ne 'apikey') {
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
        Invoke-WebRequest -UseBasicParsing -Uri 'https://mad.myddns.me/mad-codex/refresh-model-catalog.ps1' -OutFile $tempRefreshPath
    }
    if (-not (Test-Path -LiteralPath $tempRefreshPath) -or (Get-Item -LiteralPath $tempRefreshPath).Length -lt 100) {
        throw 'The MadAPI model catalog refresh script is invalid.'
    }
    if ($testMode) {
        [IO.File]::WriteAllText($tempCatalogPath, '{"models":[{"slug":"gpt-5.6-sol","display_name":"gpt-5.6-sol"}]}', $utf8NoBom)
    } else {
        $stagingHome = Join-Path $codexHome ('madapi-catalog-stage-' + $transactionId)
        New-Item -ItemType Directory -Path $stagingHome -Force | Out-Null
        try {
            [IO.File]::Copy($tempConfigPath, (Join-Path $stagingHome 'config.toml'), $true)
            & "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $tempRefreshPath -CodexHome $stagingHome
            if ($LASTEXITCODE -ne 0) { throw 'Unable to download the initial MadAPI model catalog.' }
            Move-Item -LiteralPath (Join-Path $stagingHome 'madapi-cockpit-model-catalog.json') -Destination $tempCatalogPath -Force
        } finally {
            if (Test-Path -LiteralPath $stagingHome) { Remove-Item -LiteralPath $stagingHome -Recurse -Force }
        }
        Register-CatalogRefreshTask $refreshScriptPath
    }
    if ($hadConfig) {
        $backupPath = '{0}.madapi-backup-{1}' -f $configPath, (Get-Date -Format 'yyyyMMdd-HHmmss-fff')
        [IO.File]::Copy($configPath, $backupPath, $false)
    }
    Move-Item -LiteralPath $tempConfigPath -Destination $configPath -Force
    $configInstalled = $true
    Move-Item -LiteralPath $tempRefreshPath -Destination $refreshScriptPath -Force
    Move-Item -LiteralPath $tempCatalogPath -Destination $catalogPath -Force
    if (Test-Path -LiteralPath $modelsCachePath) { Remove-Item -LiteralPath $modelsCachePath -Force }
} catch {
    if ($configInstalled) {
        if ($hadConfig -and $null -ne $backupPath -and (Test-Path -LiteralPath $backupPath)) { [IO.File]::Copy($backupPath, $configPath, $true) }
        elseif (-not $hadConfig -and (Test-Path -LiteralPath $configPath)) { Remove-Item -LiteralPath $configPath -Force }
    }
    throw
} finally {
    if (Test-Path -LiteralPath $tempConfigPath) { Remove-Item -LiteralPath $tempConfigPath -Force }
    if (Test-Path -LiteralPath $tempRefreshPath) { Remove-Item -LiteralPath $tempRefreshPath -Force }
    if (Test-Path -LiteralPath $tempCatalogPath) { Remove-Item -LiteralPath $tempCatalogPath -Force }
}

Write-Host "MadAPI Codex desktop configuration installed: $configPath"
if ($null -ne $backupPath) { Write-Host "Backup created: $backupPath" }
if ($authKind -eq 'oauth') { Write-Host 'Existing ChatGPT OAuth session preserved.' }
elseif ($authKind -eq 'apikey') { Write-Host 'Existing Codex Desktop API Key sign-in preserved.' }
else { Write-Host 'Codex Desktop sign-in was not changed. Choose ChatGPT OAuth or API Key when Codex opens.' }
Write-Host 'Restart Codex Desktop to refresh the model list.'
