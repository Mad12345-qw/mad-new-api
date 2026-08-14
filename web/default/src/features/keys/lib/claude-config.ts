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

export type ClaudePlatform = "windows" | "macos";

export const CLAUDE_MODELS = [
  "claude-fable-5",
  "claude-opus-4-8",
  "claude-opus-5",
  "claude-sonnet-5",
  "claude-haiku-4-5",
] as const;

const WINDOWS_INSTALLER_URL = "https://mad.myddns.me/mad-claude/install.ps1";
const MACOS_INSTALLER_URL = "https://mad.myddns.me/mad-claude/install.sh";

function escapePowerShellSingleQuoted(value: string): string {
  return value.replaceAll("'", "''");
}

function escapeShellSingleQuoted(value: string): string {
  return value.replaceAll("'", "'\"'\"'");
}

export function buildClaudeInstallCommand(options: {
  apiKey: string;
  platform: ClaudePlatform;
  installLanguage?: boolean;
}): string {
  if (options.platform === "macos") {
    const languageEnvironment = options.installLanguage
      ? "MADAPI_CLAUDE_INSTALL_LANGUAGE='1' "
      : "";
    return `p=$(mktemp) && trap 'rm -f "$p"' EXIT && curl -fsSL '${MACOS_INSTALLER_URL}' -o "$p" && ${languageEnvironment}MADAPI_KEY='${escapeShellSingleQuoted(options.apiKey)}' /bin/sh "$p"`;
  }

  const languageEnvironment = options.installLanguage
    ? "$env:MADAPI_CLAUDE_INSTALL_LANGUAGE='1'; "
    : "";
  return `$env:MADAPI_KEY='${escapePowerShellSingleQuoted(options.apiKey)}'; ${languageEnvironment}try { & ([ScriptBlock]::Create((irm '${WINDOWS_INSTALLER_URL}'))) } finally { Remove-Item Env:MADAPI_KEY -ErrorAction SilentlyContinue; Remove-Item Env:MADAPI_CLAUDE_INSTALL_LANGUAGE -ErrorAction SilentlyContinue }`;
}

export function detectClaudePlatform(userAgent: string): ClaudePlatform {
  return /Macintosh|Mac OS X|iPhone|iPad|iPod/i.test(userAgent)
    ? "macos"
    : "windows";
}
