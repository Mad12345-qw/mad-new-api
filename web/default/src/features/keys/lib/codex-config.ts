/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

export type CodexPlatform = 'windows' | 'macos'
export type CodexLoginMode = 'oauth' | 'apikey'

const CODEX_BASE_URL = 'https://mad.myddns.me/codex/v1'
const WINDOWS_INSTALLER_URL = 'https://mad.myddns.me/mad-codex/install.ps1'
const MACOS_INSTALLER_URL = 'https://mad.myddns.me/mad-codex/install.sh'

function escapePowerShellSingleQuoted(value: string): string {
  return value.replaceAll("'", "''")
}

function escapeShellSingleQuoted(value: string): string {
  return value.replaceAll("'", "'\"'\"'")
}

function escapeTomlBasicString(value: string): string {
  return `"${value
    .replaceAll('\\', '\\\\')
    .replaceAll('"', '\\"')
    .replaceAll('\r', '\\r')
    .replaceAll('\n', '\\n')
    .replaceAll('\t', '\\t')}"`
}

function buildAuthBlock(platform: CodexPlatform): string {
  const authCommand =
    platform === 'macos'
      ? 'h="${CODEX_HOME:-$HOME/.codex}"; exec cat "$h/madapi.key"'
      : "$h=if([string]::IsNullOrWhiteSpace([string]$env:CODEX_HOME)){Join-Path ([Environment]::GetFolderPath('UserProfile')) '.codex'}else{[string]$env:CODEX_HOME};[Console]::Out.Write([IO.File]::ReadAllText((Join-Path $h 'madapi.key')).Trim())"

  return `[model_providers.madapi.auth]
command = "${platform === 'macos' ? '/bin/sh' : 'powershell.exe'}"
args = ${
    platform === 'macos'
      ? `["-c", ${escapeTomlBasicString(authCommand)}]`
      : `["-NoProfile", "-Command", ${escapeTomlBasicString(authCommand)}]`
  }
timeout_ms = 5000
refresh_interval_ms = 300000`
}

function buildProviderBlock(platform: CodexPlatform): string {
  return `[model_providers.madapi]
name = "MadAPI"
base_url = "${CODEX_BASE_URL}"
wire_api = "responses"
stream_idle_timeout_ms = 360000
request_max_retries = 0
context_window_override = 1048576

${buildAuthBlock(platform)}`
}

export function buildCodexConfig(options: {
  apiKey: string
  platform: CodexPlatform
}): string {
  return `model_provider = "madapi"
model = "gpt-5.6-sol"
model_reasoning_effort = "high"
model_auto_compact_token_limit = 500000
disable_response_storage = true

${buildProviderBlock(options.platform)}`
}

export function buildCodexInstallCommand(options: {
  apiKey: string
  platform: CodexPlatform
  loginMode: CodexLoginMode
}): string {
  if (options.platform === 'macos') {
    return `p=$(mktemp) && trap 'rm -f "$p"' EXIT && curl -fsSL '${MACOS_INSTALLER_URL}' -o "$p" && MADAPI_KEY='${escapeShellSingleQuoted(options.apiKey)}' MADAPI_CODEX_LOGIN_MODE='${options.loginMode}' /bin/sh "$p"`
  }

  return `$env:MADAPI_KEY='${escapePowerShellSingleQuoted(options.apiKey)}'; $env:MADAPI_CODEX_LOGIN_MODE='${options.loginMode}'; try { & ([ScriptBlock]::Create((irm '${WINDOWS_INSTALLER_URL}'))) } finally { Remove-Item Env:MADAPI_KEY -ErrorAction SilentlyContinue; Remove-Item Env:MADAPI_CODEX_LOGIN_MODE -ErrorAction SilentlyContinue }`
}

export function detectCodexPlatform(userAgent: string): CodexPlatform {
  return /Macintosh|Mac OS X|iPhone|iPad|iPod/i.test(userAgent)
    ? 'macos'
    : 'windows'
}
