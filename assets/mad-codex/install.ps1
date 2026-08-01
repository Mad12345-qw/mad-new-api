$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function ConvertTo-TomlBasicString([string]$Value) {
    $escaped = $Value.Replace('\', '\\').Replace('"', '\"')
    $escaped = $escaped.Replace("`r", '\r').Replace("`n", '\n').Replace("`t", '\t')
    return '"' + $escaped + '"'
}

function Get-CodexCandidates {
    $candidates = New-Object 'System.Collections.Generic.List[string]'

    if (-not [string]::IsNullOrWhiteSpace([string]$env:CODEX_CLI_PATH)) {
        $candidates.Add([string]$env:CODEX_CLI_PATH)
    }

    foreach ($name in @('codex.exe', 'codex.cmd', 'codex')) {
        $command = Get-Command $name -ErrorAction SilentlyContinue
        if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace([string]$command.Source)) {
            $candidates.Add([string]$command.Source)
        }
    }

    $localBin = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'OpenAI\Codex\bin'
    if (Test-Path -LiteralPath $localBin) {
        $direct = Join-Path $localBin 'codex.exe'
        if (Test-Path -LiteralPath $direct) {
            $candidates.Add($direct)
        }
        Get-ChildItem -LiteralPath $localBin -Directory -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            ForEach-Object {
                $candidate = Join-Path $_.FullName 'codex.exe'
                if (Test-Path -LiteralPath $candidate) {
                    $candidates.Add($candidate)
                }
            }
    }

    $seen = @{}
    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) {
            continue
        }
        $key = $candidate.ToLowerInvariant()
        if (-not $seen.ContainsKey($key)) {
            $seen[$key] = $true
            $candidate
        }
    }
}

function Test-CodexExecutable([string]$Candidate) {
    $oldPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        & $Candidate --version *> $null
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    } finally {
        $ErrorActionPreference = $oldPreference
    }
}

function Test-CodexConfig([string]$Candidate, [string]$CodexHome) {
    $oldHome = [string]$env:CODEX_HOME
    $oldPreference = $ErrorActionPreference
    try {
        $env:CODEX_HOME = $CodexHome
        $ErrorActionPreference = 'Continue'
        & $Candidate features list *> $null
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    } finally {
        $ErrorActionPreference = $oldPreference
        if ([string]::IsNullOrEmpty($oldHome)) {
            Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
        } else {
            $env:CODEX_HOME = $oldHome
        }
    }
}

