$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Write-Utf8Json {
    param([string]$Path, $Value)
    $json = $Value | ConvertTo-Json -Depth 50
    [IO.File]::WriteAllText($Path, $json, (New-Object Text.UTF8Encoding($false)))
}

function Invoke-CodexInstallerCase {
    param(
        [string]$Root,
        [string]$Mode,
        [string]$Installer,
        [string]$RefreshScript,
        [string]$HistoryScript
    )

    $codexCaseHome = Join-Path $Root ('codex-' + $Mode)
    New-Item -ItemType Directory -Path $codexCaseHome -Force | Out-Null
    $configPath = Join-Path $codexCaseHome 'config.toml'
    $authPath = Join-Path $codexCaseHome 'auth.json'
    [IO.File]::WriteAllText(
        $configPath,
        "model_provider = `"custom`"`r`nmodel = `"gpt-5.6-sol`"`r`n`r`n[model_providers.custom]`r`nname = `"custom`"`r`nbase_url = `"https://old.invalid/v1`"`r`n",
        (New-Object Text.UTF8Encoding($false))
    )
    if ($Mode -eq 'oauth') {
        Write-Utf8Json $authPath ([ordered]@{
            auth_mode = 'chatgpt'
            tokens = [ordered]@{ access_token = 'oauth-access'; refresh_token = 'oauth-refresh' }
        })
    } else {
        Write-Utf8Json $authPath ([ordered]@{ auth_mode = 'apikey'; OPENAI_API_KEY = 'existing-key' })
    }
    $authHash = (Get-FileHash -LiteralPath $authPath -Algorithm SHA256).Hash

    $env:CODEX_HOME = $codexCaseHome
    $env:MADAPI_KEY = 'sk-test-agent-access'
    $env:MADAPI_BASE_URL = 'https://mad.test'
    $env:MADAPI_CODEX_LOGIN_MODE = $Mode
    $env:MADAPI_INSTALL_TEST_MODE = '1'
    $env:MADAPI_REFRESH_SCRIPT_SOURCE = $RefreshScript
    $env:MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE = $HistoryScript
    & $Installer | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "Codex $Mode installer failed." }

    $config = [IO.File]::ReadAllText($configPath, [Text.Encoding]::UTF8)
    Assert-True ($config -match '(?m)^model_provider = "custom"\s*$') "Codex $Mode provider changed."
    Assert-True ($config -match '(?m)^base_url = "https://mad.test/codex/v1"\s*$') "Codex $Mode gateway URL is wrong."
    Assert-True ($config -match '(?m)^env_key = "MADAPI_API_KEY"\s*$') "Codex $Mode env key is missing."
    Assert-True ($config -match '(?m)^image_generation = true\s*$') "Codex $Mode image feature is missing."
    Assert-True ((Get-FileHash -LiteralPath $authPath -Algorithm SHA256).Hash -eq $authHash) "Codex $Mode auth state changed."
    Assert-True (Test-Path -LiteralPath (Join-Path $codexCaseHome 'madapi-cockpit-model-catalog.json')) "Codex $Mode catalog is missing."
    Assert-True (Test-Path -LiteralPath (Join-Path $codexCaseHome 'madapi-refresh-model-catalog.ps1')) "Codex $Mode refresh script is missing."
    Assert-True (Test-Path -LiteralPath (Join-Path $codexCaseHome 'madapi-restore-history.ps1')) "Codex $Mode history script is missing."
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$codexRoot = Join-Path $repo 'web\public\mad-codex'
$claudeRoot = Join-Path $repo 'web\public\mad-claude'
$root = Join-Path ([IO.Path]::GetTempPath()) ('madapi-agent-access-' + [guid]::NewGuid().ToString('N'))
$savedEnvironment = @{}
$environmentNames = @(
    'CODEX_HOME', 'MADAPI_KEY', 'MADAPI_BASE_URL', 'MADAPI_CODEX_LOGIN_MODE',
    'MADAPI_INSTALL_TEST_MODE', 'MADAPI_REFRESH_SCRIPT_SOURCE',
    'MADAPI_HISTORY_RESTORE_SCRIPT_SOURCE', 'MADAPI_CLAUDE_BASE_URL',
    'MADAPI_MODELS_FIXTURE_PATH', 'MADAPI_CLAUDE_NORMAL_DIR',
    'MADAPI_CLAUDE_THREEP_DIR', 'MADAPI_CLAUDE_TOOL_DIR',
    'MADAPI_CLAUDE_IMAGE_SOURCE_DIR', 'MADAPI_CLAUDE_INSTALL_LANGUAGE',
    'MADAPI_CLAUDE_SKIP_LANGUAGE', 'MADAPI_FORCE_POSTWRITE_FAILURE'
)
foreach ($name in $environmentNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    New-Item -ItemType Directory -Path $root -Force | Out-Null

    foreach ($script in @(
        (Join-Path $codexRoot 'install.ps1'),
        (Join-Path $codexRoot 'refresh-model-catalog.ps1'),
        (Join-Path $codexRoot 'restore-history.ps1'),
        (Join-Path $claudeRoot 'install.ps1'),
        (Join-Path $claudeRoot 'install-language.ps1')
    )) {
        $tokens = $null
        $errors = $null
        [void][Management.Automation.Language.Parser]::ParseFile($script, [ref]$tokens, [ref]$errors)
        Assert-True (@($errors).Count -eq 0) ("PowerShell parse failed: " + $script)
    }

    Invoke-CodexInstallerCase `
        -Root $root `
        -Mode 'oauth' `
        -Installer (Join-Path $codexRoot 'install.ps1') `
        -RefreshScript (Join-Path $codexRoot 'refresh-model-catalog.ps1') `
        -HistoryScript (Join-Path $codexRoot 'restore-history.ps1')
    Invoke-CodexInstallerCase `
        -Root $root `
        -Mode 'apikey' `
        -Installer (Join-Path $codexRoot 'install.ps1') `
        -RefreshScript (Join-Path $codexRoot 'refresh-model-catalog.ps1') `
        -HistoryScript (Join-Path $codexRoot 'restore-history.ps1')

    $fixture = Join-Path $root 'claude-models.json'
    Write-Utf8Json $fixture ([ordered]@{ data = @(
        'claude-fable-5', 'claude-opus-4-8', 'claude-opus-5',
        'claude-sonnet-5', 'claude-haiku-4-5'
    ) | ForEach-Object { [ordered]@{ id = $_ } } })
    $normalRoot = Join-Path $root 'claude-normal'
    $threePRoot = Join-Path $root 'claude-3p'
    $toolRoot = Join-Path $root 'claude-tool'
    New-Item -ItemType Directory -Path $normalRoot -Force | Out-Null
    Write-Utf8Json (Join-Path $normalRoot 'claude_desktop_config.json') ([ordered]@{ preserved = 'yes' })

    $env:MADAPI_KEY = 'sk-test-agent-access'
    $env:MADAPI_BASE_URL = 'https://mad.test'
    $env:MADAPI_CLAUDE_BASE_URL = 'https://mad.test/v1'
    $env:MADAPI_INSTALL_TEST_MODE = '1'
    $env:MADAPI_MODELS_FIXTURE_PATH = $fixture
    $env:MADAPI_CLAUDE_NORMAL_DIR = $normalRoot
    $env:MADAPI_CLAUDE_THREEP_DIR = $threePRoot
    $env:MADAPI_CLAUDE_TOOL_DIR = $toolRoot
    $env:MADAPI_CLAUDE_IMAGE_SOURCE_DIR = Join-Path $claudeRoot 'image-tool'
    $env:MADAPI_CLAUDE_INSTALL_LANGUAGE = '1'
    $env:MADAPI_CLAUDE_SKIP_LANGUAGE = '1'
    & (Join-Path $claudeRoot 'install.ps1') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Claude installer failed.' }

    $normal = [IO.File]::ReadAllText((Join-Path $normalRoot 'claude_desktop_config.json'), [Text.Encoding]::UTF8) | ConvertFrom-Json
    Assert-True ([string]$normal.preserved -eq 'yes') 'Claude existing config was not preserved.'
    Assert-True ([string]$normal.deploymentMode -eq '3p') 'Claude deployment mode is missing.'
    Assert-True ($null -ne $normal.mcpServers.'madapi-image') 'Claude image MCP entry is missing.'
    Assert-True (Test-Path -LiteralPath (Join-Path $toolRoot 'server.mjs')) 'Claude image server is missing.'
    Assert-True (Test-Path -LiteralPath (Join-Path $toolRoot 'widget.html')) 'Claude image widget is missing.'

    $meta = [IO.File]::ReadAllText((Join-Path $threePRoot 'configLibrary\_meta.json'), [Text.Encoding]::UTF8) | ConvertFrom-Json
    $gatewayPath = Join-Path $threePRoot ('configLibrary\' + [string]$meta.appliedId + '.json')
    $gateway = [IO.File]::ReadAllText($gatewayPath, [Text.Encoding]::UTF8) | ConvertFrom-Json
    Assert-True ([string]$gateway.inferenceGatewayBaseUrl -eq 'https://mad.test/v1') 'Claude gateway URL is wrong.'
    Assert-True (@($gateway.inferenceModels).Count -eq 5) 'Claude model count is wrong.'
    Assert-True (@($gateway.inferenceModels | Where-Object { [string]$_.name -eq 'claude-haiku-4-5' }).Count -eq 1) 'Claude Haiku model is missing.'

    Write-Output 'AGENT_ACCESS_WINDOWS_ACCEPTANCE=PASS'
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
