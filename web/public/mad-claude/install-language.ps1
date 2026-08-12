$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

$archiveUrl = 'https://codeload.github.com/javaht/claude-desktop-zh-cn/zip/b862463'
$archiveSha256 = '9371cf89bb89ea5453cace672dfaa1f57b10d4fd09fea72a96b079b916cb8822'
$stageRoot = Join-Path ([IO.Path]::GetTempPath()) ('madapi-claude-language-' + [guid]::NewGuid().ToString('N'))
$archivePath = Join-Path $stageRoot 'language-pack.zip'
$sourceRoot = Join-Path $stageRoot 'source'

function Invoke-ElevatedLanguageScript {
    param([string]$ScriptPath, [string]$Action)
    $arguments = @(
        '-NoProfile',
        '-ExecutionPolicy',
        'Bypass',
        '-File',
        ('"' + $ScriptPath + '"'),
        '-Action',
        $Action,
        '-Language',
        'zh-CN',
        '-PatchMode',
        'safe',
        '-SkipAsarPatch',
        '-OriginalUserProfile',
        ('"' + [Environment]::GetFolderPath('UserProfile') + '"'),
        '-OriginalAppData',
        ('"' + $env:APPDATA + '"'),
        '-OriginalLocalAppData',
        ('"' + $env:LOCALAPPDATA + '"')
    )
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        $process = Start-Process -FilePath 'powershell.exe' -ArgumentList ($arguments -join ' ') -WindowStyle Hidden -Wait -PassThru
        return [int]$process.ExitCode
    }
    $process = Start-Process -FilePath 'powershell.exe' -ArgumentList ($arguments -join ' ') -Verb RunAs -Wait -PassThru
    return [int]$process.ExitCode
}

function Set-ClaudeLocale([string]$ConfigPath) {
    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) { return }
    $config = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $property = $config.PSObject.Properties['locale']
    if ($null -eq $property) { $config | Add-Member -NotePropertyName locale -NotePropertyValue 'zh-CN' }
    else { $property.Value = 'zh-CN' }
    $json = $config | ConvertTo-Json -Depth 50
    [IO.File]::WriteAllText($ConfigPath, $json, (New-Object Text.UTF8Encoding($false)))
}

function Stop-ClaudeDesktop {
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        $processes = @(Get-Process -Name 'Claude' -ErrorAction SilentlyContinue)
        if ($processes.Count -eq 0) { return }
        $processes | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
    }
    if (Get-Process -Name 'Claude' -ErrorAction SilentlyContinue) {
        throw 'Claude Desktop could not be closed before locale verification.'
    }
}

New-Item -ItemType Directory -Path $sourceRoot -Force | Out-Null
try {
    if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_CLAUDE_LANGUAGE_ARCHIVE)) {
        [IO.File]::Copy([string]$env:MADAPI_CLAUDE_LANGUAGE_ARCHIVE, $archivePath, $true)
    } else {
        Invoke-WebRequest -UseBasicParsing -Uri $archiveUrl -OutFile $archivePath -TimeoutSec 120
    }
    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $archiveSha256) { throw 'Claude Chinese archive checksum mismatch.' }
    Expand-Archive -LiteralPath $archivePath -DestinationPath $sourceRoot -Force
    $project = Get-ChildItem -LiteralPath $sourceRoot -Directory | Select-Object -First 1
    if ($null -eq $project) { throw 'Claude Chinese archive root is missing.' }
    $installer = Join-Path $project.FullName 'scripts\install_windows.ps1'
    $frontend = Join-Path $project.FullName 'resources\frontend-zh-CN.json'
    if (-not (Test-Path -LiteralPath $installer -PathType Leaf) -or -not (Test-Path -LiteralPath $frontend -PathType Leaf)) {
        throw 'Claude Chinese archive is incomplete.'
    }
    $null = Get-Content -LiteralPath $frontend -Raw -Encoding UTF8 | ConvertFrom-Json

    if ([string]$env:MADAPI_CLAUDE_LANGUAGE_DRY_RUN -eq '1') {
        Write-Host 'Claude Chinese Windows package verification passed.'
        exit 0
    }

    $env:CLAUDE_ZH_SKIP_UPDATE_CHECK = '1'
    $exitCode = Invoke-ElevatedLanguageScript $installer 'install'
    if ($exitCode -ne 0) {
        try { $null = Invoke-ElevatedLanguageScript $installer 'uninstall' } catch { }
        throw ('Claude Chinese safe-mode install failed with exit code ' + $exitCode + '.')
    }
    Stop-ClaudeDesktop

    $resourcePaths = @()
    $package = Get-AppxPackage -Name Claude -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $package) {
        $resourcePaths += Join-Path $package.InstallLocation 'app\resources\ion-dist\i18n\zh-CN.json'
    }
    $unpackagedRoot = Join-Path $env:LOCALAPPDATA 'AnthropicClaude'
    if (Test-Path -LiteralPath $unpackagedRoot -PathType Container) {
        $resourcePaths += @(
            Get-ChildItem -LiteralPath $unpackagedRoot -Directory -Filter 'app-*' -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            ForEach-Object { Join-Path $_.FullName 'resources\ion-dist\i18n\zh-CN.json' }
        )
    }
    if (-not ($resourcePaths | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1)) {
        throw 'Claude Chinese resource verification failed.'
    }

    $configPaths = @(
        (Join-Path $env:APPDATA 'Claude\config.json'),
        (Join-Path $env:APPDATA 'Claude-3p\config.json'),
        (Join-Path $env:LOCALAPPDATA 'Claude-3p\config.json'),
        (Join-Path $env:LOCALAPPDATA 'Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude\config.json'),
        (Join-Path $env:LOCALAPPDATA 'Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude-3p\config.json')
    )
    foreach ($configPath in $configPaths) { Set-ClaudeLocale $configPath }
    $existingConfigs = @($configPaths | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
    if ($existingConfigs.Count -lt 1) { throw 'Claude Chinese locale configuration is missing.' }
    foreach ($configPath in $existingConfigs) {
        $locale = (Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json).locale
        if ([string]$locale -ne 'zh-CN') {
            throw ('Claude Chinese locale verification failed: ' + $configPath)
        }
    }
    Write-Host 'Claude Chinese interface installed in safe mode.'
} catch {
    $logRoot = Join-Path $env:LOCALAPPDATA 'MadAPI\claude-language-logs'
    New-Item -ItemType Directory -Path $logRoot -Force | Out-Null
    $logStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $copiedLogs = @()
    foreach ($sourceLog in @(Get-ChildItem -LiteralPath $stageRoot -Recurse -File -Filter '*.log' -ErrorAction SilentlyContinue)) {
        $targetLog = Join-Path $logRoot ($logStamp + '-' + $sourceLog.Name)
        Copy-Item -LiteralPath $sourceLog.FullName -Destination $targetLog -Force
        $copiedLogs += $targetLog
    }
    foreach ($copiedLog in $copiedLogs) { Write-Host ('LANGUAGE_LOG=' + $copiedLog) }
    throw
} finally {
    Remove-Item Env:CLAUDE_ZH_SKIP_UPDATE_CHECK -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $stageRoot) { Remove-Item -LiteralPath $stageRoot -Recurse -Force }
}