function Protect-SecretFile([string]$Path) {
    $acl = New-Object System.Security.AccessControl.FileSecurity
    $acl.SetAccessRuleProtection($true, $false)
    $rights = [System.Security.AccessControl.FileSystemRights]::FullControl
    $inheritance = [System.Security.AccessControl.InheritanceFlags]::None
    $propagation = [System.Security.AccessControl.PropagationFlags]::None
    $allow = [System.Security.AccessControl.AccessControlType]::Allow

    $identities = @(
        [System.Security.Principal.WindowsIdentity]::GetCurrent().User,
        (New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18')),
        (New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-544'))
    )
    foreach ($identity in $identities) {
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $identity,
            $rights,
            $inheritance,
            $propagation,
            $allow
        )
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

$apiKey = [string]$env:MADAPI_KEY
if ([string]::IsNullOrWhiteSpace($apiKey)) {
    throw 'MADAPI_KEY is missing.'
}
if ($apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_KEY contains unsupported characters.'
}

$userHome = [Environment]::GetFolderPath('UserProfile')
$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) {
    Join-Path $userHome '.codex'
} else {
    [string]$env:CODEX_HOME
}
$configPath = Join-Path $codexHome 'config.toml'
$keyPath = Join-Path $codexHome 'madapi.key'
$transactionId = [guid]::NewGuid().ToString('N')
$tempConfigPath = Join-Path $codexHome ("config.toml.madapi.$transactionId.tmp")
$tempKeyPath = Join-Path $codexHome ("madapi.key.$transactionId.tmp")
$rollbackKeyPath = Join-Path $codexHome ("madapi.key.$transactionId.rollback")
$backupPath = $null
$hadConfig = Test-Path -LiteralPath $configPath
$hadKey = Test-Path -LiteralPath $keyPath
$configInstalled = $false
$keyInstalled = $false

New-Item -ItemType Directory -Path $codexHome -Force | Out-Null

$codexCli = $null
foreach ($candidate in @(Get-CodexCandidates)) {
    if (Test-CodexExecutable $candidate) {
        $codexCli = $candidate
        break
    }
}
if ($null -eq $codexCli) {
    throw 'Codex CLI was not found. Install or update Codex, then try again.'
}
if ($hadConfig -and -not (Test-CodexConfig $codexCli $codexHome)) {
    throw 'The existing Codex configuration could not be parsed. No files were changed.'
}

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$utf8Strict = New-Object System.Text.UTF8Encoding($false, $true)
$sourceLines = @()
if ($hadConfig) {
    $sourceLines = @([IO.File]::ReadAllLines($configPath, $utf8Strict))
}

$keptLines = New-Object 'System.Collections.Generic.List[string]'
$currentSection = ''
$skipMadApiSection = $false
$preservedDefaults = @{
    model_reasoning_effort = $false
    model_auto_compact_token_limit = $false
    disable_response_storage = $false
}

foreach ($line in $sourceLines) {
    if ($line -match '^\s*\[([^]]+)\]\s*(?:#.*)?$') {
        $currentSection = $Matches[1].Trim()
        $skipMadApiSection =
            $currentSection -eq 'model_providers.madapi' -or
            $currentSection.StartsWith('model_providers.madapi.')
        if ($skipMadApiSection) {
            continue
        }
    }

    if ($skipMadApiSection) {
        continue
    }

    if ($currentSection -eq '') {
        if ($line -match '^\s*(model_provider|model)\s*=') {
            continue
        }
        foreach ($name in @($preservedDefaults.Keys)) {
            if ($line -match ('^\s*' + [regex]::Escape($name) + '\s*=')) {
                $preservedDefaults[$name] = $true
            }
        }
    }

    $keptLines.Add($line)
}

$headerLines = New-Object 'System.Collections.Generic.List[string]'
$headerLines.Add('model_provider = "madapi"')
$headerLines.Add('model = "gpt-5.6-sol"')
if (-not $preservedDefaults.model_reasoning_effort) {
    $headerLines.Add('model_reasoning_effort = "high"')
}
if (-not $preservedDefaults.model_auto_compact_token_limit) {
    $headerLines.Add('model_auto_compact_token_limit = 500000')
}
if (-not $preservedDefaults.disable_response_storage) {
    $headerLines.Add('disable_response_storage = true')
}

$authCommand = '$h=if([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)){Join-Path ([Environment]::GetFolderPath(''UserProfile'')) ''.codex''}else{[string]$env:CODEX_HOME};[Console]::Out.Write([IO.File]::ReadAllText((Join-Path $h ''madapi.key'')).Trim())'
$configLines = New-Object 'System.Collections.Generic.List[string]'
foreach ($line in $headerLines) {
    $configLines.Add($line)
}
$configLines.Add('')
foreach ($line in $keptLines) {
    $configLines.Add($line)
}
$configLines.Add('')
$configLines.Add('[model_providers.madapi]')
$configLines.Add('name = "MadAPI"')
$configLines.Add('base_url = "https://mad.myddns.me/codex/v1"')
$configLines.Add('wire_api = "responses"')
$configLines.Add('supports_websockets = true')
$configLines.Add('stream_idle_timeout_ms = 360000')
$configLines.Add('request_max_retries = 0')
$configLines.Add('context_window_override = 1048576')
$configLines.Add('')
$configLines.Add('[model_providers.madapi.auth]')
$configLines.Add('command = "powershell.exe"')
$configLines.Add('args = ["-NoProfile", "-Command", ' + (ConvertTo-TomlBasicString $authCommand) + ']')
$configLines.Add('timeout_ms = 5000')
$configLines.Add('refresh_interval_ms = 300000')

try {
    [IO.File]::WriteAllText(
        $tempConfigPath,
        (($configLines -join [Environment]::NewLine).Trim() + [Environment]::NewLine),
        $utf8NoBom
    )
    [IO.File]::WriteAllText($tempKeyPath, $apiKey, $utf8NoBom)
    Protect-SecretFile $tempKeyPath

    if ($hadConfig) {
        $backupPath = '{0}.madapi-backup-{1}' -f $configPath, (Get-Date -Format 'yyyyMMdd-HHmmss-fff')
        [IO.File]::Copy($configPath, $backupPath, $false)
        Protect-SecretFile $backupPath
    }
    if ($hadKey) {
        Move-Item -LiteralPath $keyPath -Destination $rollbackKeyPath
    }

    Move-Item -LiteralPath $tempKeyPath -Destination $keyPath
    $keyInstalled = $true
    Move-Item -LiteralPath $tempConfigPath -Destination $configPath -Force
    $configInstalled = $true

    if (-not (Test-CodexConfig $codexCli $codexHome)) {
        throw 'The generated MadAPI configuration could not be parsed by Codex.'
    }

    if (Test-Path -LiteralPath $rollbackKeyPath) {
        Remove-Item -LiteralPath $rollbackKeyPath -Force
    }
} catch {
    $failure = $_
    if ($configInstalled) {
        if ($hadConfig -and $null -ne $backupPath -and (Test-Path -LiteralPath $backupPath)) {
            [IO.File]::Copy($backupPath, $configPath, $true)
        } elseif (-not $hadConfig -and (Test-Path -LiteralPath $configPath)) {
            Remove-Item -LiteralPath $configPath -Force
        }
    }
    if ($keyInstalled -and (Test-Path -LiteralPath $keyPath)) {
        Remove-Item -LiteralPath $keyPath -Force
    }
    if ($hadKey -and (Test-Path -LiteralPath $rollbackKeyPath)) {
        Move-Item -LiteralPath $rollbackKeyPath -Destination $keyPath
    }
    throw $failure
} finally {
    foreach ($path in @($tempConfigPath, $tempKeyPath, $rollbackKeyPath)) {
        if (Test-Path -LiteralPath $path) {
            Remove-Item -LiteralPath $path -Force
        }
    }
}

Write-Host "MadAPI Codex configuration installed: $configPath"
if ($null -ne $backupPath) {
    Write-Host "Backup created: $backupPath"
}
Write-Host 'Restart Codex to load the model catalog.'
