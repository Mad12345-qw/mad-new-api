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
export type AgentKind = 'codex' | 'claude'
export type AgentPlatform = 'windows' | 'macos'
export type CodexLoginMode = 'oauth' | 'apikey'

type AgentCommandOptions = {
  agent: AgentKind
  platform: AgentPlatform
  token: string
  serverAddress: string
  codexLoginMode: CodexLoginMode
  installClaudeChinese: boolean
}

function quotePowerShell(value: string): string {
  return `'${value.replaceAll("'", "''")}'`
}

function quoteShell(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`
}

export function detectAgentPlatform(): AgentPlatform {
  if (typeof navigator === 'undefined') return 'windows'
  return /mac/i.test(navigator.userAgent) ? 'macos' : 'windows'
}

export function buildAgentInstallCommand(options: AgentCommandOptions): string {
  const baseUrl = options.serverAddress.replace(/\/+$/, '')
  const assetRoot =
    options.agent === 'codex' ? `${baseUrl}/mad-codex` : `${baseUrl}/mad-claude`

  if (options.platform === 'windows') {
    const assignments = [
      `$env:MADAPI_KEY=${quotePowerShell(options.token)}`,
      `$env:MADAPI_BASE_URL=${quotePowerShell(baseUrl)}`,
    ]
    if (options.agent === 'codex') {
      assignments.push(
        `$env:MADAPI_CODEX_LOGIN_MODE=${quotePowerShell(options.codexLoginMode)}`
      )
    } else {
      assignments.push(
        `$env:MADAPI_CLAUDE_BASE_URL=${quotePowerShell(`${baseUrl}/v1`)}`,
        `$env:MADAPI_CLAUDE_INSTALL_LANGUAGE=${quotePowerShell(options.installClaudeChinese ? '1' : '0')}`
      )
    }
    return `${assignments.join('; ')}; & ([ScriptBlock]::Create((Invoke-RestMethod -UseBasicParsing ${quotePowerShell(`${assetRoot}/install.ps1`)})))`
  }

  const variables = [
    `MADAPI_KEY=${quoteShell(options.token)}`,
    `MADAPI_BASE_URL=${quoteShell(baseUrl)}`,
  ]
  if (options.agent === 'codex') {
    variables.push(
      `MADAPI_CODEX_LOGIN_MODE=${quoteShell(options.codexLoginMode)}`
    )
  } else {
    variables.push(
      `MADAPI_CLAUDE_BASE_URL=${quoteShell(`${baseUrl}/v1`)}`,
      `MADAPI_CLAUDE_INSTALL_LANGUAGE=${quoteShell(options.installClaudeChinese ? '1' : '0')}`
    )
  }
  return `${variables.join(' ')} /bin/sh -c "$(curl -fsSL ${quoteShell(`${assetRoot}/install.sh`)})"`
}
