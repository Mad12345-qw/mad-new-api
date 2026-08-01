param([Parameter(Mandatory = $true)][string]$InstallerPath)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True([bool]$Value, [string]$Message) { if (-not $Value) { throw "Assertion failed: $Message" } }
function Write-Utf8([string]$Path, [string]$Value) { [IO.File]::WriteAllText($Path, $Value, (New-Object System.Text.UTF8Encoding($false))) }
function Hash([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash }
function Install([string]$CodexHome, [string]$Key) {
    $oldHome, $oldKey = $env:CODEX_HOME, $env:MADAPI_KEY
    try { $env:CODEX_HOME = $CodexHome; $env:MADAPI_KEY = $Key; & $InstallerPath }
    finally { $env:CODEX_HOME = $oldHome; $env:MADAPI_KEY = $oldKey }
}

$installer = [IO.File]::ReadAllText($InstallerPath)
Assert-True (-not $installer.Contains('CODEX_CLI_PATH')) 'CLI probing remains.'
Assert-True (-not $installer.Contains('madapi.key')) 'Command authentication remains.'
Assert-True (-not $installer.Contains('Get-Command')) 'CLI discovery remains.'

$temporaryRoot = if ([string]::IsNullOrWhiteSpace([string]$env:RUNNER_TEMP)) { [IO.Path]::GetTempPath() } else { [string]$env:RUNNER_TEMP }
$codexHome = Join-Path $temporaryRoot ('mad-codex-desktop-' + [guid]::NewGuid().ToString('N'))
$session = Join-Path $codexHome 'sessions\sentinel.jsonl'
New-Item -ItemType Directory -Path (Split-Path -Parent $session) -Force | Out-Null
$config = Join-Path $codexHome 'config.toml'
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
[model_providers.madapi]
name = "Old MadAPI"
[plugins."github@openai-curated"]
enabled = true
'@
try {
    Write-Utf8 $config $original; Write-Utf8 $keyFile 'keep-me'; Write-Utf8 $cache '{}'; Write-Utf8 $session 'session'
    $configHash = Hash $config
    $sessionHash = Hash $session
    Install $codexHome 'sk-windows-first-key'
    $result = [IO.File]::ReadAllText($config)
    Assert-True ($result.Contains('model_provider = "newapi"')) 'Provider identity changed.'
    Assert-True ($result.Contains('model = "deepseek-v4-flash"')) 'Default model changed.'
    Assert-True ($result.Contains('name = "NewAPI"')) 'Provider name changed.'
    Assert-True ($result.Contains('experimental_bearer_token = "sk-windows-first-key"')) 'Bearer token missing.'
    Assert-True ($result.Contains('requires_openai_auth = true')) 'Desktop auth setting missing.'
    Assert-True ($result.Contains('disable_response_storage = true')) 'Unrelated setting changed.'
    Assert-True (-not ($result -match '(?m)^\s*(model_catalog_json|"model_catalog_json"|''model_catalog_json'')\s*=')) 'Static catalog remains.'
    Assert-True (-not $result.Contains('[model_providers.newapi.auth]')) 'Command auth was added.'
    Assert-True (-not $result.Contains('[model_providers.madapi]')) 'Temporary provider remains.'
    Assert-True (([IO.File]::ReadAllText($keyFile)) -eq 'keep-me') 'Existing key file changed.'
    Assert-True (-not (Test-Path -LiteralPath $cache)) 'Stale cache remains.'
    Assert-True ((Hash $session) -eq $sessionHash) 'Session changed.'
    $backup = @(Get-ChildItem -LiteralPath $codexHome -Filter 'config.toml.madapi-backup-*' -File)[0]
    Assert-True ($null -ne $backup -and (Hash $backup.FullName) -eq $configHash) 'Backup is not exact.'
    Install $codexHome 'sk-windows-second-key'
    $result = [IO.File]::ReadAllText($config)
    Assert-True ($result.Contains('experimental_bearer_token = "sk-windows-second-key"')) 'Repeat install did not update token.'
    Assert-True (([regex]::Matches($result, '(?m)^\[model_providers\.newapi\]\r?$')).Count -eq 1) 'Duplicate provider created.'
    $fresh = Join-Path $codexHome 'fresh'
    Install $fresh 'sk-windows-fresh-key'
    $freshConfig = [IO.File]::ReadAllText((Join-Path $fresh 'config.toml'))
    Assert-True ($freshConfig.Contains('model_provider = "custom"')) 'Fresh identity is wrong.'
    Assert-True ($freshConfig.Contains('model = "gpt-5.6-sol"')) 'Fresh default missing.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $fresh 'madapi.key'))) 'Fresh install created key file.'
    Write-Host 'Windows desktop Codex installer acceptance passed.'
} finally { if (Test-Path -LiteralPath $codexHome) { Remove-Item -LiteralPath $codexHome -Recurse -Force } }
