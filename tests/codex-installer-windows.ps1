param(
    [Parameter(Mandatory = $true)]
    [string]$InstallerPath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) {
        throw "Assertion failed: $Message"
    }
}

function Get-Hash([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-Utf8NoBom([string]$Path, [string]$Content) {
    [IO.File]::WriteAllText($Path, $Content, (New-Object System.Text.UTF8Encoding($false)))
}

function Invoke-TestInstaller([string]$CodexHome, [string]$Key, [string]$CliPath) {
    $oldHome = [string]$env:CODEX_HOME
    $oldKey = [string]$env:MADAPI_KEY
    $oldCli = [string]$env:CODEX_CLI_PATH
    $oldPreference = $ErrorActionPreference
    try {
        $env:CODEX_HOME = $CodexHome
        $env:MADAPI_KEY = $Key
        $env:CODEX_CLI_PATH = $CliPath
        $ErrorActionPreference = 'Continue'
        $output = & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $InstallerPath 2>&1
        return [pscustomobject]@{
            ExitCode = $LASTEXITCODE
            Output = @($output | ForEach-Object { [string]$_ })
        }
    } finally {
        $ErrorActionPreference = $oldPreference
        foreach ($entry in @(
            @{ Name = 'CODEX_HOME'; Value = $oldHome },
            @{ Name = 'MADAPI_KEY'; Value = $oldKey },
            @{ Name = 'CODEX_CLI_PATH'; Value = $oldCli }
        )) {
            if ([string]::IsNullOrEmpty([string]$entry.Value)) {
                Remove-Item ("Env:" + $entry.Name) -ErrorAction SilentlyContinue
            } else {
                Set-Item ("Env:" + $entry.Name) ([string]$entry.Value)
            }
        }
    }
}

Assert-True ($PSVersionTable.PSVersion.Major -eq 5) 'The Windows acceptance test must use PowerShell 5.1.'
$codexCommand = Get-Command codex.cmd -ErrorAction Stop
$codexCli = [string]$codexCommand.Source
$sandbox = Join-Path $env:RUNNER_TEMP ('mad-codex-windows-' + [guid]::NewGuid().ToString('N'))

try {
    New-Item -ItemType Directory -Path $sandbox -Force | Out-Null
    $testHome = Join-Path $sandbox 'complex'
    $sessionDir = Join-Path $testHome 'sessions\2026\08\01'
    New-Item -ItemType Directory -Path $sessionDir -Force | Out-Null
    $configPath = Join-Path $testHome 'config.toml'
    $sessionPath = Join-Path $sessionDir 'sentinel.jsonl'
    $desktop = ([char]0x684c).ToString() + ([char]0x9762)
    $project = ([char]0x9879).ToString() + ([char]0x76ee)
    $projectPath = "D:\$desktop\$project"
    $config = @"
model_provider = "custom"
model = "old-model"
model_reasoning_effort = "medium"

[features]
memories = true

[model_providers.custom]
name = "Existing Provider"
base_url = "https://example.invalid/v1"
wire_api = "responses"

[model_providers.madapi]
experimental_bearer_token = "old-secret"

[projects.'$projectPath']
trust_level = "trusted"

[plugins."github@openai-curated"]
enabled = true

[mcp_servers.node_repl]
command = 'C:\Tools\node_repl.exe'
args = []
"@
    Write-Utf8NoBom $configPath ($config.Trim() + "`n")
    Write-Utf8NoBom $sessionPath '{"type":"sentinel"}'
    $configHash = Get-Hash $configPath
    $sessionHash = Get-Hash $sessionPath

    $first = Invoke-TestInstaller $testHome 'sk-windows-first-key' $codexCli
    Assert-True ($first.ExitCode -eq 0) ('First install failed: ' + ($first.Output -join ' | '))
    $installed = [IO.File]::ReadAllText($configPath, [Text.Encoding]::UTF8)
    Assert-True ($installed.Contains($projectPath)) 'UTF-8 project path was not preserved.'
    Assert-True ($installed.Contains('[model_providers.custom]')) 'Existing provider was not preserved.'
    Assert-True ($installed.Contains('[plugins."github@openai-curated"]')) 'Plugin configuration was not preserved.'
    Assert-True ($installed.Contains('[mcp_servers.node_repl]')) 'MCP configuration was not preserved.'
    Assert-True ($installed.Contains('model_reasoning_effort = "medium"')) 'Existing reasoning effort was overwritten.'
    Assert-True (-not $installed.Contains('old-secret')) 'Old MadAPI secret remained in config.toml.'
    Assert-True (-not $installed.Contains('sk-windows-first-key')) 'API key was written into config.toml.'
    Assert-True ($installed.Contains('madapi.key')) 'Protected key-file auth was not configured.'
    Assert-True ((Get-Hash $sessionPath) -eq $sessionHash) 'Session data changed.'

    $backup = @(Get-ChildItem -LiteralPath $testHome -Filter 'config.toml.madapi-backup-*' -File)
    Assert-True ($backup.Count -eq 1) 'Exactly one backup was not created.'
    Assert-True ((Get-Hash $backup[0].FullName) -eq $configHash) 'Backup is not byte-identical to the original config.'
    $keyPath = Join-Path $testHome 'madapi.key'
    Assert-True (([IO.File]::ReadAllText($keyPath)) -eq 'sk-windows-first-key') 'Protected key file has the wrong value.'
    Assert-True ((Get-Acl -LiteralPath $keyPath).AreAccessRulesProtected) 'Key file still inherits broad ACLs.'

    $oldHome = [string]$env:CODEX_HOME
    try {
        $env:CODEX_HOME = $testHome
        & $codexCli features list *> $null
        Assert-True ($LASTEXITCODE -eq 0) 'Actual Codex rejected the installed configuration.'
    } finally {
        if ([string]::IsNullOrEmpty($oldHome)) {
            Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
        } else {
            $env:CODEX_HOME = $oldHome
        }
    }

    Start-Sleep -Milliseconds 1100
    $second = Invoke-TestInstaller $testHome 'sk-windows-second-key' $codexCli
    Assert-True ($second.ExitCode -eq 0) 'Repeat install failed.'
    $reinstalled = [IO.File]::ReadAllText($configPath, [Text.Encoding]::UTF8)
    Assert-True (([regex]::Matches($reinstalled, '(?m)^\[model_providers\.madapi\]$')).Count -eq 1) 'Duplicate provider section was created.'
    Assert-True (([regex]::Matches($reinstalled, '(?m)^\[model_providers\.madapi\.auth\]$')).Count -eq 1) 'Duplicate auth section was created.'
    Assert-True (([IO.File]::ReadAllText($keyPath)) -eq 'sk-windows-second-key') 'Repeat install did not rotate the key.'

    $freshHome = Join-Path $sandbox 'fresh'
    $fresh = Invoke-TestInstaller $freshHome 'sk-windows-fresh-key' $codexCli
    Assert-True ($fresh.ExitCode -eq 0) 'Fresh install failed.'
    $oldHome = [string]$env:CODEX_HOME
    try {
        $env:CODEX_HOME = $freshHome
        & $codexCli --strict-config features list *> $null
        Assert-True ($LASTEXITCODE -eq 0) 'Actual Codex strictly rejected the clean generated configuration.'
    } finally {
        if ([string]::IsNullOrEmpty($oldHome)) {
            Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
        } else {
            $env:CODEX_HOME = $oldHome
        }
    }

    $badHome = Join-Path $sandbox 'malformed'
    New-Item -ItemType Directory -Path $badHome -Force | Out-Null
    $badConfig = Join-Path $badHome 'config.toml'
    Write-Utf8NoBom $badConfig "broken = [`n"
    $badHash = Get-Hash $badConfig
    $bad = Invoke-TestInstaller $badHome 'sk-windows-bad-key' $codexCli
    Assert-True ($bad.ExitCode -ne 0) 'Malformed existing config was accepted.'
    Assert-True ((Get-Hash $badConfig) -eq $badHash) 'Malformed existing config was changed.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $badHome 'madapi.key'))) 'Key file was created for a rejected config.'

    $rollbackHome = Join-Path $sandbox 'rollback'
    New-Item -ItemType Directory -Path $rollbackHome -Force | Out-Null
    $rollbackConfig = Join-Path $rollbackHome 'config.toml'
    $rollbackKey = Join-Path $rollbackHome 'madapi.key'
    Write-Utf8NoBom $rollbackConfig "model = \"old-model\"`n"
    Write-Utf8NoBom $rollbackKey 'sk-old-key'
    $rollbackHash = Get-Hash $rollbackConfig
    $fakeScript = Join-Path $sandbox 'fake-codex.ps1'
    $fakeCommand = Join-Path $sandbox 'fake-codex.cmd'
    $counterPath = Join-Path $sandbox 'fake-count.txt'
    Write-Utf8NoBom $fakeScript @'
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Rest)
if ($Rest.Count -gt 0 -and $Rest[0] -eq '--version') { exit 0 }
$path = $env:CODEX_FAKE_COUNT
$count = if (Test-Path -LiteralPath $path) { [int](Get-Content -LiteralPath $path -Raw) } else { 0 }
$count++
[IO.File]::WriteAllText($path, [string]$count)
if ($count -ge 2) { exit 1 }
exit 0
'@
    Write-Utf8NoBom $fakeCommand "@echo off`r`npowershell.exe -NoProfile -ExecutionPolicy Bypass -File \"$fakeScript\" %*`r`n"
    $oldCounter = [string]$env:CODEX_FAKE_COUNT
    try {
        $env:CODEX_FAKE_COUNT = $counterPath
        $rollback = Invoke-TestInstaller $rollbackHome 'sk-new-key' $fakeCommand
    } finally {
        if ([string]::IsNullOrEmpty($oldCounter)) {
            Remove-Item Env:CODEX_FAKE_COUNT -ErrorAction SilentlyContinue
        } else {
            $env:CODEX_FAKE_COUNT = $oldCounter
        }
    }
    Assert-True ($rollback.ExitCode -ne 0) 'Forced post-write validation failure was accepted.'
    Assert-True ((Get-Hash $rollbackConfig) -eq $rollbackHash) 'Config was not rolled back byte-for-byte.'
    Assert-True (([IO.File]::ReadAllText($rollbackKey)) -eq 'sk-old-key') 'Previous key was not restored.'

    Write-Host 'Windows PowerShell 5.1 Codex installer acceptance passed.'
} finally {
    if (Test-Path -LiteralPath $sandbox) {
        [IO.Directory]::Delete($sandbox, $true)
    }
}
