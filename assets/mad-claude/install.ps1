$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Read-JsonFile {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return [pscustomobject]@{}
    }
    $text = [IO.File]::ReadAllText($Path, (New-Object Text.UTF8Encoding($false, $true)))
    if ([string]::IsNullOrWhiteSpace($text)) { return [pscustomobject]@{} }
    return $text | ConvertFrom-Json
}

function Set-JsonProperty {
    param($Object, [string]$Name, $Value)
    $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value -Force
}

function Write-JsonAtomic {
    param([string]$Path, $Value)
    $directory = Split-Path -Parent $Path
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $temporary = $Path + '.madapi.' + [guid]::NewGuid().ToString('N') + '.tmp'
    $json = $Value | ConvertTo-Json -Depth 50
    [IO.File]::WriteAllText($temporary, $json, (New-Object Text.UTF8Encoding($false)))
    $null = [IO.File]::ReadAllText($temporary, (New-Object Text.UTF8Encoding($false, $true))) | ConvertFrom-Json
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function Backup-File {
    param([string]$Path, [string]$BackupPath)
    if (Test-Path -LiteralPath $Path -PathType Leaf) {
        [IO.File]::Copy($Path, $BackupPath, $true)
        return $true
    }
    return $false
}

function Restore-File {
    param([string]$Path, [string]$BackupPath, [bool]$HadOriginal)
    if ($HadOriginal -and (Test-Path -LiteralPath $BackupPath -PathType Leaf)) {
        [IO.File]::Copy($BackupPath, $Path, $true)
    } elseif (Test-Path -LiteralPath $Path) {
        Remove-Item -LiteralPath $Path -Force
    }
}

function Merge-ClaudeConfig {
    param([string]$Path, [string]$ServerPath, [string]$NodeCommand)
    $config = Read-JsonFile $Path
    Set-JsonProperty $config 'deploymentMode' '3p'
    $mcpServersProperty = $config.PSObject.Properties['mcpServers']
    if ($null -eq $mcpServersProperty -or $null -eq $mcpServersProperty.Value) {
        $mcpServers = [pscustomobject]@{}
        Set-JsonProperty $config 'mcpServers' $mcpServers
    } else {
        $mcpServers = $mcpServersProperty.Value
    }
    $imageServer = [ordered]@{
        command = $NodeCommand
        args = @($ServerPath)
    }
    Set-JsonProperty $mcpServers 'madapi-image' ([pscustomobject]$imageServer)
    Write-JsonAtomic $Path $config
}

function Stop-ClaudeDesktop {
    $processes = @(Get-Process -Name 'Claude' -ErrorAction SilentlyContinue)
    foreach ($process in $processes) {
        try { $null = $process.CloseMainWindow() } catch { }
    }
    for ($attempt = 0; $attempt -lt 10; $attempt++) {
        if (-not (Get-Process -Name 'Claude' -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 500
    }
    Get-Process -Name 'Claude' -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
}

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey) -or $apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY is missing or invalid.'
}

$models = @(
    'claude-fable-5',
    'claude-opus-4-8',
    'claude-opus-5',
    'claude-sonnet-5',
    'claude-haiku-4-5'
)
$baseUrl = if ([string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_BASE_URL)) {
    'https://mad.myddns.me/v1'
} else {
    ([string]$env:MADAPI_CLAUDE_BASE_URL).TrimEnd('/')
}
$testMode = [string]$env:MADAPI_INSTALL_TEST_MODE -eq '1'

Write-Host 'Checking MadAPI model access...'
if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_MODELS_FIXTURE_PATH)) {
    $modelsJson = [IO.File]::ReadAllText([string]$env:MADAPI_MODELS_FIXTURE_PATH, [Text.Encoding]::UTF8)
} else {
    $response = Invoke-WebRequest -UseBasicParsing -Uri ($baseUrl + '/models') -Method Get -Headers @{
        'x-api-key' = $apiKey
        'Accept' = 'application/json'
    } -TimeoutSec 30
    $modelsJson = $response.Content
}
$modelResponse = $modelsJson | ConvertFrom-Json
$available = @($modelResponse.data | ForEach-Object { [string]$_.id })
$missing = @($models | Where-Object { $_ -notin $available })
if ($missing.Count -gt 0) {
    throw ('The API key cannot access: ' + ($missing -join ', ') + '. No files were changed.')
}
Write-Host 'All five Claude models are available.'

$normalRoot = if ([string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_NORMAL_DIR)) {
    Join-Path $env:APPDATA 'Claude'
} else { [string]$env:MADAPI_CLAUDE_NORMAL_DIR }
$threePRoot = if ([string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_THREEP_DIR)) {
    Join-Path $env:LOCALAPPDATA 'Claude-3p'
} else { [string]$env:MADAPI_CLAUDE_THREEP_DIR }
$toolRoot = if ([string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_TOOL_DIR)) {
    Join-Path $env:LOCALAPPDATA 'MadAPI\claude-image-tool'
} else { [string]$env:MADAPI_CLAUDE_TOOL_DIR }

