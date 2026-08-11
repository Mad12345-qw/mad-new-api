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

if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_REFRESH_RESPONSE_FILE)) {
    $availablePayload = Get-Content -LiteralPath ([string]$env:MADAPI_REFRESH_RESPONSE_FILE) -Raw -Encoding UTF8 | ConvertFrom-Json
} else {
    $availablePayload = Invoke-RestMethod -UseBasicParsing -Uri ($baseUrl + '/codex/v1/models') -Headers @{ Authorization = 'Bearer ' + $apiKey } -Method Get -TimeoutSec 30
}

if (-not [string]::IsNullOrWhiteSpace([string]$env:MADAPI_CODEX_TEMPLATE_FILE)) {
    $templatePayload = Get-Content -LiteralPath ([string]$env:MADAPI_CODEX_TEMPLATE_FILE) -Raw -Encoding UTF8 | ConvertFrom-Json
} else {
    $templatePayload = Invoke-RestMethod -UseBasicParsing -Uri 'https://models.router-for.me/codex_client_models.json' -Method Get -TimeoutSec 30
}

$templates = @($templatePayload.models)
if ($templates.Count -lt 1) { throw 'The official CPA Codex model catalog is empty.' }
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

$result = New-Object 'System.Collections.Generic.List[object]'
$seen = @{}
$priority = 1
foreach ($id in $availableIds) {
    $lower = $id.ToLowerInvariant()
    if ($seen.ContainsKey($lower)) { continue }
    $seen[$lower] = $true
    if ($lower -match '(?:^|[-_.])(image|video|seedance|sora|veo|kling|hailuo)(?:$|[-_.])') { continue }

    $source = $templateBySlug[$lower]
    if ($null -eq $source -and $lower.EndsWith('-pro')) {
        $source = $templateBySlug[$lower.Substring(0, $lower.Length - 4)]
    }
    if ($null -eq $source) { $source = $defaultTemplate }
    $entry = (($source | ConvertTo-Json -Depth 100 -Compress) | ConvertFrom-Json)
    $entry.slug = $id
    $entry.display_name = $id
    $entry.priority = $priority
    $entry.visibility = 'list'
    $entry.supported_in_api = $true
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
Write-Output ('MadAPI Codex model catalog refreshed: ' + $result.Count)
