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
import { describe, expect, test } from 'bun:test'

import {
  buildCodexConfig,
  buildCodexInstallCommand,
  detectCodexPlatform,
} from '../src/features/keys/lib/codex-config'

const API_KEY = 'sk-test-key-123'

describe('Codex configuration generation', () => {
  test('builds a complete Windows configuration', () => {
    const result = buildCodexConfig({
      apiKey: API_KEY,
      platform: 'windows',
    })

    expect(result).toContain('model_provider = "madapi"')
    expect(result).toContain('base_url = "https://mad.myddns.me/codex/v1"')
    expect(result).toContain('command = "powershell.exe"')
    expect(result).toContain('madapi.key')
    expect(result).not.toContain(API_KEY)
    expect(result).not.toContain('experimental_bearer_token')
    expect(result).not.toContain('requires_openai_auth')
  })

  test('builds a macOS provider configuration', () => {
    const result = buildCodexConfig({
      apiKey: API_KEY,
      platform: 'macos',
    })

    expect(result).toContain('command = "/bin/sh"')
    expect(result).toContain('madapi.key')
    expect(result).not.toContain(API_KEY)
    expect(result).toContain('model_provider = "madapi"')
  })

  test('builds native one-command installers', () => {
    const windows = buildCodexInstallCommand({
      apiKey: API_KEY,
      platform: 'windows',
      loginMode: 'oauth',
    })
    const macos = buildCodexInstallCommand({
      apiKey: API_KEY,
      platform: 'macos',
      loginMode: 'apikey',
    })

    expect(windows).toContain("$env:MADAPI_KEY='sk-test-key-123'")
    expect(windows).toContain("$env:MADAPI_CODEX_LOGIN_MODE='oauth'")
    expect(windows).toContain('/mad-codex/install.ps1')
    expect(windows).toContain('try {')
    expect(windows).toContain('finally {')
    expect(macos).toContain("MADAPI_KEY='sk-test-key-123'")
    expect(macos).toContain("MADAPI_CODEX_LOGIN_MODE='apikey'")
    expect(macos).toContain('/mad-codex/install.sh')
    expect(macos).toContain('mktemp')
    expect(macos).toContain(`trap 'rm -f "$p"' EXIT`)
    expect(macos).not.toContain('/bin/sh -c')
    expect(macos).not.toContain("'''")
  })

  test('detects Apple and Windows clients', () => {
    expect(detectCodexPlatform('Mozilla/5.0 (Macintosh; Intel Mac OS X)')).toBe(
      'macos'
    )
    expect(detectCodexPlatform('Mozilla/5.0 (iPhone; CPU iPhone OS)')).toBe(
      'macos'
    )
    expect(
      detectCodexPlatform('Mozilla/5.0 (Windows NT 10.0; Win64; x64)')
    ).toBe('windows')
  })
})