$normalConfig = Join-Path $normalRoot 'claude_desktop_config.json'
$threePConfig = Join-Path $threePRoot 'claude_desktop_config.json'
$libraryDir = Join-Path $threePRoot 'configLibrary'
$metaPath = Join-Path $libraryDir '_meta.json'
$configId = [guid]::NewGuid().ToString()
$gatewayPath = Join-Path $libraryDir ($configId + '.json')
$serverPath = Join-Path $toolRoot 'server.mjs'
$widgetPath = Join-Path $toolRoot 'widget.html'
$portableNodePath = Join-Path $toolRoot 'runtime\node.exe'

New-Item -ItemType Directory -Path $normalRoot -Force | Out-Null
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backupRoot = Join-Path $normalRoot ('madapi-claude-backup-' + $stamp + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
$normalBackup = Join-Path $backupRoot 'normal-config.json'
$threePBackup = Join-Path $backupRoot 'threep-config.json'
$metaBackup = Join-Path $backupRoot 'library-meta.json'
$toolBackup = Join-Path $backupRoot 'image-tool'
$hadNormal = Backup-File $normalConfig $normalBackup
$hadThreeP = Backup-File $threePConfig $threePBackup
$hadMeta = Backup-File $metaPath $metaBackup
$hadTool = Test-Path -LiteralPath $toolRoot -PathType Container
if ($hadTool) { Copy-Item -LiteralPath $toolRoot -Destination $toolBackup -Recurse -Force }

$stageRoot = Join-Path ([IO.Path]::GetTempPath()) ('madapi-claude-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null
$stageServer = Join-Path $stageRoot 'server.mjs'
$stageWidget = Join-Path $stageRoot 'widget.html'
$stageNode = Join-Path $stageRoot 'node.exe'

try {
    if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_IMAGE_SOURCE_DIR)) {
        $sourceDir = [string]$env:MADAPI_CLAUDE_IMAGE_SOURCE_DIR
        [IO.File]::Copy((Join-Path $sourceDir 'server.mjs'), $stageServer, $true)
        [IO.File]::Copy((Join-Path $sourceDir 'widget.html'), $stageWidget, $true)
    } else {
        $assetUrl = 'https://mad.myddns.me/mad-claude/image-tool'
        Invoke-WebRequest -UseBasicParsing -Uri ($assetUrl + '/server.mjs') -OutFile $stageServer -TimeoutSec 60
        Invoke-WebRequest -UseBasicParsing -Uri ($assetUrl + '/widget.html') -OutFile $stageWidget -TimeoutSec 60
    }
    if ((Get-Item -LiteralPath $stageServer).Length -lt 1000 -or (Get-Item -LiteralPath $stageWidget).Length -lt 500) {
        throw 'Image tool assets are incomplete. No files were changed.'
    }

    $systemNode = Get-Command node.exe -ErrorAction SilentlyContinue | Select-Object -First 1
    $forcePortableNode = [string]$env:MADAPI_CLAUDE_FORCE_PORTABLE_NODE -eq '1'
    if (-not $forcePortableNode -and $null -ne $systemNode) {
        $nodeCommand = [string]$systemNode.Source
    } else {
        $nodeCommand = $portableNodePath
        $runtimeSource = [string]$env:MADAPI_CLAUDE_NODE_RUNTIME_PATH
        if (-not [string]::IsNullOrWhiteSpace($runtimeSource)) {
            if (-not (Test-Path -LiteralPath $runtimeSource -PathType Leaf)) {
                throw 'Configured portable Node runtime is missing.'
            }
            [IO.File]::Copy($runtimeSource, $stageNode, $true)
        } elseif (-not (Test-Path -LiteralPath $portableNodePath -PathType Leaf)) {
            $nodeVersion = '22.23.2'
            $nodeArchive = Join-Path $stageRoot ('node-v' + $nodeVersion + '-win-x64.zip')
            $nodeExtract = Join-Path $stageRoot 'node-runtime'
            Invoke-WebRequest -UseBasicParsing `
                -Uri ('https://nodejs.org/dist/v' + $nodeVersion + '/node-v' + $nodeVersion + '-win-x64.zip') `
                -OutFile $nodeArchive `
                -TimeoutSec 180
            $nodeHash = (Get-FileHash -LiteralPath $nodeArchive -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($nodeHash -ne '1177b4137ba5adaa56354ae40f1080c7450e8ae09cecb47da459d1c52ac99f97') {
                throw 'Portable Node archive checksum mismatch.'
            }
            Expand-Archive -LiteralPath $nodeArchive -DestinationPath $nodeExtract -Force
            $downloadedNode = Get-ChildItem -LiteralPath $nodeExtract -Recurse -File -Filter 'node.exe' | Select-Object -First 1
            if ($null -eq $downloadedNode) { throw 'Portable Node runtime is incomplete.' }
            [IO.File]::Copy($downloadedNode.FullName, $stageNode, $true)
        }
    }

    if (-not $testMode) { Stop-ClaudeDesktop }

    New-Item -ItemType Directory -Path $toolRoot -Force | Out-Null
    [IO.File]::Copy($stageServer, $serverPath, $true)
    [IO.File]::Copy($stageWidget, $widgetPath, $true)
    if (Test-Path -LiteralPath $stageNode -PathType Leaf) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $portableNodePath) -Force | Out-Null
        [IO.File]::Copy($stageNode, $portableNodePath, $true)
    }

    Merge-ClaudeConfig $normalConfig $serverPath $nodeCommand
    Merge-ClaudeConfig $threePConfig $serverPath $nodeCommand

    New-Item -ItemType Directory -Path $libraryDir -Force | Out-Null
    $gateway = [ordered]@{
        coworkEgressAllowedHosts = @('*')
        disableDeploymentModeChooser = $true
        inferenceProvider = 'gateway'
        inferenceGatewayBaseUrl = $baseUrl
        inferenceGatewayApiKey = $apiKey
        inferenceGatewayAuthScheme = 'x-api-key'
        inferenceModels = @($models | ForEach-Object { [ordered]@{ name = $_ } })
    }
    Write-JsonAtomic $gatewayPath ([pscustomobject]$gateway)

    $meta = Read-JsonFile $metaPath
    $entries = @()
    $entriesProperty = $meta.PSObject.Properties['entries']
    if ($null -ne $entriesProperty -and $null -ne $entriesProperty.Value) {
        $entries = @($entriesProperty.Value | Where-Object { [string]$_.name -ne 'MadAPI' })
    }
    $entries += [pscustomobject]([ordered]@{ id = $configId; name = 'MadAPI' })
    Set-JsonProperty $meta 'appliedId' $configId
    Set-JsonProperty $meta 'entries' $entries
    Write-JsonAtomic $metaPath $meta

    if ([string]$env:MADAPI_FORCE_POSTWRITE_FAILURE -eq '1') {
        throw 'Forced post-write failure for rollback acceptance.'
    }

    $writtenGateway = Read-JsonFile $gatewayPath
    $writtenMeta = Read-JsonFile $metaPath
    if ([string]$writtenMeta.appliedId -ne $configId) { throw 'Gateway activation check failed.' }
    $writtenModels = @($writtenGateway.inferenceModels | ForEach-Object { [string]$_.name })
    if (@($models | Where-Object { $_ -notin $writtenModels }).Count -gt 0) {
        throw 'Gateway model verification failed.'
    }

    if (-not $testMode -and [string]$env:MADAPI_CLAUDE_SKIP_LANGUAGE -ne '1') {
        $languageInstaller = [string]$env:MADAPI_CLAUDE_LANGUAGE_INSTALLER_PATH
        if ([string]::IsNullOrWhiteSpace($languageInstaller)) {
            $languageInstaller = Join-Path $stageRoot 'install-language.ps1'
            Invoke-WebRequest -UseBasicParsing -Uri 'https://mad.myddns.me/mad-claude/install-language.ps1' -OutFile $languageInstaller -TimeoutSec 60
        }
        & $languageInstaller
    }

    Write-Host 'Claude Desktop MadAPI setup completed.'
    Write-Host ('BACKUP=' + $backupRoot)
    if (-not $testMode) {
        try { Start-Process 'shell:AppsFolder\Claude_pzs8sxrjxfjjc!Claude' }
        catch {
            $candidates = @(
                (Join-Path $env:LOCALAPPDATA 'AnthropicClaude\claude.exe'),
                (Join-Path $env:LOCALAPPDATA 'Programs\Claude\Claude.exe'),
                (Join-Path $env:LOCALAPPDATA 'Claude\Claude.exe'),
                (Join-Path $env:ProgramFiles 'Claude\Claude.exe')
            )
            $claudeExe = $candidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
            if ($claudeExe) { Start-Process -FilePath $claudeExe }
            else { Write-Host 'Start Claude Desktop manually.' }
        }
    }
} catch {
    Restore-File $normalConfig $normalBackup $hadNormal
    Restore-File $threePConfig $threePBackup $hadThreeP
    Restore-File $metaPath $metaBackup $hadMeta
    if (Test-Path -LiteralPath $gatewayPath) { Remove-Item -LiteralPath $gatewayPath -Force }
    if (Test-Path -LiteralPath $toolRoot) { Remove-Item -LiteralPath $toolRoot -Recurse -Force }
    if ($hadTool -and (Test-Path -LiteralPath $toolBackup -PathType Container)) {
        Copy-Item -LiteralPath $toolBackup -Destination $toolRoot -Recurse -Force
    }
    Write-Error $_.Exception.Message
    exit 1
} finally {
    if (Test-Path -LiteralPath $stageRoot) { Remove-Item -LiteralPath $stageRoot -Recurse -Force }
}
