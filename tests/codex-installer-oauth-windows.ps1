param([Parameter(Mandatory = $true)][string]$InstallerPath)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True([bool]$Value, [string]$Message) {
    if (-not $Value) { throw "Assertion failed: $Message" }
}

function Write-Utf8([string]$Path, [string]$Value) {
    New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
    [IO.File]::WriteAllText($Path, $Value, (New-Object Text.UTF8Encoding($false)))
}

function Hash([string]$Path) {
    (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash
}

function Invoke-Installer([string]$CodexHome, [string]$Mode, [string]$ModelsFixture) {
    $env:CODEX_HOME = $CodexHome
    $env:MADAPI_CODEX_LOGIN_MODE = $Mode
    $env:MADAPI_REFRESH_RESPONSE_FILE = $ModelsFixture
    & $InstallerPath | Out-Host
}

$InstallerPath = (Resolve-Path -LiteralPath $InstallerPath).Path
$assetRoot = Split-Path -Parent $InstallerPath
$repoRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $assetRoot)))
$fixtureRoot = Join-Path $repoRoot 'tests\fixtures'
$modelsFixture = Join-Path $fixtureRoot 'newapi-models.json'
$oauthModelsFixture = Join-Path $fixtureRoot 'oauth-codex-models.json'
$templateFixture = Join-Path $fixtureRoot 'codex-model-templates.json'
$refreshScript = Join-Path $assetRoot 'refresh-model-catalog.ps1'
$historyScript = Join-Path $assetRoot 'restore-history.ps1'
$imageSkillSource = Join-Path $assetRoot 'image-skill'
$installerSource = [IO.File]::ReadAllText($InstallerPath)
Assert-True ($installerSource.Contains('OAuth mode prepared. Restart Codex Desktop and sign in with ChatGPT.')) 'OAuth sign-in guidance is missing from the installer.'
$root = Join-Path ([IO.Path]::GetTempPath()) ('madapi-oauth-installer-' + [guid]::NewGuid().ToString('N'))
$environmentNames = @('CODEX_HOME', 'MADAPI_KEY', 'MADAPI_API_KEY', 'MADAPI_BASE_URL', 'MADAPI_CODEX_LOGIN_MODE', 'MADAPI_CODEX_AUTH_KIND', 'MADAPI_INSTALL_TEST_MODE', 'MADAPI_REFRESH_SCRIPT_SOURCE', 'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE', 'MADAPI_REFRESH_RESPONSE_FILE', 'MADAPI_CODEX_TEMPLATE_FILE', 'MADAPI_IMAGE_SKILL_SOURCE_DIR')
$oldEnvironment = @{}
foreach ($name in $environmentNames) { $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    $env:MADAPI_KEY = 'sk-oauth-installer-test'
    $env:MADAPI_BASE_URL = 'http://127.0.0.1:13016'
    $env:MADAPI_INSTALL_TEST_MODE = '1'
    $env:MADAPI_REFRESH_SCRIPT_SOURCE = $refreshScript
    $env:MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE = $historyScript
    $env:MADAPI_CODEX_TEMPLATE_FILE = $templateFixture
    $env:MADAPI_IMAGE_SKILL_SOURCE_DIR = $imageSkillSource

    $freshHome = Join-Path $root 'fresh\.codex'
    Invoke-Installer $freshHome 'oauth' $oauthModelsFixture
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $freshHome 'auth.json'))) 'Fresh OAuth install created an auth file.'
    $freshConfig = [IO.File]::ReadAllText((Join-Path $freshHome 'config.toml'))
    Assert-True ($freshConfig.Contains('requires_openai_auth = true')) 'Fresh OAuth install did not enable ChatGPT sign-in.'
    Assert-True ($freshConfig.Contains('/codex/v1')) 'Fresh OAuth install did not use the OAuth route.'

    $existingHome = Join-Path $root 'existing\.codex'
    $existingAuthPath = Join-Path $existingHome 'auth.json'
    $oauth = '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"oauth-access-token","refresh_token":"oauth-refresh-token","id_token":"oauth-id-token"}}'
    Write-Utf8 $existingAuthPath $oauth
    $oauthHash = Hash $existingAuthPath
    Invoke-Installer $existingHome 'oauth' $oauthModelsFixture | Out-Null
    Assert-True ((Hash $existingAuthPath) -eq $oauthHash) 'Existing OAuth session changed.'

    $switchHome = Join-Path $root 'switch\.codex'
    Invoke-Installer $switchHome 'apikey' $modelsFixture | Out-Null
    $switchAuthPath = Join-Path $switchHome 'auth.json'
    Assert-True (Test-Path -LiteralPath $switchAuthPath) 'API install did not create auth state.'
    $apiAuth = Get-Content -LiteralPath $switchAuthPath -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ([string]$apiAuth.auth_mode -eq 'apikey') 'API install wrote the wrong auth mode.'
    Invoke-Installer $switchHome 'oauth' $oauthModelsFixture
    Assert-True (-not (Test-Path -LiteralPath $switchAuthPath)) 'API to OAuth switch did not clear API auth state.'
    Assert-True (@(Get-ChildItem -LiteralPath $switchHome -Filter 'auth.json.madapi-backup-*' -File -ErrorAction SilentlyContinue).Count -eq 1) 'API auth backup is missing.'

    Write-Host 'Codex OAuth installer acceptance passed: fresh, existing OAuth, API to OAuth, and API mode.'
} finally {
    foreach ($name in $environmentNames) { [Environment]::SetEnvironmentVariable($name, $oldEnvironment[$name], 'Process') }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
