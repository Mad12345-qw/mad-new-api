param(
    [string]$CodexHome,
    [string]$ProviderId,
    [string]$BackupPath
)

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

function Get-CurrentProviderId([string]$ConfigPath) {
    if (-not (Test-Path -LiteralPath $ConfigPath)) { return $null }
    $config = [IO.File]::ReadAllText($ConfigPath, [Text.Encoding]::UTF8)
    $match = [regex]::Match($config, '(?m)^\s*model_provider\s*=\s*"([A-Za-z0-9_-]+)"\s*$')
    if (-not $match.Success) { return $null }
    return $match.Groups[1].Value
}

function Backup-HistoryDatabase([string]$DatabasePath, [string]$BackupPath) {
    foreach ($suffix in @('', '-wal', '-shm')) {
        $source = $DatabasePath + $suffix
        if (Test-Path -LiteralPath $source) {
            [IO.File]::Copy($source, (Join-Path $BackupPath ([IO.Path]::GetFileName($source))), $false)
        }
    }
}

function Migrate-HistoryProvider([string]$DatabasePath, [string]$ProviderId) {
    if (-not (Test-Path -LiteralPath $DatabasePath) -or $ProviderId -eq 'openai') { return 0 }
    if ($ProviderId -notmatch '^[A-Za-z0-9_-]+$') { throw 'Current Codex provider identifier is invalid.' }
    if (-not ('MadApiHistorySqlite' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class MadApiHistorySqlite
{
    [DllImport("winsqlite3.dll", CallingConvention = CallingConvention.Cdecl, CharSet = CharSet.Unicode)]
    private static extern int sqlite3_open16(string filename, out IntPtr db);

    [DllImport("winsqlite3.dll", CallingConvention = CallingConvention.Cdecl)]
    private static extern int sqlite3_close_v2(IntPtr db);

    [DllImport("winsqlite3.dll", CallingConvention = CallingConvention.Cdecl, CharSet = CharSet.Ansi)]
    private static extern int sqlite3_exec(IntPtr db, string sql, IntPtr callback, IntPtr context, out IntPtr error);

    [DllImport("winsqlite3.dll", CallingConvention = CallingConvention.Cdecl)]
    private static extern int sqlite3_changes(IntPtr db);

    [DllImport("winsqlite3.dll", CallingConvention = CallingConvention.Cdecl)]
    private static extern void sqlite3_free(IntPtr pointer);

    public static int MigrateOpenAIThreads(string path, string provider)
    {
        IntPtr db = IntPtr.Zero;
        IntPtr error = IntPtr.Zero;
        try
        {
            if (sqlite3_open16(path, out db) != 0)
                throw new InvalidOperationException("Unable to open Codex history database.");

            string sql =
                "UPDATE threads SET model_provider = '" + provider + "'" +
                " WHERE archived = 0 AND COALESCE(model_provider, '') IN ('', 'openai');";
            if (sqlite3_exec(db, sql, IntPtr.Zero, IntPtr.Zero, out error) != 0)
                throw new InvalidOperationException("Unable to migrate Codex history provider.");
            return sqlite3_changes(db);
        }
        finally
        {
            if (error != IntPtr.Zero) sqlite3_free(error);
            if (db != IntPtr.Zero) sqlite3_close_v2(db);
        }
    }
}
'@
    }
    return [MadApiHistorySqlite]::MigrateOpenAIThreads($DatabasePath, $ProviderId)
}

if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $userHome = [Environment]::GetFolderPath('UserProfile')
    $CodexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
}

$sessionsPath = Join-Path $CodexHome 'sessions'
$indexPath = Join-Path $CodexHome 'session_index.jsonl'
$statePath = Join-Path $CodexHome '.codex-global-state.json'
$configPath = Join-Path $CodexHome 'config.toml'
$databasePath = Join-Path $CodexHome 'state_5.sqlite'
if (-not (Test-Path -LiteralPath $sessionsPath)) { Write-Output 'MadAPI local history recovery: no existing sessions.'; exit 0 }
$providerId = if ([string]::IsNullOrWhiteSpace($ProviderId)) { Get-CurrentProviderId $configPath } else { $ProviderId.Trim() }
if ([string]::IsNullOrWhiteSpace($providerId)) { throw 'Current Codex provider is missing; history was not changed.' }

$utf8 = New-Object System.Text.UTF8Encoding($false)
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fff'
$backupPath = if ([string]::IsNullOrWhiteSpace($BackupPath)) { Join-Path $CodexHome ('madapi-history-backup-' + $stamp) } else { $BackupPath }
New-Item -ItemType Directory -Path $backupPath -ErrorAction Stop | Out-Null
if (Test-Path -LiteralPath $indexPath) { [IO.File]::Copy($indexPath, (Join-Path $backupPath 'session_index.jsonl.before'), $false) }
if (Test-Path -LiteralPath $statePath) { [IO.File]::Copy($statePath, (Join-Path $backupPath '.codex-global-state.json.before'), $false) }
Backup-HistoryDatabase $databasePath $backupPath
$migratedThreads = Migrate-HistoryProvider $databasePath $providerId

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
Write-Output ('MadAPI local history provider migrated: ' + $migratedThreads + ' conversations to ' + $providerId + '.')
Write-Output ('History backup created: ' + $backupPath)
