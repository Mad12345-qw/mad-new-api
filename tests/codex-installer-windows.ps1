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
    $catalogFixture = (Join-Path $PSScriptRoot 'fixtures\codex-models.json').Replace('\', '/')
    $config = @"
model_provider = "custom"
model = "old-model"
model_catalog_json = "$catalogFixture"
model_reasoning_effort = "medium"

[features]
memories = true

[model_providers.custom]
name = "Existing Provider"
base_url = "https://example.invalid/v1"
wire_api = "responses"

[model_providers.madapi]
name = "Old MadAPI"
base_url = "https://old.example.invalid/v1"
wire_api = "responses"
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
    Assert-True ($installed.Contains('model_provider = "custom"')) 'Existing provider identity was changed.'
    Assert-True ($installed.Contains('name = "Existing Provider"')) 'Existing provider display name was changed.'
    Assert-True (-not $installed.Contains('[model_providers.madapi]')) 'A second provider identity was created.'
    Assert-True ($installed.Contains('[plugins."github@openai-curated"]')) 'Plugin configuration was not preserved.'
    Assert-True ($installed.Contains('[mcp_servers.node_repl]')) 'MCP configuration was not preserved.'
    Assert-True ($installed.Contains('model_reasoning_effort = "medium"')) 'Existing reasoning effort was overwritten.'
    Assert-True (-not $installed.Contains('old-secret')) 'Old MadAPI secret remained in config.toml.'
    Assert-True ($installed.Contains('experimental_bearer_token = "sk-windows-first-key"')) 'API key was not written into the active provider.'
    Assert-True (([regex]::Matches($installed, '(?m)^experimental_bearer_token = "sk-windows-first-key"\r?$')).Count -eq 1) 'API key was written more than once.'
    Assert-True (-not $installed.Contains('madapi.key')) 'Obsolete key-file authentication remained configured.'
    Assert-True (-not $installed.Contains('model_catalog_json')) 'A static model catalog remained configured.'
    Assert-True (-not $installed.Contains('[model_providers.custom.auth]')) 'Command-backed authentication remained configured.'
    Assert-True (-not $installed.Contains('supports_websockets')) 'Unverified WebSocket support was enabled.'
    Assert-True (-not $installed.Contains('disable_response_storage')) 'Optional response storage policy was added.'
    Assert-True ($installed.Contains('stream_idle_timeout_ms = 360000')) 'Stable 360 second stream timeout was not configured.'
    Assert-True ($installed.Contains('request_max_retries = 3')) 'Stable request retry count was not configured.'
    Assert-True ($installed.Contains('requires_openai_auth = true')) 'Remote model catalog authentication was not enabled.'
    Assert-True ((Get-Hash $sessionPath) -eq $sessionHash) 'Session data changed.'

    $backup = @(Get-ChildItem -LiteralPath $testHome -Filter 'config.toml.madapi-backup-*' -File)
    Assert-True ($backup.Count -eq 1) 'Exactly one backup was not created.'
    Assert-True ((Get-Hash $backup[0].FullName) -eq $configHash) 'Backup is not byte-identical to the original config.'
    Assert-True ((Get-Acl -LiteralPath $configPath).AreAccessRulesProtected) 'Config containing the API key still inherits broad ACLs.'

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
    Assert-True (([regex]::Matches($reinstalled, '(?m)^\[model_providers\.custom\]\r?$')).Count -eq 1) 'Duplicate provider section was created.'
    Assert-True (([regex]::Matches($reinstalled, '(?m)^experimental_bearer_token = "sk-windows-second-key"\r?$')).Count -eq 1) 'Repeat install did not replace the API key exactly once.'
    Assert-True (-not $reinstalled.Contains('sk-windows-first-key')) 'Repeat install retained the previous API key.'
    Assert-True (-not $reinstalled.Contains('supports_websockets')) 'Repeat install enabled unverified WebSocket support.'

    $legacyHome = Join-Path $sandbox 'legacy-script-defaults'
    New-Item -ItemType Directory -Path $legacyHome -Force | Out-Null
    $legacyConfigPath = Join-Path $legacyHome 'config.toml'
    Write-Utf8NoBom $legacyConfigPath @'
model_provider = "custom"
model = "gpt-5.6-sol"
disable_response_storage = true

[model_providers.custom]
name = "custom"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
requires_openai_auth = true
experimental_bearer_token = "sk-legacy-key"
stream_idle_timeout_ms = 360000
request_max_retries = 0
context_window_override = 1048576
'@
    Write-Utf8NoBom ($legacyConfigPath + '.madapi-backup-20260801-010101-001') @'
model_provider = "custom"
model = "deepseek-v4-flash"

[model_providers.custom]
name = "custom"
base_url = "https://example.invalid/v1"
wire_api = "responses"
'@
    $legacy = Invoke-TestInstaller $legacyHome 'sk-windows-legacy-migrated-key' $codexCli
    Assert-True ($legacy.ExitCode -eq 0) 'Legacy script-default migration failed.'
    $legacyMigrated = [IO.File]::ReadAllText($legacyConfigPath, [Text.Encoding]::UTF8)
    Assert-True (-not $legacyMigrated.Contains('disable_response_storage')) 'Legacy injected response-storage setting was not removed.'
    Assert-True ($legacyMigrated.Contains('request_max_retries = 3')) 'Legacy retry count was not migrated.'

    $preserveStorageHome = Join-Path $sandbox 'preserve-user-storage'
    New-Item -ItemType Directory -Path $preserveStorageHome -Force | Out-Null
    $preserveStorageConfigPath = Join-Path $preserveStorageHome 'config.toml'
    Write-Utf8NoBom $preserveStorageConfigPath @'
model_provider = "custom"
model = "gpt-5.6-sol"
disable_response_storage = true

[model_providers.custom]
name = "custom"
base_url = "https://example.invalid/v1"
wire_api = "responses"
'@
    $preservedStorage = Invoke-TestInstaller $preserveStorageHome 'sk-windows-preserve-storage-key' $codexCli
    Assert-True ($preservedStorage.ExitCode -eq 0) 'User storage preference preservation failed.'
    $preservedStorageConfig = [IO.File]::ReadAllText($preserveStorageConfigPath, [Text.Encoding]::UTF8)
    Assert-True ($preservedStorageConfig.Contains('disable_response_storage = true')) 'User-owned response-storage setting was removed.'

    $recoveryHome = Join-Path $sandbox 'recover-provider'
    New-Item -ItemType Directory -Path $recoveryHome -Force | Out-Null
    $recoveryConfigPath = Join-Path $recoveryHome 'config.toml'
    Write-Utf8NoBom $recoveryConfigPath @'
model_provider = "madapi"
model = "gpt-5.6-sol"

[model_providers.custom]
name = "custom"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"

[model_providers.madapi]
name = "MadAPI"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
'@
    Write-Utf8NoBom ($recoveryConfigPath + '.madapi-backup-20260801-010101-001') @'
model_provider = "custom"
model = "deepseek-v4-flash"

[model_providers.custom]
name = "custom"
base_url = "https://mad.myddns.me/codex/v1"
wire_api = "responses"
requires_openai_auth = true
'@
    $recovery = Invoke-TestInstaller $recoveryHome 'sk-windows-recovery-key' $codexCli
    Assert-True ($recovery.ExitCode -eq 0) ('Provider recovery failed: ' + ($recovery.Output -join ' | '))
    $recoveredConfig = [IO.File]::ReadAllText($recoveryConfigPath, [Text.Encoding]::UTF8)
    Assert-True ($recoveredConfig.Contains('model_provider = "custom"')) 'Original provider identity was not recovered from backup.'
    Assert-True (-not $recoveredConfig.Contains('[model_providers.madapi]')) 'Temporary MadAPI provider identity remained after recovery.'

    $freshHome = Join-Path $sandbox 'fresh'
    $fresh = Invoke-TestInstaller $freshHome 'sk-windows-fresh-key' $codexCli
    Assert-True ($fresh.ExitCode -eq 0) 'Fresh install failed.'
    $freshConfig = [IO.File]::ReadAllText((Join-Path $freshHome 'config.toml'), [Text.Encoding]::UTF8)
    Assert-True ($freshConfig.Contains('model_provider = "custom"')) 'Fresh install did not use the proven custom provider identity.'
    Assert-True ($freshConfig.Contains('name = "custom"')) 'Fresh install did not use the proven custom provider display name.'
    Assert-True ($freshConfig.Contains('experimental_bearer_token = "sk-windows-fresh-key"')) 'Fresh install did not configure the MadAPI key.'
    $oldHome = [string]$env:CODEX_HOME
    try {
        $env:CODEX_HOME = $freshHome
        & $codexCli features list *> $null
        Assert-True ($LASTEXITCODE -eq 0) 'Actual Codex rejected the clean generated configuration.'
    } finally {
        if ([string]::IsNullOrEmpty($oldHome)) {
            Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
        } else {
            $env:CODEX_HOME = $oldHome
        }
    }

    $officialHome = Join-Path $sandbox 'official-provider'
    New-Item -ItemType Directory -Path $officialHome -Force | Out-Null
    $officialConfigPath = Join-Path $officialHome 'config.toml'
    Write-Utf8NoBom $officialConfigPath @'
model_provider = "openai"
model = "gpt-5.6-sol"

[features]
memories = true
'@
    $official = Invoke-TestInstaller $officialHome 'sk-windows-official-key' $codexCli
    Assert-True ($official.ExitCode -eq 0) ('Reserved provider migration failed: ' + ($official.Output -join ' | '))
    $officialConfig = [IO.File]::ReadAllText($officialConfigPath, [Text.Encoding]::UTF8)
    Assert-True ($officialConfig.Contains('model_provider = "custom"')) 'Reserved OpenAI provider was not moved to the proven custom provider.'
    Assert-True ($officialConfig.Contains('[model_providers.custom]')) 'Custom MadAPI provider was not created for the reserved OpenAI provider.'
    Assert-True (-not $officialConfig.Contains('[model_providers.openai]')) 'Reserved OpenAI provider was illegally overridden.'
    Assert-True ($officialConfig.Contains('[features]')) 'Existing official-provider configuration was not preserved.'

    $badHome = Join-Path $sandbox 'malformed'
    New-Item -ItemType Directory -Path $badHome -Force | Out-Null
    $badConfig = Join-Path $badHome 'config.toml'
    Write-Utf8NoBom $badConfig "broken = [`n"
    $badHash = Get-Hash $badConfig
    $bad = Invoke-TestInstaller $badHome 'sk-windows-bad-key' $codexCli
    Assert-True ($bad.ExitCode -ne 0) 'Malformed existing config was accepted.'
    Assert-True ((Get-Hash $badConfig) -eq $badHash) 'Malformed existing config was changed.'

    $rollbackHome = Join-Path $sandbox 'rollback'
    New-Item -ItemType Directory -Path $rollbackHome -Force | Out-Null
    $rollbackConfig = Join-Path $rollbackHome 'config.toml'
    $rollbackKey = Join-Path $rollbackHome 'madapi.key'
    $rollbackCatalog = Join-Path $rollbackHome 'madapi-models.json'
    Write-Utf8NoBom $rollbackConfig "model = \"old-model\"`n"
    Write-Utf8NoBom $rollbackKey 'sk-old-key'
    Write-Utf8NoBom $rollbackCatalog '{"models":[{"slug":"old-model"}]}'
    $rollbackHash = Get-Hash $rollbackConfig
    $rollbackCatalogHash = Get-Hash $rollbackCatalog
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
    Assert-True (([IO.File]::ReadAllText($rollbackKey)) -eq 'sk-old-key') 'Unrelated legacy key file was changed.'
    Assert-True ((Get-Hash $rollbackCatalog) -eq $rollbackCatalogHash) 'Unrelated legacy model catalog was changed.'

    Write-Host 'Windows PowerShell 5.1 Codex installer acceptance passed.'
} finally {
    if (Test-Path -LiteralPath $sandbox) {
        [IO.Directory]::Delete($sandbox, $true)
    }
}
