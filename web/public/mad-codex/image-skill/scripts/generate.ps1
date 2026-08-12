param(
    [Parameter(Mandatory = $true)]
    [string]$Prompt,

    [string]$Out = '',

    [ValidateSet('auto', 'low', 'medium', 'high')]
    [string]$Quality = 'auto',

    [string]$Size = 'auto',

    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

$codexHome = if ([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)) {
    Join-Path $env:USERPROFILE '.codex'
} else {
    [string]$env:CODEX_HOME
}
$keyPath = Join-Path $codexHome 'madapi.key'
$outputDirectory = Join-Path (Get-Location).Path 'outputs'
$baseUrl = ([string]$env:MADAPI_BASE_URL).Trim().TrimEnd('/')
if ([string]::IsNullOrWhiteSpace($baseUrl)) { $baseUrl = 'https://mad.myddns.me' }
$endpoint = $baseUrl + '/v1/images/generations'

if ([string]::IsNullOrWhiteSpace($Prompt)) { throw 'Prompt cannot be empty.' }
if (-not (Test-Path -LiteralPath $keyPath)) { throw 'MadAPI key file is missing.' }

$apiKey = (Get-Content -LiteralPath $keyPath -Raw -Encoding UTF8).Trim()
if ($apiKey -notmatch '^sk-[A-Za-z0-9._-]+$') { throw 'MadAPI key file is invalid.' }

if ($DryRun) {
    [pscustomobject]@{
        ok = $true
        dry_run = $true
        endpoint = $endpoint
        model = 'gpt-image-2'
        response_format = 'url'
        quality = $Quality
        size = $Size
    } | ConvertTo-Json -Compress
    exit 0
}

New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
$requestedOutput = $Out
if ([string]::IsNullOrWhiteSpace($requestedOutput)) {
    $requestedOutput = Join-Path $outputDirectory ('madapi-' + [guid]::NewGuid().ToString('N') + '.png')
} elseif (-not [IO.Path]::IsPathRooted($requestedOutput)) {
    $requestedOutput = Join-Path (Get-Location).Path $requestedOutput
}
$requestedOutput = [IO.Path]::GetFullPath($requestedOutput)
$requestedDirectory = Split-Path -Parent $requestedOutput
New-Item -ItemType Directory -Path $requestedDirectory -Force | Out-Null

$payload = [ordered]@{
    model = 'gpt-image-2'
    prompt = $Prompt
    n = 1
    quality = $Quality
    size = $Size
    response_format = 'url'
}
$json = $payload | ConvertTo-Json -Depth 8 -Compress
$curl = Join-Path $env:WINDIR 'System32\curl.exe'
if (-not (Test-Path -LiteralPath $curl)) { throw 'Windows curl.exe is unavailable.' }

$requestId = [guid]::NewGuid().ToString('N')
$requestPath = Join-Path $env:TEMP ('madapi-image-request-' + $requestId + '.json')
$responsePath = Join-Path $env:TEMP ('madapi-image-response-' + $requestId + '.json')
$headerPath = Join-Path $env:TEMP ('madapi-image-header-' + $requestId + '.txt')
try {
    [IO.File]::WriteAllText($requestPath, $json, (New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText(
        $headerPath,
        'Authorization: Bearer ' + $apiKey + [Environment]::NewLine +
        'Content-Type: application/json; charset=utf-8' + [Environment]::NewLine,
        [Text.Encoding]::ASCII
    )
    $statusOutput = @(& $curl `
        --silent `
        --show-error `
        --max-time 300 `
        --output $responsePath `
        --write-out '%{http_code}' `
        --header ('@' + $headerPath) `
        --data-binary ('@' + $requestPath) `
        $endpoint 2>&1)
    $curlExitCode = $LASTEXITCODE
    $statusText = ($statusOutput -join '').Trim()
    if ($curlExitCode -ne 0) {
        throw ('MadAPI image request failed: curl exit ' + $curlExitCode + ', ' + $statusText)
    }
    if (-not (Test-Path -LiteralPath $responsePath)) { throw 'MadAPI returned no response body.' }
    $responseText = [IO.File]::ReadAllText($responsePath, [Text.Encoding]::UTF8)
    try { $response = $responseText | ConvertFrom-Json }
    catch { throw ('MadAPI returned invalid JSON with HTTP status ' + $statusText + '.') }
    if ($statusText -notmatch '^2[0-9][0-9]$') {
        $message = [string]$response.error.message
        if ([string]::IsNullOrWhiteSpace($message)) { $message = $responseText }
        throw ('MadAPI image request returned HTTP ' + $statusText + ': ' + $message)
    }
} finally {
    Remove-Item -LiteralPath $requestPath, $responsePath, $headerPath -Force -ErrorAction SilentlyContinue
}

$items = @($response.data)
if ($items.Count -eq 0 -or $null -eq $items[0]) { throw 'MadAPI returned no image data.' }
$imageUrl = [string]$items[0].url
$base64Image = [string]$items[0].b64_json
$temporaryOutput = $requestedOutput + '.' + [guid]::NewGuid().ToString('N') + '.tmp'

try {
    if (-not [string]::IsNullOrWhiteSpace($imageUrl)) {
        $downloadOutput = @(& $curl `
            --fail `
            --location `
            --silent `
            --show-error `
            --max-time 300 `
            --output $temporaryOutput `
            $imageUrl 2>&1)
        if ($LASTEXITCODE -ne 0) {
            throw ('MadAPI image download failed: ' + ($downloadOutput -join ' '))
        }
    } elseif (-not [string]::IsNullOrWhiteSpace($base64Image)) {
        [IO.File]::WriteAllBytes($temporaryOutput, [Convert]::FromBase64String($base64Image))
    } else {
        throw 'MadAPI returned neither an image URL nor b64_json.'
    }

    $bytes = [IO.File]::ReadAllBytes($temporaryOutput)
    if ($bytes.Length -lt 100) { throw 'Downloaded image is empty or truncated.' }
    $extension = '.bin'
    if ($bytes.Length -ge 8 -and $bytes[0] -eq 0x89 -and $bytes[1] -eq 0x50 -and $bytes[2] -eq 0x4E -and $bytes[3] -eq 0x47) {
        $extension = '.png'
    } elseif ($bytes.Length -ge 3 -and $bytes[0] -eq 0xFF -and $bytes[1] -eq 0xD8 -and $bytes[2] -eq 0xFF) {
        $extension = '.jpg'
    } elseif ($bytes.Length -ge 12 -and [Text.Encoding]::ASCII.GetString($bytes, 0, 4) -eq 'RIFF' -and [Text.Encoding]::ASCII.GetString($bytes, 8, 4) -eq 'WEBP') {
        $extension = '.webp'
    } else {
        throw 'Downloaded payload is not a supported PNG, JPEG, or WebP image.'
    }

    $finalOutput = [IO.Path]::ChangeExtension($requestedOutput, $extension)
    if (Test-Path -LiteralPath $finalOutput) { throw 'Output file already exists.' }
    Move-Item -LiteralPath $temporaryOutput -Destination $finalOutput
    $previewPath = $finalOutput.Replace('\', '/')
    [pscustomobject]@{
        ok = $true
        model = 'gpt-image-2'
        path = $finalOutput
        source_url = $imageUrl
        preview_markdown = '![Generated image](' + $previewPath + ')'
        download_markdown = if ([string]::IsNullOrWhiteSpace($imageUrl)) { '' } else { '[Open or download original](' + $imageUrl + ')' }
        bytes = $bytes.Length
    } | ConvertTo-Json -Compress
} finally {
    if (Test-Path -LiteralPath $temporaryOutput) { Remove-Item -LiteralPath $temporaryOutput -Force }
}
