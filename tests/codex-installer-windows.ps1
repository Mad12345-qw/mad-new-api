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
$oauthModelsFixture = Join-Path $fixtureRoot 'oauth-codex-models.json'
$templateFixture = Join-Path $fixtureRoot 'cpa-codex-templates.json'
$refreshScript = Join-Path $assetRoot 'refresh-model-catalog.ps1'
$historyScript = Join-Path $assetRoot 'restore-history.ps1'

$installer = [IO.File]::ReadAllText($InstallerPath)
Assert-True ($installer.Contains('requires_openai_auth')) 'Login-mode authentication branch is missing.'
Assert-True ($installer.Contains('experimental_bearer_token')) 'OAuth bearer configuration is missing.'
Assert-True ($installer.Contains('env_key = ')) 'API key configuration is missing.'
Assert-True ($installer.Contains('x-openai-actor-authorization')) 'Official gateway actor header is missing.'
Assert-True ($installer.Contains('supports_websockets = false')) 'WebSocket opt-out is missing.'
Assert-True ($installer.Contains('image_generation = true')) 'Codex image generation feature is missing.'
Assert-True ($installer.Contains('/codex/v1')) 'Dedicated NewAPI-CPA route is missing.'
Assert-True ($installer.Contains('/codex/cockpit/v1')) 'API compatibility route is missing.'
$refreshSource = [IO.File]::ReadAllText($refreshScript)
Assert-True ($refreshSource.Contains('/mad-codex/cpa-codex-templates.json')) 'Self-hosted Codex model template is missing.'
Assert-True (-not $refreshSource.Contains('models.router-for.me')) 'Codex model refresh still depends on a third-party host.'
Assert-True ($installer.Contains("'ChatGPT', 'Codex'")) 'Codex Desktop process-group shutdown is missing.'
Assert-True ($installer.Contains('OpenAI.Codex_')) 'Codex Desktop package filter is missing.'

