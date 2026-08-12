param(
    [Parameter(Mandatory = $true)][string]$InstallerPath,
    [Parameter(Mandatory = $true)][string]$ImageToolPath
)

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

$root = Join-Path ([IO.Path]::GetTempPath()) ('madapi-claude-test-' + [guid]::NewGuid().ToString('N'))
$normal = Join-Path $root 'normal'
$threep = Join-Path $root 'threep'
$tool = Join-Path $root 'tool'
$library = Join-Path $threep 'configLibrary'
New-Item -ItemType Directory -Path $normal -Force | Out-Null
New-Item -ItemType Directory -Path $library -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $tool 'cache') -Force | Out-Null
$cacheSentinel = Join-Path $tool 'cache\keep-image.png'
[IO.File]::WriteAllBytes($cacheSentinel, [byte[]](1, 2, 3, 4))

$normalConfig = Join-Path $normal 'claude_desktop_config.json'
$threePConfig = Join-Path $threep 'claude_desktop_config.json'
$metaPath = Join-Path $library '_meta.json'
$modelsPath = Join-Path $root 'models.json'
$imageSource = Join-Path $root 'image-source'
New-Item -ItemType Directory -Path $imageSource -Force | Out-Null
[IO.File]::Copy((Join-Path $ImageToolPath 'server.mjs'), (Join-Path $imageSource 'server.mjs'), $true)
$injectedWidget = [IO.File]::ReadAllText((Join-Path $ImageToolPath 'widget.html'), [Text.Encoding]::UTF8)
$injectedWidget = $injectedWidget.Replace(
    '</head>',
    '<script src="/mad-home/default-theme.js"></script><script src="/mad-home/oauth-bridge-v3.js"></script></head>'
)
[IO.File]::WriteAllText((Join-Path $imageSource 'widget.html'), $injectedWidget, (New-Object Text.UTF8Encoding($false)))

Write-Utf8Json $normalConfig ([pscustomobject]@{
    oauthAccount = 'keep-account'
    historyMarker = 'keep-history'
    mcpServers = [pscustomobject]@{ existing = [pscustomobject]@{ command = 'keep' } }
})
Write-Utf8Json $threePConfig ([pscustomobject]@{ customSetting = 'keep-setting' })
Write-Utf8Json $metaPath ([pscustomobject]@{
    appliedId = 'existing-id'
    entries = @([pscustomobject]@{ id = 'existing-id'; name = 'Existing' })
})
$expectedModels = @(
    'claude-fable-5',
    'claude-opus-4-8',
    'claude-opus-5',
    'claude-sonnet-5',
    'claude-haiku-4-5'
)
Write-Utf8Json $modelsPath ([pscustomobject]@{
    data = @($expectedModels | ForEach-Object { [pscustomobject]@{ id = $_ } })
})

