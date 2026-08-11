param([Parameter(Mandatory = $true)][string]$InstallerPath)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True([bool]$Value, [string]$Message) { if (-not $Value) { throw "Assertion failed: $Message" } }
function Write-Utf8([string]$Path, [string]$Value) { [IO.File]::WriteAllText($Path, $Value, (New-Object Text.UTF8Encoding($false))) }
function Hash([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash }

$InstallerPath = (Resolve-Path -LiteralPath $InstallerPath).Path
$assetRoot = Split-Path -Parent $InstallerPath
$repoRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $assetRoot))
$fixtureRoot = Join-Path $repoRoot 'tests\fixtures'
$modelsFixture = Join-Path $fixtureRoot 'newapi-models.json'
$templateFixture = Join-Path $fixtureRoot 'cpa-codex-templates.json'
$refreshScript = Join-Path $assetRoot 'refresh-model-catalog.ps1'
$historyScript = Join-Path $assetRoot 'restore-history.ps1'

$installer = [IO.File]::ReadAllText($InstallerPath)
Assert-True ($installer.Contains('requires_openai_auth = false')) 'Official custom gateway authentication is missing.'
Assert-True ($installer.Contains('env_key = ')) 'Official custom gateway env_key is missing.'
Assert-True ($installer.Contains('x-openai-actor-authorization')) 'Official gateway actor header is missing.'
Assert-True ($installer.Contains('supports_websockets = false')) 'WebSocket opt-out is missing.'
Assert-True ($installer.Contains('image_generation = true')) 'Codex image generation feature is missing.'
Assert-True ($installer.Contains('/codex/v1')) 'Dedicated NewAPI-CPA route is missing.'
Assert-True (-not $installer.Contains('/codex/cockpit/v1')) 'Legacy API compatibility route remains.'
Assert-True (-not $installer.Contains('experimental_bearer_token')) 'Token is still written into config.toml.'

$root = Join-Path ([IO.Path]::GetTempPath()) ('madapi-clean-codex-' + [guid]::NewGuid().ToString('N'))
$codexHome = Join-Path $root '.codex'
$configPath = Join-Path $codexHome 'config.toml'
$authPath = Join-Path $codexHome 'auth.json'
$sessionPath = Join-Path $codexHome 'sessions\sentinel.jsonl'
New-Item -ItemType Directory -Path (Split-Path -Parent $sessionPath) -Force | Out-Null

$originalConfig = @'
model_provider = "custom"
model = "gpt-5.6-terra"
disable_response_storage = true

[model_providers.custom]
name = "custom"
base_url = "https://old.invalid/v1"
wire_api = "responses"
requires_openai_auth = false

[features]
memories = true

[plugins."documents@openai-primary-runtime"]
enabled = true
'@
$oauth = '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"oauth-access-token","refresh_token":"oauth-refresh-token","id_token":"oauth-id-token"}}'

