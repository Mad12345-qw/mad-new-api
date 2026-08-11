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
import { Bot, Check, Copy, Languages, Monitor, Terminal } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import {
  buildAgentInstallCommand,
  detectAgentPlatform,
  type AgentKind,
  type AgentPlatform,
  type CodexLoginMode,
} from '../../lib/agent-access-config'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  tokenKey: string
}

function getServerAddress(): string {
  try {
    const raw = localStorage.getItem('status')
    if (raw) {
      const status = JSON.parse(raw)
      if (status.server_address) return String(status.server_address)
    }
  } catch {
    /* use current origin */
  }
  return window.location.origin
}

export function AgentAccessDialog({ open, onOpenChange, tokenKey }: Props) {
  const { t } = useTranslation()
  const [agent, setAgent] = useState<AgentKind>('codex')
  const [platform, setPlatform] = useState<AgentPlatform>('windows')
  const [loginMode, setLoginMode] = useState<CodexLoginMode>('oauth')
  const [installChinese, setInstallChinese] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!open) return
    setPlatform(detectAgentPlatform())
    setCopied(false)
  }, [open])

  const command = useMemo(
    () =>
      buildAgentInstallCommand({
        agent,
        platform,
        token: tokenKey,
        serverAddress: getServerAddress(),
        codexLoginMode: loginMode,
        installClaudeChinese: installChinese,
      }),
    [agent, installChinese, loginMode, platform, tokenKey]
  )

  const handleCopy = async () => {
    if (!(await copyToClipboard(command))) return
    setCopied(true)
    toast.success(t('Copied'))
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Agent Access')}
      contentClassName='sm:max-w-xl'
      footer={
        <Button onClick={handleCopy} disabled={!tokenKey}>
          {copied ? <Check /> : <Copy />}
          {t('Copy Command')}
        </Button>
      }
    >
      <div className='space-y-5'>
        <Tabs value={agent} onValueChange={(value) => setAgent(value as AgentKind)}>
          <TabsList className='grid w-full grid-cols-2'>
            <TabsTrigger value='codex'>
              <Terminal />
              {t('Codex Access')}
            </TabsTrigger>
            <TabsTrigger value='claude'>
              <Bot />
              {t('Claude Access')}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <div className='space-y-2'>
          <Label>{t('Operating System')}</Label>
          <Tabs
            value={platform}
            onValueChange={(value) => setPlatform(value as AgentPlatform)}
          >
            <TabsList className='grid w-full grid-cols-2'>
              <TabsTrigger value='windows'>
                <Monitor />
                Windows
              </TabsTrigger>
              <TabsTrigger value='macos'>macOS</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>

        {agent === 'codex' ? (
          <div className='space-y-2'>
            <Label>{t('Login Mode')}</Label>
            <Tabs
              value={loginMode}
              onValueChange={(value) => setLoginMode(value as CodexLoginMode)}
            >
              <TabsList className='grid w-full grid-cols-2'>
                <TabsTrigger value='oauth'>OAuth</TabsTrigger>
                <TabsTrigger value='apikey'>API Key</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>
        ) : (
          <label className='flex cursor-pointer items-center gap-3'>
            <Checkbox
              checked={installChinese}
              onCheckedChange={(checked) => setInstallChinese(checked === true)}
            />
            <Languages className='size-4 text-muted-foreground' />
            <span className='text-sm'>{t('Install Chinese Interface')}</span>
          </label>
        )}

        <Textarea
          value={command}
          readOnly
          rows={6}
          spellCheck={false}
          className='min-h-32 resize-none font-mono text-xs'
          aria-label={t('Install Command')}
        />
      </div>
    </Dialog>
  )
}
