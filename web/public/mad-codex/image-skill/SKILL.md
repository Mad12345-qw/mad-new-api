---
name: "madapi-imagegen"
description: "Generate raster images through MadAPI gpt-image-2 on Codex Desktop in OAuth or API Key mode. Use for every request to generate, create, draw, render, or design a bitmap image."
---

# MadAPI Image Generation

Keep the current Codex login mode unchanged.

## Required workflow

1. Do not fabricate an attachment or output path.
2. Build a complete image prompt from the user's request.
3. Run the installed platform generator exactly once unless the user explicitly asks to retry.

Windows:

```powershell
$prompt = @'
<complete image prompt>
'@
$codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { "$env:USERPROFILE\.codex" }
& "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass `
    -File "$codexHome\skills\madapi-imagegen\scripts\generate.ps1" `
    -Prompt $prompt
```

macOS:

```sh
codex_home=${CODEX_HOME:-"$HOME/.codex"}
/bin/sh "$codex_home/skills/madapi-imagegen/scripts/generate.sh" "<complete image prompt>"
```

4. Parse the returned JSON. A successful result contains `ok=true` and an absolute `path`.
5. Render the image from `source_url` with Markdown image syntax. This MadAPI URL is the most reliable preview source on Windows and macOS.
6. Add a clickable `source_url` link for opening or downloading the original, then mention the saved absolute local path. Do not place a Windows backslash path inside Markdown image syntax.

The script reads the existing MadAPI key locally, calls `gpt-image-2` through MadAPI's standard Images API, requests a URL first, downloads the original image to `$CODEX_HOME\generated_images`, validates PNG/JPEG/WebP, and never prints the key.

If generation fails, report the exact error and stop. Never invent a replacement image URL or file path.