$oldEnvironment = @{}
$environmentNames = @('CODEX_HOME', 'MADAPI_KEY', 'MADAPI_API_KEY', 'MADAPI_BASE_URL', 'MADAPI_CODEX_LOGIN_MODE', 'MADAPI_INSTALL_TEST_MODE', 'MADAPI_REFRESH_SCRIPT_SOURCE', 'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE', 'MADAPI_REFRESH_RESPONSE_FILE', 'MADAPI_CODEX_TEMPLATE_FILE')
foreach ($name in $environmentNames) { $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    Write-Utf8 $configPath $originalConfig
    Write-Utf8 $authPath $oauth
    Write-Utf8 $sessionPath 'session-sentinel'
    Write-Utf8 (Join-Path $codexHome 'models_cache.json') '{}'
    $authHash = Hash $authPath
    $sessionHash = Hash $sessionPath

    $env:CODEX_HOME = $codexHome
    $env:MADAPI_KEY = 'sk-clean-installer-test'
    $env:MADAPI_BASE_URL = 'http://127.0.0.1:13016'
    $env:MADAPI_CODEX_LOGIN_MODE = 'oauth'
    $env:MADAPI_INSTALL_TEST_MODE = '1'
    $env:MADAPI_REFRESH_SCRIPT_SOURCE = $refreshScript
    $env:MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE = $historyScript
    & $InstallerPath | Out-Host

    $result = [IO.File]::ReadAllText($configPath)
    Assert-True ($result.Contains('model_provider = "custom"')) 'Provider identity changed.'
    Assert-True ($result.Contains('base_url = "http://127.0.0.1:13016/codex/v1"')) 'Dedicated NewAPI-CPA route is missing.'
    Assert-True ($result.Contains('requires_openai_auth = false')) 'Official gateway auth mode changed.'
    Assert-True ($result.Contains('env_key = "MADAPI_API_KEY"')) 'Gateway env key is missing.'
    Assert-True ($result.Contains('http_headers = { "x-openai-actor-authorization" = "madapi-gateway" }')) 'Gateway actor header is missing.'
    Assert-True ($result.Contains('image_generation = true')) 'Image generation was not enabled.'
    Assert-True (([regex]::Matches($result, '(?m)^\[features\]\r?$')).Count -eq 1) 'Features table was duplicated.'
    Assert-True ($result.Contains('memories = true')) 'Existing feature setting changed.'
    Assert-True ($result.Contains('[plugins."documents@openai-primary-runtime"]')) 'Unrelated plugin setting changed.'
    Assert-True (-not $result.Contains('sk-clean-installer-test')) 'MadAPI key leaked into config.toml.'
    Assert-True ((Hash $authPath) -eq $authHash) 'OAuth state changed.'
    Assert-True ((Hash $sessionPath) -eq $sessionHash) 'Session data changed.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $codexHome 'models_cache.json'))) 'Stale model cache remains.'
    Assert-True (Test-Path -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json')) 'Initial catalog is missing.'

    $env:MADAPI_KEY = 'sk-clean-installer-repeat'
    $env:MADAPI_CODEX_LOGIN_MODE = 'apikey'
    & $InstallerPath | Out-Host
    $repeat = [IO.File]::ReadAllText($configPath)
    Assert-True (([regex]::Matches($repeat, '(?m)^\[model_providers\.custom\]\r?$')).Count -eq 1) 'Provider table was duplicated.'
    Assert-True (-not $repeat.Contains('sk-clean-installer-repeat')) 'Repeat-install key leaked into config.toml.'
    Assert-True ((Hash $authPath) -eq $authHash) 'API mode overwrote OAuth state.'

    $env:MADAPI_API_KEY = 'sk-refresh-test'
    $env:MADAPI_REFRESH_RESPONSE_FILE = $modelsFixture
    $env:MADAPI_CODEX_TEMPLATE_FILE = $templateFixture
    & $refreshScript -CodexHome $codexHome | Out-Host
    $catalog = Get-Content -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $slugs = @($catalog.models | ForEach-Object { [string]$_.slug })
    Assert-True ('gpt-5.6-sol-pro' -in $slugs) 'Sol Pro was not generated from the CPA Sol profile.'
    Assert-True ('gpt-5.6-terra-pro' -in $slugs) 'Terra Pro was not generated from the CPA Terra profile.'
    Assert-True ('gpt-image-2' -notin $slugs) 'Image-only model leaked into the conversation selector.'
    Assert-True ('seedance-2.0-fast' -notin $slugs) 'Video-only model leaked into the conversation selector.'
    $sol = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-sol' })[0]
    $solPro = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-sol-pro' })[0]
    Assert-True ($sol.default_reasoning_level -eq $solPro.default_reasoning_level) 'Sol Pro capability profile differs from Sol.'
    $terra = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-terra' })[0]
    $terraPro = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-terra-pro' })[0]
    Assert-True ($terra.default_reasoning_level -eq $terraPro.default_reasoning_level) 'Terra Pro capability profile differs from Terra.'

    Write-Host 'Codex Windows clean gateway acceptance passed.'
} finally {
    foreach ($name in $environmentNames) { [Environment]::SetEnvironmentVariable($name, $oldEnvironment[$name], 'Process') }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
