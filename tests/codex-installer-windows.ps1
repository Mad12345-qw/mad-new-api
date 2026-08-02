param([Parameter(Mandatory = $true)][string]$InstallerPath)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True([bool]$Value, [string]$Message) { if (-not $Value) { throw "Assertion failed: $Message" } }
function Write-Utf8([string]$Path, [string]$Value) { [IO.File]::WriteAllText($Path, $Value, (New-Object System.Text.UTF8Encoding($false))) }
function Hash([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash }
function Write-OAuth([string]$Path) {
    Write-Utf8 $Path '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"oauth-access-token","refresh_token":"oauth-refresh-token","id_token":"oauth-id-token"},"last_refresh":"2026-08-02T00:00:00Z"}'
}
function Install([string]$CodexHome, [string]$Key) {
    $oldHome, $oldKey, $oldTestMode, $oldRefreshSource = $env:CODEX_HOME, $env:MADAPI_KEY, $env:MADAPI_INSTALL_TEST_MODE, $env:MADAPI_REFRESH_SCRIPT_SOURCE
    try {
        $env:CODEX_HOME = $CodexHome
        $env:MADAPI_KEY = $Key
        $env:MADAPI_INSTALL_TEST_MODE = '1'
        $env:MADAPI_REFRESH_SCRIPT_SOURCE = Join-Path (Split-Path -Parent $InstallerPath) 'refresh-model-catalog.ps1'
        & $InstallerPath
    } finally {
        $env:CODEX_HOME, $env:MADAPI_KEY, $env:MADAPI_INSTALL_TEST_MODE, $env:MADAPI_REFRESH_SCRIPT_SOURCE = $oldHome, $oldKey, $oldTestMode, $oldRefreshSource
    }
}

$installer = [IO.File]::ReadAllText($InstallerPath)
Assert-True (-not $installer.Contains('CODEX_CLI_PATH')) 'CLI probing remains.'
Assert-True (-not $installer.Contains('madapi.key')) 'External key-file authentication remains.'
Assert-True (-not $installer.Contains('Get-Command')) 'CLI discovery remains.'
Assert-True ($installer -match '(?m)^\$tempRefreshPath\s*=.*\.ps1') 'Temporary refresh script does not use a PowerShell-compatible extension.'

$temporaryRoot = if ([string]::IsNullOrWhiteSpace([string]$env:RUNNER_TEMP)) { [IO.Path]::GetTempPath() } else { [string]$env:RUNNER_TEMP }
$codexHome = Join-Path $temporaryRoot ('mad-codex-desktop-' + [guid]::NewGuid().ToString('N'))
$session = Join-Path $codexHome 'sessions\sentinel.jsonl'
New-Item -ItemType Directory -Path (Split-Path -Parent $session) -Force | Out-Null
$config = Join-Path $codexHome 'config.toml'
$auth = Join-Path $codexHome 'auth.json'
$keyFile = Join-Path $codexHome 'madapi.key'
$cache = Join-Path $codexHome 'models_cache.json'
$original = @'
model_provider = "newapi"
model = "deepseek-v4-flash"
model_catalog_json = "cc-switch-model-catalog.json"
disable_response_storage = true
[model_providers.newapi]
name = "NewAPI"
base_url = "https://old.invalid/v1"
requires_openai_auth = false
experimental_bearer_token = "sk-stale-bearer"
[model_providers.newapi.auth]
command = "powershell.exe"
args = ["-NoProfile", "-Command", "Write-Output stale"]
[model_providers.madapi]
name = "Old MadAPI"
[plugins."github@openai-curated"]
enabled = true
'@
try {
    Write-Utf8 $config $original; Write-OAuth $auth; Write-Utf8 $keyFile 'keep-me'; Write-Utf8 $cache '{}'; Write-Utf8 $session 'session'
    $configHash = Hash $config
    $authHash = Hash $auth
    $sessionHash = Hash $session
    Install $codexHome 'sk-windows-first-key'
    $result = [IO.File]::ReadAllText($config)
    Assert-True ($result.Contains('model_provider = "newapi"')) 'Provider identity changed.'
    Assert-True ($result.Contains('model = "deepseek-v4-flash"')) 'Default model changed.'
    Assert-True ($result.Contains('name = "NewAPI"')) 'Provider name changed.'
    Assert-True ($result.Contains('experimental_bearer_token = "sk-windows-first-key"')) 'Bearer token missing.'
    Assert-True ($result.Contains('requires_openai_auth = true')) 'Desktop OAuth setting missing.'
    Assert-True ($result.Contains('base_url = "https://mad.myddns.me/codex/cockpit/v1"')) 'OAuth Cockpit route is missing.'
    Assert-True (-not $result.Contains('[model_providers.newapi.auth]')) 'Command auth remains.'
    Assert-True (-not $result.Contains('sk-stale-bearer')) 'Stale bearer remains.'
    Assert-True (-not $result.Contains('Write-Output stale')) 'Stale command auth remains.'
    Assert-True ($result.Contains('disable_response_storage = true')) 'Unrelated setting changed.'
    Assert-True ($result.Contains('model_catalog_json = "madapi-cockpit-model-catalog.json"')) 'OAuth managed catalog is missing.'
    Assert-True (-not $result.Contains('cc-switch-model-catalog.json')) 'Conflicting third-party catalog remains.'
    Assert-True (-not $result.Contains('[model_providers.madapi]')) 'Temporary provider remains.'
    Assert-True (([IO.File]::ReadAllText($keyFile)) -eq 'keep-me') 'Existing key file changed.'
    Assert-True (-not (Test-Path -LiteralPath $cache)) 'Stale cache remains.'
    Assert-True ((Hash $auth) -eq $authHash) 'OAuth state changed.'
    Assert-True ((Hash $session) -eq $sessionHash) 'Session changed.'
    Assert-True (Test-Path -LiteralPath (Join-Path $codexHome 'madapi-refresh-model-catalog.ps1')) 'OAuth refresh script is missing.'
    Assert-True (Test-Path -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json')) 'OAuth catalog file is missing.'
    $backup = @(Get-ChildItem -LiteralPath $codexHome -Filter 'config.toml.madapi-backup-*' -File)[0]
    Assert-True ($null -ne $backup -and (Hash $backup.FullName) -eq $configHash) 'Backup is not exact.'
    Install $codexHome 'sk-windows-second-key'
    $result = [IO.File]::ReadAllText($config)
    Assert-True ($result.Contains('experimental_bearer_token = "sk-windows-second-key"')) 'Repeat install did not update token.'
    Assert-True (-not $result.Contains('sk-windows-first-key')) 'Repeat install retained the old token.'
    Assert-True (([regex]::Matches($result, '(?m)^\[model_providers\.newapi\]\r?$')).Count -eq 1) 'Duplicate provider created.'
    $fresh = Join-Path $codexHome 'fresh'
    New-Item -ItemType Directory -Path $fresh -Force | Out-Null
    Write-OAuth (Join-Path $fresh 'auth.json')
    Install $fresh 'sk-windows-fresh-key'
    $freshConfig = [IO.File]::ReadAllText((Join-Path $fresh 'config.toml'))
    Assert-True ($freshConfig.Contains('model_provider = "custom"')) 'Fresh identity is wrong.'
    Assert-True ($freshConfig.Contains('model = "gpt-5.6-sol"')) 'Fresh default missing.'
    Assert-True ($freshConfig.Contains('requires_openai_auth = true')) 'Fresh OAuth setting missing.'
    Assert-True ($freshConfig.Contains('base_url = "https://mad.myddns.me/codex/cockpit/v1"')) 'Fresh OAuth Cockpit route is missing.'
    Assert-True ($freshConfig.Contains('model_catalog_json = "madapi-cockpit-model-catalog.json"')) 'Fresh OAuth managed catalog is missing.'
    Assert-True ($freshConfig.Contains('experimental_bearer_token = "sk-windows-fresh-key"')) 'Fresh bearer token missing.'
    Assert-True (-not $freshConfig.Contains('[model_providers.custom.auth]')) 'Fresh command auth remains.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $fresh 'madapi.key'))) 'Fresh install created key file.'

    $apiOnly = Join-Path $codexHome 'api-only'
    New-Item -ItemType Directory -Path $apiOnly -Force | Out-Null
    $apiOnlyConfig = Join-Path $apiOnly 'config.toml'
    $apiOnlyCache = Join-Path $apiOnly 'models_cache.json'
    Write-Utf8 $apiOnlyConfig 'model = "gpt-5.6-sol"'
    Write-Utf8 (Join-Path $apiOnly 'auth.json') '{"OPENAI_API_KEY":"sk-existing-api-key","tokens":null,"last_refresh":null}'
    Write-Utf8 $apiOnlyCache '{}'
    $apiOnlyAuth = Join-Path $apiOnly 'auth.json'
    $apiOnlyConfigHash = Hash $apiOnlyConfig
    $apiOnlyAuthHash = Hash $apiOnlyAuth
    Install $apiOnly 'sk-windows-api-key'
    $apiResult = [IO.File]::ReadAllText($apiOnlyConfig)
    Assert-True ($apiResult.Contains('requires_openai_auth = false')) 'API-key auth gate is wrong.'
    Assert-True ($apiResult.Contains('[model_providers.custom.auth]')) 'API-key command auth is missing.'
    Assert-True ($apiResult.Contains("[Console]::Out.Write('sk-windows-api-key')")) 'API-key command does not contain the MadAPI key.'
    Assert-True (-not $apiResult.Contains('experimental_bearer_token')) 'API-key config contains conflicting bearer auth.'
    Assert-True ($apiResult.Contains('model_catalog_json = "madapi-cockpit-model-catalog.json"')) 'API-key managed catalog is missing.'
    Assert-True ($apiResult.Contains('base_url = "https://mad.myddns.me/codex/cockpit/v1"')) 'API-key Cockpit route is missing.'
    Assert-True (-not $apiResult.Contains('cc-switch-model-catalog.json')) 'Conflicting third-party catalog remains.'
    Assert-True (Test-Path -LiteralPath (Join-Path $apiOnly 'madapi-refresh-model-catalog.ps1')) 'API-key refresh script is missing.'
    Assert-True (Test-Path -LiteralPath (Join-Path $apiOnly 'madapi-cockpit-model-catalog.json')) 'API-key catalog file is missing.'
    Assert-True (-not (Test-Path -LiteralPath $apiOnlyCache)) 'API-key stale cache remains.'
    $apiAuth = [IO.File]::ReadAllText($apiOnlyAuth) | ConvertFrom-Json
    Assert-True ($apiAuth.OPENAI_API_KEY -eq 'sk-existing-api-key') 'Existing API-key authentication changed.'
    Assert-True ((Hash $apiOnlyAuth) -eq $apiOnlyAuthHash) 'Existing API-key authentication was not preserved byte-for-byte.'
    $apiConfigBackup = @(Get-ChildItem -LiteralPath $apiOnly -Filter 'config.toml.madapi-backup-*' -File)[0]
    Assert-True ((Hash $apiConfigBackup.FullName) -eq $apiOnlyConfigHash) 'API-key config backup is not exact.'
    Assert-True (@(Get-ChildItem -LiteralPath $apiOnly -Filter 'auth.json.madapi-backup-*' -File).Count -eq 0) 'Installer created an unnecessary authentication backup.'

    $unsigned = Join-Path $codexHome 'unsigned'
    New-Item -ItemType Directory -Path $unsigned -Force | Out-Null
    $unsignedConfig = Join-Path $unsigned 'config.toml'
    Write-Utf8 $unsignedConfig 'model = "gpt-5.6-sol"'
    Install $unsigned 'sk-windows-new-key'
    $unsignedResult = [IO.File]::ReadAllText($unsignedConfig)
    Assert-True (-not $unsignedResult.Contains('[model_providers.custom.auth]')) 'New-user install forced command authentication.'
    Assert-True ($unsignedResult.Contains('requires_openai_auth = true')) 'New-user install did not preserve the Codex sign-in chooser.'
    Assert-True ($unsignedResult.Contains('experimental_bearer_token = "sk-windows-new-key"')) 'New-user MadAPI bearer token is missing.'
    Assert-True ($unsignedResult.Contains('model_catalog_json = "madapi-cockpit-model-catalog.json"')) 'New-user install did not configure the managed catalog.'
    Assert-True ($unsignedResult.Contains('base_url = "https://mad.myddns.me/codex/cockpit/v1"')) 'New-user install did not configure the Cockpit route.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $unsigned 'auth.json'))) 'New-user install forced API-key sign-in.'
    Write-Host 'Windows desktop Codex installer acceptance passed.'
} finally { if (Test-Path -LiteralPath $codexHome) { Remove-Item -LiteralPath $codexHome -Recurse -Force } }
