param([string]$CodexHome)

$ErrorActionPreference = 'Stop'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$userHome = [Environment]::GetFolderPath('UserProfile')
if ([string]::IsNullOrWhiteSpace($CodexHome)) {
    $CodexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) { Join-Path $userHome '.codex' } else { [string]$env:CODEX_HOME }
}

$apiKey = [string]$env:MADAPI_API_KEY
if ([string]::IsNullOrWhiteSpace($apiKey)) { $apiKey = [string]$env:MADAPI_KEY }
if ([string]::IsNullOrWhiteSpace($apiKey) -or $apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') {
    throw 'MADAPI_API_KEY is missing or invalid.'
}

$baseUrl = ([string]$env:MADAPI_BASE_URL).Trim().TrimEnd('/')
if ([string]::IsNullOrWhiteSpace($baseUrl)) { $baseUrl = 'https://mad.myddns.me' }
$catalogPath = Join-Path $CodexHome 'madapi-cockpit-model-catalog.json'
$modelsCachePath = Join-Path $CodexHome 'models_cache.json'
$authPath = Join-Path $CodexHome 'auth.json'

$authKind = ([string]$env:MADAPI_CODEX_AUTH_KIND).Trim().ToLowerInvariant()
if ([string]::IsNullOrWhiteSpace($authKind)) {
    $authKind = 'oauth'
    if (Test-Path -LiteralPath $authPath) {
        try { $auth = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8 | ConvertFrom-Json }
        catch { throw 'Codex Desktop authentication state is unreadable.' }
        if ([string]$auth.auth_mode -eq 'apikey' -or -not [string]::IsNullOrWhiteSpace([string]$auth.OPENAI_API_KEY)) {
            $authKind = 'apikey'
        }
    }
}
if (@('oauth', 'apikey') -notcontains $authKind) { throw 'MADAPI_CODEX_AUTH_KIND is invalid.' }

if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_REFRESH_RESPONSE_FILE)) {
    $availablePayload = Get-Content -LiteralPath ([string]$env:MADAPI_REFRESH_RESPONSE_FILE) -Raw -Encoding UTF8 | ConvertFrom-Json
} else {
    $availablePayload = Invoke-RestMethod -UseBasicParsing -Uri ($baseUrl + '/v1/models') -Headers @{ Authorization = 'Bearer ' + $apiKey } -Method Get -TimeoutSec 30
}

if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_CODEX_TEMPLATE_FILE)) {
    $templatePayload = Get-Content -LiteralPath ([string]$env:MADAPI_CODEX_TEMPLATE_FILE) -Raw -Encoding UTF8 | ConvertFrom-Json
} else {
    $templatePayload = Invoke-RestMethod -UseBasicParsing -Uri ($baseUrl + '/mad-codex/codex-model-templates.json') -Method Get -TimeoutSec 30
}

$templates = @($templatePayload.models)
if ($templates.Count -lt 1) { throw 'The Codex model catalog is empty.' }
$templateBySlug = @{}
foreach ($template in $templates) {
    $slug = [string]$template.slug
    if (-not [string]::IsNullOrWhiteSpace($slug)) { $templateBySlug[$slug.ToLowerInvariant()] = $template }
}
$defaultTemplate = $templateBySlug['gpt-5.5']
if ($null -eq $defaultTemplate) { $defaultTemplate = $templates[0] }

$availableIds = New-Object 'System.Collections.Generic.List[string]'
if ($null -ne $availablePayload.PSObject.Properties['data']) {
    foreach ($model in @($availablePayload.data)) {
        $id = [string]$model.id
        if (-not [string]::IsNullOrWhiteSpace($id)) { $availableIds.Add($id.Trim()) }
    }
} elseif ($null -ne $availablePayload.PSObject.Properties['models']) {
    foreach ($model in @($availablePayload.models)) {
        $id = if ($null -ne $model.PSObject.Properties['id']) { [string]$model.id } else { [string]$model.slug }
        if (-not [string]::IsNullOrWhiteSpace($id)) { $availableIds.Add($id.Trim()) }
    }
}

