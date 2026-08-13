import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

function readSource(relativePath: string) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

describe('MadAPI 3.0 UI parity', () => {
  test('keeps the compact Agent Access action before the status switch', () => {
    const source = readSource('../../components/data-table-row-actions.tsx')
    const agentButton = source.indexOf("className='h-7 min-w-0")
    const agentLabel = source.indexOf("{t('Agent Access')}", agentButton)
    const statusSwitch = source.indexOf('onClick={handleToggleStatus}')

    assert.ok(agentButton >= 0, 'Agent Access must remain a compact button')
    assert.ok(
      agentLabel > agentButton,
      'Agent Access button must show its label'
    )
    assert.ok(
      statusSwitch > agentLabel,
      'Agent Access must remain before the status switch'
    )
  })

  test('keeps the Agent Access instructions and action-column width', () => {
    const dialog = readSource(
      '../../components/dialogs/agent-access-dialog.tsx'
    )
    const columns = readSource('../../components/api-keys-columns.tsx')

    assert.match(dialog, /t\('Setup Instructions'\)/)
    assert.match(dialog, /t\('Copy the one-command setup below'\)/)
    assert.match(dialog, /Open PowerShell, paste the command, and press Enter/)
    assert.match(dialog, /Restart \{\{agent\}\} and choose an available model/)
    assert.match(columns, /id: 'actions',[\s\S]*?size: 260/)
  })

  test('keeps popular wallet badges combined with the live bonus rate', () => {
    const source = readSource(
      '../../../wallet/components/recharge-form-card.tsx'
    )

    assert.match(source, /preset\.value === 128 \|\| preset\.value === 648/)
    assert.match(source, /promotion\.bonusRatePercent/)
    assert.match(source, /`\$\{badge\} · \$\{t\('Bonus \{\{rate\}\}%'/)
  })

  test('uses the 3.0 Chinese labels', () => {
    const locale = JSON.parse(
      readSource('../../../../i18n/locales/zh.json')
    ) as { translation: Record<string, string> }

    assert.equal(locale.translation['Lifetime Usage'], '累计用量')
    assert.equal(locale.translation['Bonus {{rate}}%'], '加赠 {{rate}}%')
    assert.equal(locale.translation['Agent Access'], 'Agent接入')
  })
})
