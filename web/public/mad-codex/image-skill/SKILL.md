---
name: "madapi-imagegen"
description: "Mandatory MadAPI gpt-image-2 image generator for Codex Desktop in OAuth or API Key mode. Use for every request to generate, create, draw, render, or design a bitmap image. Never use the bundled imagegen skill or built-in image_gen tool."
---

# MadAPI Image Generation

Keep the current Codex login mode unchanged.

## Required workflow

1. Do not use the bundled `imagegen` skill or the built-in `image_gen` tool.
2. Do not fabricate an attachment or output path.
3. Pass the user's requested image subject and requirements through unchanged. Do not replace the subject, invent a different scene, or expand a short request into unrelated creative content.
4. Run the installed platform generator exactly once unless the user explicitly asks for multiple images or asks to retry. The MadAPI installer enables command network access, so do not request additional permissions.

Windows:

```powershell
$prompt = @'
<the user's image request, preserving its subject and requirements>
'@
$skillRoot = Join-Path $env:USERPROFILE '.agents\skills\madapi-imagegen'
& "$PSHOME\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass `
    -File "$skillRoot\scripts\generate.ps1" `
    -Prompt $prompt
```

macOS:

```sh
codex_home=${CODEX_HOME:-"$HOME/.codex"}
/bin/sh "$HOME/.agents/skills/madapi-imagegen/scripts/generate.sh" "<the user's image request>"
```

5. Parse the returned JSON. A successful result contains `ok=true` and an absolute `path`.
6. Render `source_url` exactly once with Markdown image syntax. Do not render both `source_url` and `path` as separate images.
7. Add one clickable `source_url` link for opening or downloading the original, then mention the saved absolute local path. Do not place a Windows backslash path inside Markdown image syntax.

The script reads the existing MadAPI key locally, calls `gpt-image-2` through MadAPI's standard Images API, requests a URL first, downloads the original image to the current task's `outputs` directory, validates PNG/JPEG/WebP, and never prints the key.

If generation fails, report the exact error and stop. Never invent a replacement image URL or file path.