$apiModelIds = @(
    'claude-fable-5',
    'claude-opus-5',
    'gpt-5.6-sol',
    'gpt-5.6-terra',
    'gpt-5.6-luna',
    'grok-4.6',
    'gpt-5.6-sol-pro',
    'gpt-5.6-terra-pro'
)
$apiModelSlots = [ordered]@{
    'claude-fable-5' = [ordered]@{ Shell = 'gpt-5.5'; Profile = 'gpt-5.5' }
    'claude-opus-5' = [ordered]@{ Shell = 'gpt-5.4'; Profile = 'gpt-5.4' }
    'gpt-5.6-sol' = [ordered]@{ Shell = 'gpt-5.6-sol'; Profile = 'gpt-5.6-sol' }
    'gpt-5.6-terra' = [ordered]@{ Shell = 'gpt-5.6-terra'; Profile = 'gpt-5.6-terra' }
    'gpt-5.6-luna' = [ordered]@{ Shell = 'gpt-5.6-luna'; Profile = 'gpt-5.6-luna' }
    'grok-4.6' = [ordered]@{ Shell = 'gpt-5.4-mini'; Profile = 'gpt-5.4-mini' }
    'gpt-5.6-sol-pro' = [ordered]@{ Shell = 'gpt-5.3-codex'; Profile = 'gpt-5.6-sol' }
    'gpt-5.6-terra-pro' = [ordered]@{ Shell = 'gpt-5.2'; Profile = 'gpt-5.6-terra' }
}
if ($authKind -eq 'apikey') {
    $availableLookup = @{}
    foreach ($id in $availableIds) { $availableLookup[$id.ToLowerInvariant()] = $id }
    $missing = @($apiModelIds | Where-Object { -not $availableLookup.ContainsKey($_) })
    if ($missing.Count -gt 0) { throw ('MadAPI API catalog is missing required models: ' + ($missing -join ', ')) }
    $availableIds = New-Object 'System.Collections.Generic.List[string]'
    foreach ($id in $apiModelIds) { $availableIds.Add($availableLookup[$id]) }
}

$result = New-Object 'System.Collections.Generic.List[object]'
$seen = @{}
$priority = 1
foreach ($id in $availableIds) {
    $lower = $id.ToLowerInvariant()
    if ($seen.ContainsKey($lower)) { continue }
    $seen[$lower] = $true
    if ($lower -match '(?:^|[-_.])(image|video|seedance|sora|veo|kling|hailuo)(?:$|[-_.])') { continue }

    $catalogSlug = $id
    $sourceSlug = if ($lower -eq 'grok-4.6') { 'gpt-5.4-mini' } else { $lower }
    if ($authKind -eq 'apikey') {
        $slot = $apiModelSlots[$lower]
        if ($null -eq $slot) { throw ('MadAPI API model slot is missing: ' + $id) }
        $catalogSlug = [string]$slot.Shell
        $sourceSlug = [string]$slot.Profile
    }
    $source = $templateBySlug[$sourceSlug.ToLowerInvariant()]
    if ($null -eq $source) { $source = $defaultTemplate }
    $entry = (($source | ConvertTo-Json -Depth 100 -Compress) | ConvertFrom-Json)
    $entry.slug = $catalogSlug
    $entry.display_name = $id
    $entry.description = 'Available through MadAPI: ' + $id
    $entry.priority = $priority
    $entry.visibility = 'list'
    $entry.supported_in_api = $true
    $entry | Add-Member -NotePropertyName prefer_websockets -NotePropertyValue $false -Force
    $result.Add($entry)
    $priority++
}

if ($result.Count -lt 1) { throw 'MadAPI returned no Codex conversation models.' }
$payload = [ordered]@{ models = $result.ToArray() }
New-Item -ItemType Directory -Path $CodexHome -Force | Out-Null
$tempPath = Join-Path $CodexHome ('madapi-cockpit-model-catalog.' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    [IO.File]::WriteAllText($tempPath, ($payload | ConvertTo-Json -Depth 100), $utf8NoBom)
    Move-Item -LiteralPath $tempPath -Destination $catalogPath -Force
} finally {
    if (Test-Path -LiteralPath $tempPath) { Remove-Item -LiteralPath $tempPath -Force }
}
if (Test-Path -LiteralPath $modelsCachePath) { Remove-Item -LiteralPath $modelsCachePath -Force }
Write-Output ('MadAPI Codex model catalog refreshed: ' + $authKind + ', ' + $result.Count)
