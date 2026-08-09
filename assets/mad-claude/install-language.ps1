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
        & powershell.exe @arguments
        return $LASTEXITCODE
    }
    $process = Start-Process -FilePath 'powershell.exe' -ArgumentList ($arguments -join ' ') -Verb RunAs -Wait -PassThru
    return $process.ExitCode
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

    Get-Process -Name Claude -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    Start-Process 'shell:AppsFolder\Claude_pzs8sxrjxfjjc!Claude'

    $package = Get-AppxPackage -Name Claude -ErrorAction Stop
    $resourcePath = Join-Path $package.InstallLocation 'app\resources\ion-dist\i18n\zh-CN.json'
    $configPath = Join-Path $env:LOCALAPPDATA 'Claude-3p\config.json'
    if (-not (Test-Path -LiteralPath $resourcePath -PathType Leaf)) {
        throw 'Claude Chinese resource verification failed.'
    }
    $locale = (Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json).locale
    if ([string]$locale -ne 'zh-CN') { throw 'Claude Chinese locale verification failed.' }
    Write-Host 'Claude Chinese interface installed in safe mode.'
} finally {
    Remove-Item Env:CLAUDE_ZH_SKIP_UPDATE_CHECK -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $stageRoot) { Remove-Item -LiteralPath $stageRoot -Recurse -Force }
}
