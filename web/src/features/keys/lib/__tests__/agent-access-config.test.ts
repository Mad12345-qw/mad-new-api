import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { buildAgentInstallCommand } from '../agent-access-config'

describe('Agent Access command contract', () => {
  test('keeps Codex access on the dedicated route for OAuth and API Key modes', () => {
    const oauthCommand = buildAgentInstallCommand({
      agent: 'codex',
      platform: 'windows',
      token: 'sk-test',
      serverAddress: 'https://mad.example/',
      codexLoginMode: 'oauth',
      installClaudeChinese: false,
    })
    const apiKeyCommand = buildAgentInstallCommand({
      agent: 'codex',
      platform: 'macos',
      token: 'sk-test',
      serverAddress: 'https://mad.example/',
      codexLoginMode: 'apikey',
      installClaudeChinese: false,
    })

    assert.match(oauthCommand, /MADAPI_CODEX_LOGIN_MODE=/)
    assert.match(oauthCommand, /https:\/\/mad\.example\/mad-codex\/install\.ps1/)
    assert.match(apiKeyCommand, /MADAPI_CODEX_LOGIN_MODE=/)
    assert.match(apiKeyCommand, /https:\/\/mad\.example\/mad-codex\/install\.sh/)
    assert.doesNotMatch(oauthCommand, /\/codex\/cockpit\/v1/)
    assert.doesNotMatch(apiKeyCommand, /\/codex\/cockpit\/v1/)
  })

  test('keeps Claude as the parallel Agent Access option', () => {
    const command = buildAgentInstallCommand({
      agent: 'claude',
      platform: 'windows',
      token: 'sk-test',
      serverAddress: 'https://mad.example',
      codexLoginMode: 'oauth',
      installClaudeChinese: true,
    })

    assert.match(command, /https:\/\/mad\.example\/mad-claude\/install\.ps1/)
    assert.match(command, /MADAPI_CLAUDE_INSTALL_LANGUAGE=/)
    assert.doesNotMatch(command, /MADAPI_CODEX_LOGIN_MODE=/)
  })
})