$oldEnvironment = @{}
$environmentNames = @(
    'MADAPI_KEY',
    'MADAPI_INSTALL_TEST_MODE',
    'MADAPI_MODELS_FIXTURE_PATH',
    'MADAPI_CLAUDE_NORMAL_DIR',
    'MADAPI_CLAUDE_THREEP_DIR',
    'MADAPI_CLAUDE_TOOL_DIR',
    'MADAPI_CLAUDE_IMAGE_SOURCE_DIR',
    'MADAPI_CLAUDE_FORCE_PORTABLE_NODE',
    'MADAPI_CLAUDE_NODE_RUNTIME_PATH',
    'MADAPI_FORCE_POSTWRITE_FAILURE',
    'MADAPI_CLAUDE_SKIP_LANGUAGE',
    'MADAPI_CLAUDE_INSTALL_LANGUAGE',
    'MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH'
    'MADAPI_CLAUDE_BASE_URL'
    'MADAPI_BASE_URL'
)
foreach ($name in $environmentNames) {
    $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

try {
    $env:MADAPI_KEY = 'sk-test-claude-installer'
    $env:MADAPI_INSTALL_TEST_MODE = '1'
    $env:MADAPI_MODELS_FIXTURE_PATH = $modelsPath
    $env:MADAPI_CLAUDE_NORMAL_DIR = $normal
    $env:MADAPI_CLAUDE_THREEP_DIR = $threep
    $env:MADAPI_CLAUDE_TOOL_DIR = $tool
    $env:MADAPI_CLAUDE_IMAGE_SOURCE_DIR = $imageSource
    $env:MADAPI_CLAUDE_BASE_URL = 'https://mad.myddns.me/v1/'
    $env:MADAPI_BASE_URL = 'https://mad.myddns.me/v1'
    & $InstallerPath | Out-Host
    & $InstallerPath | Out-Host

    $normalResult = Get-Content -LiteralPath $normalConfig -Raw -Encoding UTF8 | ConvertFrom-Json
    $threePResult = Get-Content -LiteralPath $threePConfig -Raw -Encoding UTF8 | ConvertFrom-Json
    $metaResult = Get-Content -LiteralPath $metaPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $gatewayPath = Join-Path $library ([string]$metaResult.appliedId + '.json')
    $gateway = Get-Content -LiteralPath $gatewayPath -Raw -Encoding UTF8 | ConvertFrom-Json

    Assert-True ($normalResult.oauthAccount -eq 'keep-account') 'OAuth account data was not preserved.'
    Assert-True ($normalResult.historyMarker -eq 'keep-history') 'History marker was not preserved.'
    Assert-True ($normalResult.mcpServers.existing.command -eq 'keep') 'Existing MCP server was not preserved.'
    Assert-True (-not [string]::IsNullOrWhiteSpace([string]$normalResult.mcpServers.'madapi-image'.command)) 'Image MCP command is missing.'
    Assert-True ($normalResult.deploymentMode -eq '3p') 'Normal config is not in gateway mode.'
    Assert-True ($threePResult.customSetting -eq 'keep-setting') '3p custom setting was not preserved.'
    Assert-True ($threePResult.deploymentMode -eq '3p') '3p config is not in gateway mode.'
    Assert-True (Test-Path -LiteralPath (Join-Path $tool 'server.mjs')) 'Image MCP server was not installed.'
    Assert-True (Test-Path -LiteralPath (Join-Path $tool 'widget.html')) 'Image MCP widget was not installed.'
    Assert-True (-not ([IO.File]::ReadAllText((Join-Path $tool 'widget.html')).Contains('/mad-home/'))) 'Injected site scripts were not removed from the image widget.'
    Assert-True (Test-Path -LiteralPath $cacheSentinel) 'Repeated install deleted the image cache.'
    $actualModels = @($gateway.inferenceModels | ForEach-Object { [string]$_.name })
    Assert-True (($actualModels -join '|') -eq ($expectedModels -join '|')) 'Gateway model list is incorrect.'
    Assert-True (@($metaResult.entries | Where-Object { $_.name -eq 'MadAPI' }).Count -eq 1) 'Repeated install created duplicate MadAPI entries.'
    Assert-True (@($metaResult.entries | Where-Object { $_.name -eq 'Existing' }).Count -eq 1) 'Existing config library entry was lost.'
    Assert-True ([string]$gateway.inferenceGatewayBaseUrl -eq 'https://mad.myddns.me') 'Gateway base URL was not normalized to the root.'
    $installerText = [IO.File]::ReadAllText($InstallerPath, [Text.Encoding]::UTF8)
    $serverText = [IO.File]::ReadAllText((Join-Path $ImageToolPath 'server.mjs'), [Text.Encoding]::UTF8)
    Assert-True ($installerText.Contains('($baseUrl + ''/v1/models'')')) 'Models endpoint does not use /v1/models.'
    Assert-True ($serverText.Contains('${baseUrl}/v1/images/generations')) 'Image endpoint does not use /v1/images/generations.'

    $languageFailure = Join-Path $root 'language-failure.ps1'
    [IO.File]::WriteAllText(
        $languageFailure,
        "throw 'Optional language fixture failed.'",
        (New-Object Text.UTF8Encoding($false))
    )
    $env:MADAPI_CLAUDE_INSTALL_LANGUAGE = '1'
    $env:MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH = $languageFailure
    & $InstallerPath | Out-Host
    Remove-Item Env:MADAPI_CLAUDE_INSTALL_LANGUAGE -ErrorAction SilentlyContinue
    Remove-Item Env:MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH -ErrorAction SilentlyContinue
    $languageFailureResult = Get-Content -LiteralPath $normalConfig -Raw -Encoding UTF8 | ConvertFrom-Json
    Assert-True ($languageFailureResult.oauthAccount -eq 'keep-account') 'Optional language failure rolled back OAuth data.'
    Assert-True (-not [string]::IsNullOrWhiteSpace([string]$languageFailureResult.mcpServers.'madapi-image'.command)) 'Optional language failure removed the image tool.'

    $env:MADAPI_CLAUDE_FORCE_PORTABLE_NODE = '1'
    $env:MADAPI_CLAUDE_NODE_RUNTIME_PATH = (Get-Command node.exe -ErrorAction Stop).Source
    & $InstallerPath | Out-Host
    $portableResult = Get-Content -LiteralPath $normalConfig -Raw -Encoding UTF8 | ConvertFrom-Json
    $portableNode = Join-Path $tool 'runtime\node.exe'
    Assert-True (Test-Path -LiteralPath $portableNode -PathType Leaf) 'Portable Node runtime was not installed.'
    Assert-True ([string]$portableResult.mcpServers.'madapi-image'.command -eq $portableNode) 'Portable Node command is incorrect.'
    Remove-Item Env:MADAPI_CLAUDE_FORCE_PORTABLE_NODE -ErrorAction SilentlyContinue
    Remove-Item Env:MADAPI_CLAUDE_NODE_RUNTIME_PATH -ErrorAction SilentlyContinue

    $normalHash = (Get-FileHash -LiteralPath $normalConfig -Algorithm SHA256).Hash
    $threePHash = (Get-FileHash -LiteralPath $threePConfig -Algorithm SHA256).Hash
    $metaHash = (Get-FileHash -LiteralPath $metaPath -Algorithm SHA256).Hash
    Write-Utf8Json $modelsPath ([pscustomobject]@{
        data = @($expectedModels[0..3] | ForEach-Object { [pscustomobject]@{ id = $_ } })
    })
    $failed = $false
    try { & $InstallerPath | Out-Null } catch { $failed = $true }
    Assert-True $failed 'Missing model validation did not fail.'
    Assert-True ((Get-FileHash -LiteralPath $normalConfig -Algorithm SHA256).Hash -eq $normalHash) 'Failed validation changed normal config.'
    Assert-True ((Get-FileHash -LiteralPath $threePConfig -Algorithm SHA256).Hash -eq $threePHash) 'Failed validation changed 3p config.'
    Assert-True ((Get-FileHash -LiteralPath $metaPath -Algorithm SHA256).Hash -eq $metaHash) 'Failed validation changed library metadata.'

    Write-Utf8Json $modelsPath ([pscustomobject]@{
        data = @($expectedModels | ForEach-Object { [pscustomobject]@{ id = $_ } })
    })
    $env:MADAPI_FORCE_POSTWRITE_FAILURE = '1'
    $rollbackFailed = $false
    try { & $InstallerPath | Out-Null } catch { $rollbackFailed = $true }
    Assert-True $rollbackFailed 'Forced post-write failure did not fail.'
    Assert-True ((Get-FileHash -LiteralPath $normalConfig -Algorithm SHA256).Hash -eq $normalHash) 'Rollback did not restore normal config.'
    Assert-True ((Get-FileHash -LiteralPath $threePConfig -Algorithm SHA256).Hash -eq $threePHash) 'Rollback did not restore 3p config.'
    Assert-True ((Get-FileHash -LiteralPath $metaPath -Algorithm SHA256).Hash -eq $metaHash) 'Rollback did not restore library metadata.'
    Assert-True (Test-Path -LiteralPath $cacheSentinel) 'Rollback did not preserve the image cache.'
    Remove-Item Env:MADAPI_FORCE_POSTWRITE_FAILURE -ErrorAction SilentlyContinue

    Write-Host 'Claude Windows installer acceptance passed.'
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $oldEnvironment[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