$root = Join-Path ([IO.Path]::GetTempPath()) ('madapi-clean-codex-' + [guid]::NewGuid().ToString('N'))
$codexHome = Join-Path $root '.codex'
$configPath = Join-Path $codexHome 'config.toml'
$authPath = Join-Path $codexHome 'auth.json'
$sessionPath = Join-Path $codexHome 'sessions\sentinel.jsonl'
$projectRoot = Join-Path $root 'project-one'
$projectSessionPath = Join-Path $codexHome 'sessions\rollout-project-one.jsonl'
$globalStatePath = Join-Path $codexHome '.codex-global-state.json'
New-Item -ItemType Directory -Path (Split-Path -Parent $sessionPath) -Force | Out-Null
New-Item -ItemType Directory -Path $projectRoot -Force | Out-Null

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
$environmentNames = @('CODEX_HOME', 'MADAPI_KEY', 'MADAPI_API_KEY', 'MADAPI_BASE_URL', 'MADAPI_CODEX_LOGIN_MODE', 'MADAPI_CODEX_AUTH_KIND', 'MADAPI_INSTALL_TEST_MODE', 'MADAPI_REFRESH_SCRIPT_SOURCE', 'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE', 'MADAPI_REFRESH_RESPONSE_FILE', 'MADAPI_CODEX_TEMPLATE_FILE', 'MADAPI_IMAGE_SKILL_SOURCE_DIR')
foreach ($name in $environmentNames) { $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    Write-Utf8 $configPath $originalConfig
    Write-Utf8 $authPath $oauth
    Write-Utf8 $sessionPath 'session-sentinel'
    Write-Utf8 $projectSessionPath (([ordered]@{
        type = 'session_meta'
        payload = [ordered]@{
            id = 'project-thread-one'
            cwd = $projectRoot
        }
    } | ConvertTo-Json -Compress))
    Write-Utf8 $globalStatePath (([ordered]@{
        'local-projects' = [ordered]@{
            'local-project-one' = [ordered]@{
                id = 'local-project-one'
                name = 'Project One'
                rootPaths = @($projectRoot)
            }
        }
        'projectless-thread-ids' = @('project-thread-one')
        'thread-workspace-root-hints' = [ordered]@{}
        'thread-project-assignments' = [ordered]@{}
    } | ConvertTo-Json -Depth 20 -Compress))
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
    $env:MADAPI_IMAGE_SKILL_SOURCE_DIR = Join-Path $assetRoot 'image-skill'
    $env:MADAPI_REFRESH_RESPONSE_FILE = $oauthModelsFixture
    $env:MADAPI_CODEX_TEMPLATE_FILE = $templateFixture
    & $InstallerPath | Out-Host

    $result = [IO.File]::ReadAllText($configPath)
    Assert-True ($result.Contains('model_provider = "custom"')) 'Provider identity changed.'
    Assert-True ($result.Contains('base_url = "http://127.0.0.1:13016/codex/v1"')) 'Dedicated NewAPI-CPA route is missing.'
    Assert-True ($result.Contains('requires_openai_auth = true')) 'OAuth gateway auth mode changed.'
    Assert-True ($result.Contains('experimental_bearer_token = ')) 'OAuth bearer token is missing.'
    Assert-True (-not $result.Contains('env_key = "MADAPI_API_KEY"')) 'OAuth config incorrectly uses API env_key.'
    Assert-True ($result.Contains('x-openai-actor-authorization')) 'Gateway actor header is missing.'
    Assert-True ($result.Contains('image_generation = true')) 'Image generation was not enabled.'
    Assert-True ($result.Contains('localeOverride = "zh-CN"')) 'Codex did not default to Chinese.'
    Assert-True ($result.Contains('network_access = true')) 'Image skill network access was not enabled.'
    Assert-True ($result.Contains('path = "') -and $result.Contains('madapi-imagegen')) 'Image skill path was not registered.'
    Assert-True (([regex]::Matches($result, '(?m)^\[features\]\r?$')).Count -eq 1) 'Features table was duplicated.'
    Assert-True ($result.Contains('memories = true')) 'Existing feature setting changed.'
    Assert-True ($result.Contains('[plugins."documents@openai-primary-runtime"]')) 'Unrelated plugin setting changed.'
    Assert-True ($result.Contains('experimental_bearer_token = "sk-clean-installer-test"')) 'OAuth bearer token is not bound to the configured MadAPI key.'
    Assert-True ((Hash $authPath) -eq $authHash) 'OAuth state changed.'
    Assert-True ((Hash $sessionPath) -eq $sessionHash) 'Session data changed.'
    $globalState = Get-Content -LiteralPath $globalStatePath -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True (@($globalState.'local-projects'.PSObject.Properties).Count -eq 1) 'Existing Codex project was lost.'
    Assert-True ($null -ne $globalState.'thread-project-assignments'.PSObject.Properties['project-thread-one']) 'Project conversation assignment was not restored.'
    Assert-True ('project-thread-one' -notin @($globalState.'projectless-thread-ids')) 'Assigned conversation remains projectless.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $codexHome 'models_cache.json'))) 'Stale model cache remains.'
    Assert-True (Test-Path -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json')) 'Initial catalog is missing.'
    $imageSkillText = [IO.File]::ReadAllText((Join-Path (Join-Path (Split-Path -Parent $codexHome) '.agents') 'skills\madapi-imagegen\SKILL.md'), [Text.Encoding]::UTF8)
    Assert-True ($imageSkillText.Contains('absolute `path`') -and $imageSkillText.Contains('source_url')) 'Image preview/download contract is missing.'

    $env:MADAPI_CODEX_LOGIN_MODE = 'apikey'
    $env:MADAPI_KEY = 'sk-clean-api-installer'
    $env:MADAPI_REFRESH_RESPONSE_FILE = $modelsFixture
    & $InstallerPath | Out-Host
    $repeat = [IO.File]::ReadAllText($configPath)
    Assert-True (([regex]::Matches($repeat, '(?m)^\[model_providers\.custom\]\r?$')).Count -eq 1) 'Provider table was duplicated.'
    Assert-True ($repeat.Contains('requires_openai_auth = false')) 'API gateway auth mode is missing.'
    Assert-True ($repeat.Contains('base_url = "http://127.0.0.1:13016/codex/cockpit/v1"')) 'API compatibility route is missing.'
    Assert-True ($repeat.Contains('env_key = "MADAPI_API_KEY"')) 'API gateway env_key is missing.'
    Assert-True (-not $repeat.Contains('experimental_bearer_token')) 'API config contains OAuth bearer authentication.'
    Assert-True ($repeat.Contains('image_generation = true')) 'API image generation was not enabled.'
    Assert-True ($repeat.Contains('localeOverride = "zh-CN"')) 'API Codex did not default to Chinese.'
    Assert-True ($repeat.Contains('network_access = true')) 'API image skill network access was not enabled.'

    Assert-True (-not $installer.Contains('MADAPI_CODEX_LANGUAGE')) 'Codex installer still exposes an English language option.'
    $apiAuth = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ([string]$apiAuth.auth_mode -eq 'apikey' -and [string]$apiAuth.OPENAI_API_KEY -eq 'sk-clean-api-installer') 'API authentication state was not configured.'
    $apiCatalog = Get-Content -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True (@($apiCatalog.models).Count -eq 8) 'API catalog does not contain exactly eight models.'

    $env:MADAPI_API_KEY = 'sk-refresh-test'
    $env:MADAPI_REFRESH_RESPONSE_FILE = $modelsFixture
    $env:MADAPI_CODEX_TEMPLATE_FILE = $templateFixture
    $env:MADAPI_CODEX_AUTH_KIND = 'apikey'
& $refreshScript -CodexHome $codexHome | Out-Host
    $catalog = Get-Content -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $slugs = @($catalog.models | ForEach-Object { [string]$_.slug })
    Assert-True ('gpt-5.3-codex' -in $slugs) 'Sol Pro CPA compatibility slug is missing.'
    Assert-True ('gpt-5.2' -in $slugs) 'Terra Pro CPA compatibility slug is missing.'
    Assert-True ('gpt-image-2' -notin $slugs) 'Image-only model leaked into the conversation selector.'
    Assert-True ('seedance-2.0-fast' -notin $slugs) 'Video-only model leaked into the conversation selector.'
    $sol = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-sol' })[0]
    $solPro = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.3-codex' })[0]
    Assert-True ($sol.default_reasoning_level -eq $solPro.default_reasoning_level) 'Sol Pro capability profile differs from Sol.'
    $terra = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.6-terra' })[0]
    $terraPro = @($catalog.models | Where-Object { $_.slug -eq 'gpt-5.2' })[0]
    Assert-True ($terra.default_reasoning_level -eq $terraPro.default_reasoning_level) 'Terra Pro capability profile differs from Terra.'

    $apiSlugs = @($catalog.models | ForEach-Object { [string]$_.display_name })
    $expectedApi = @('claude-fable-5','claude-opus-5','gpt-5.6-sol','gpt-5.6-terra','gpt-5.6-luna','grok-4.6','gpt-5.6-sol-pro','gpt-5.6-terra-pro')
    Assert-True (($apiSlugs -join '|') -eq ($expectedApi -join '|')) 'API catalog is not exactly the fixed eight models.'

    $env:MADAPI_CODEX_AUTH_KIND = 'oauth'
    $env:MADAPI_REFRESH_RESPONSE_FILE = $oauthModelsFixture
    & $refreshScript -CodexHome $codexHome | Out-Host
    $oauthCatalog = Get-Content -LiteralPath (Join-Path $codexHome 'madapi-cockpit-model-catalog.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $oauthSlugs = @($oauthCatalog.models | ForEach-Object { [string]$_.slug })
    Assert-True ($oauthSlugs.Count -eq 17) 'OAuth catalog does not contain exactly 17 conversation models.'
    Assert-True ('gpt-image-2' -notin $oauthSlugs -and 'seedance-2.0-fast' -notin $oauthSlugs) 'OAuth media model leaked into the conversation catalog.'
    Assert-True ('gpt-5.6-sol' -in $oauthSlugs -and 'gpt-5.6-terra' -in $oauthSlugs) 'OAuth Pro/base model catalog is incomplete.'
    Assert-True ('grok-4.6' -in $oauthSlugs) 'OAuth catalog is missing grok-4.6.'
    $oauthGrok = @($oauthCatalog.models | Where-Object { $_.slug -eq 'grok-4.6' })[0]
    $grokProfile = @($catalog.models | Where-Object { $_.display_name -eq 'grok-4.6' })[0]
    Assert-True ($oauthGrok.default_reasoning_level -eq $grokProfile.default_reasoning_level) 'OAuth grok-4.6 capability profile differs from the registered API slot.'

    Write-Host 'Codex Windows clean gateway acceptance passed.'
} finally {
    foreach ($name in $environmentNames) { [Environment]::SetEnvironmentVariable($name, $oldEnvironment[$name], 'Process') }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
