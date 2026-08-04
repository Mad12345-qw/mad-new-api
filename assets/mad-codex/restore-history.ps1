param([string]$CodexHome)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Set-JsonProperty([object]$Object, [string]$Name, [object]$Value) {
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value }
    else { $property.Value = $Value }
}

function Get-ProjectContainers([object]$Value) {
    if ($null -eq $Value) { return }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string] -and $Value -isnot [pscustomobject]) {
        foreach ($child in $Value) { Get-ProjectContainers $child }
        return
    }
    if ($Value -isnot [pscustomobject]) { return }
    if ($null -ne $Value.PSObject.Properties['local-projects']) { Write-Output $Value }
    foreach ($property in $Value.PSObject.Properties) { Get-ProjectContainers $property.Value }
}

if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $userHome = [Environment]::GetFolderPath('UserProfile')
    $CodexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
}

$sessionsPath = Join-Path $CodexHome 'sessions'
$indexPath = Join-Path $CodexHome 'session_index.jsonl'
$statePath = Join-Path $CodexHome '.codex-global-state.json'
if (-not (Test-Path -LiteralPath $sessionsPath)) { Write-Output 'MadAPI local history recovery: no existing sessions.'; exit 0 }

$utf8 = New-Object System.Text.UTF8Encoding($false)
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fff'
$backupPath = Join-Path $CodexHome ('madapi-history-backup-' + $stamp)
New-Item -ItemType Directory -Path $backupPath -Force | Out-Null
if (Test-Path -LiteralPath $indexPath) { [IO.File]::Copy($indexPath, (Join-Path $backupPath 'session_index.jsonl.before'), $false) }
if (Test-Path -LiteralPath $statePath) { [IO.File]::Copy($statePath, (Join-Path $backupPath '.codex-global-state.json.before'), $false) }

$existingTitles = @{}
if (Test-Path -LiteralPath $indexPath) {
    foreach ($line in [IO.File]::ReadAllLines($indexPath, [Text.Encoding]::UTF8)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try {
            $item = $line | ConvertFrom-Json
            if ($null -ne $item.id -and -not [string]::IsNullOrWhiteSpace([string]$item.thread_name)) { $existingTitles[[string]$item.id] = [string]$item.thread_name }
        } catch {}
    }
}

$records = New-Object 'System.Collections.Generic.List[object]'
foreach ($file in @(Get-ChildItem -LiteralPath $sessionsPath -Recurse -Filter 'rollout-*.jsonl' -File -ErrorAction SilentlyContinue)) {
    $firstLine = [IO.File]::ReadLines($file.FullName, [Text.Encoding]::UTF8) | Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace($firstLine)) { continue }
    try { $meta = $firstLine | ConvertFrom-Json } catch { continue }
    if ($meta.type -ne 'session_meta' -or $null -eq $meta.payload -or [string]::IsNullOrWhiteSpace([string]$meta.payload.id)) { continue }
    $timestamp = [datetime]::MinValue
    [void][datetime]::TryParse([string]$meta.payload.timestamp, [ref]$timestamp)
    $records.Add([pscustomobject]@{ Id = [string]$meta.payload.id; Cwd = [string]$meta.payload.cwd; UpdatedAt = $timestamp.ToUniversalTime().ToString('o'); SortAt = $timestamp; File = $file.FullName })
}

$records = @($records | Sort-Object SortAt, Id -Unique)
$indexLines = New-Object 'System.Collections.Generic.List[string]'
foreach ($record in $records) {
    $title = if ($existingTitles.ContainsKey($record.Id)) { $existingTitles[$record.Id] } elseif (-not [string]::IsNullOrWhiteSpace($record.Cwd)) { Split-Path -Leaf $record.Cwd } else { 'Recovered conversation' }
    $indexLines.Add(([ordered]@{ id = $record.Id; thread_name = $title; updated_at = $record.UpdatedAt } | ConvertTo-Json -Compress))
}

$indexTemp = $indexPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
[IO.File]::WriteAllText($indexTemp, (($indexLines -join [Environment]::NewLine) + $(if ($indexLines.Count -gt 0) { [Environment]::NewLine } else { '' })), $utf8)
Move-Item -LiteralPath $indexTemp -Destination $indexPath -Force

$assigned = 0
$assignedIds = New-Object 'System.Collections.Generic.HashSet[string]'
if (Test-Path -LiteralPath $statePath) {
    try {
        $state = [IO.File]::ReadAllText($statePath, [Text.Encoding]::UTF8) | ConvertFrom-Json
        foreach ($container in @(Get-ProjectContainers $state)) {
            $projects = $container.'local-projects'
            foreach ($projectProperty in $projects.PSObject.Properties) {
                $projectId = $projectProperty.Name
                $project = $projectProperty.Value
                foreach ($rootPath in @($project.rootPaths)) {
                    if ([string]::IsNullOrWhiteSpace([string]$rootPath) -or -not (Test-Path -LiteralPath $rootPath -PathType Container)) { continue }
                    $normalizedRoot = ([IO.Path]::GetFullPath([string]$rootPath)).TrimEnd('\\')
                    foreach ($record in $records) {
                        if ([string]::IsNullOrWhiteSpace($record.Cwd)) { continue }
                        $normalizedCwd = ([IO.Path]::GetFullPath($record.Cwd)).TrimEnd('\\')
                        if (-not ($normalizedCwd.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase) -or $normalizedCwd.StartsWith($normalizedRoot + '\\', [StringComparison]::OrdinalIgnoreCase))) { continue }
                        if ($null -eq $container.PSObject.Properties['thread-workspace-root-hints']) { Set-JsonProperty $container 'thread-workspace-root-hints' ([pscustomobject]@{}) }
                        if ($null -eq $container.PSObject.Properties['thread-project-assignments']) { Set-JsonProperty $container 'thread-project-assignments' ([pscustomobject]@{}) }
                        $container.'thread-workspace-root-hints' | Add-Member -NotePropertyName $record.Id -NotePropertyValue $normalizedRoot -Force
                        $assignment = [ordered]@{ projectKind = 'local'; projectId = $projectId; cwd = $record.Cwd; pendingCoreUpdate = $false }
                        $container.'thread-project-assignments' | Add-Member -NotePropertyName $record.Id -NotePropertyValue $assignment -Force
                        $assigned++
                        [void]$assignedIds.Add($record.Id)
                    }
                }
            }
            if ($null -ne $container.PSObject.Properties['projectless-thread-ids']) {
                $removed = @($container.'projectless-thread-ids' | Where-Object { -not $assignedIds.Contains([string]$_) })
                Set-JsonProperty $container 'projectless-thread-ids' $removed
            }
        }
        $stateTemp = $statePath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
        [IO.File]::WriteAllText($stateTemp, ($state | ConvertTo-Json -Depth 100 -Compress), $utf8)
        Move-Item -LiteralPath $stateTemp -Destination $statePath -Force
    } catch {
        Write-Warning ('MadAPI local project recovery skipped: ' + $_.Exception.Message)
    }
}

Write-Output ('MadAPI local history recovered: ' + $records.Count + ' conversations, ' + $assigned + ' project mappings.')
Write-Output ('History backup created: ' + $backupPath)
