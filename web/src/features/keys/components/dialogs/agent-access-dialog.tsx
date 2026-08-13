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
import {
  Apple,
  Bot,
  Check,
  Copy,
  Image,
  Languages,
  Monitor,
  ShieldCheck,
  Terminal,
} from 'lucide-react'
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
      contentClassName='max-h-[min(90vh,760px)] overflow-y-auto sm:max-w-2xl'
      footer={
        <Button
          className='w-full sm:w-auto sm:min-w-40'
          onClick={handleCopy}
          disabled={!tokenKey}
        >
          {copied ? <Check /> : <Copy />}
          {t('Copy Command')}
        </Button>
      }
    >
      <div className='space-y-4'>
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

        <div className='space-y-1.5'>
          <Label className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium'>
            <Monitor className='size-3.5' />
            {t('Operating System')}
          </Label>
          <Tabs
            value={platform}
            onValueChange={(value) => setPlatform(value as AgentPlatform)}
          >
            <TabsList className='grid w-full grid-cols-2'>
              <TabsTrigger value='windows'>
                <Monitor />
                Windows
              </TabsTrigger>
              <TabsTrigger value='macos'>
                <Apple />
                macOS
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </div>

        {agent === 'codex' ? (
          <div className='space-y-1.5'>
            <Label className='text-muted-foreground text-xs font-medium'>
              {t('Login Mode')}
            </Label>
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
          <label className='border-border flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5'>
            <Checkbox
              checked={installChinese}
              onCheckedChange={(checked) => setInstallChinese(checked === true)}
            />
            <Languages className='mt-0.5 size-4 shrink-0 text-sky-600 dark:text-sky-400' />
            <span className='min-w-0'>
              <span className='block text-sm font-medium'>
                {t('Install Chinese Interface')}
              </span>
              <span className='text-muted-foreground mt-0.5 block text-xs'>
                {t('Language installation is separate and cannot roll back Claude access')}
              </span>
            </span>
          </label>
        )}

        <div className='bg-muted/35 overflow-hidden rounded-lg border'>
          <div className='border-border flex items-center justify-between gap-3 border-b px-3 py-2'>
            <span className='text-muted-foreground text-xs font-medium'>
              {platform === 'windows'
                ? t('PowerShell command')
                : t('Terminal command')}
            </span>
            <span className='text-muted-foreground shrink-0 text-[10px] uppercase'>
              {platform === 'windows' ? 'PowerShell' : 'Shell'}
            </span>
          </div>
          <Textarea
            value={command}
            readOnly
            rows={6}
            spellCheck={false}
            className='max-h-36 min-h-32 resize-none rounded-none border-0 bg-transparent font-mono text-[11px] leading-5 shadow-none focus-visible:ring-0 sm:text-xs'
            aria-label={t('Install Command')}
          />
        </div>

        <div className='space-y-2.5 rounded-lg border p-3'>
          <div className='text-sm font-medium'>{t('Setup Instructions')}</div>
          <div className='space-y-2 text-xs'>
            <div className='flex items-start gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                1
              </span>
              <span>{t('Copy the one-command setup below')}</span>
            </div>
            <div className='flex items-start gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                2
              </span>
              <span>
                {platform === 'windows'
                  ? t('Open PowerShell, paste the command, and press Enter')
                  : t('Open Terminal, paste the command, and press Enter')}
              </span>
            </div>
            <div className='flex items-start gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                3
              </span>
              <span>
                {t('Restart {{agent}} and choose an available model', {
                  agent: agent === 'codex' ? 'Codex' : 'Claude',
                })}
              </span>
            </div>
          </div>
        </div>

        <div className='grid grid-cols-1 gap-2 text-xs sm:grid-cols-3'>
          <div className='flex items-center gap-1.5'>
            <ShieldCheck className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400' />
            <span>{t('Backs up the original configuration')}</span>
          </div>
          <div className='flex items-center gap-1.5'>
            <Bot className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400' />
            <span>
              {agent === 'codex'
                ? t('Updates the MadAPI provider safely')
                : t('Installs five Claude models')}
            </span>
          </div>
          <div className='flex items-center gap-1.5'>
            {agent === 'claude' ? (
              <Image className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400' />
            ) : (
              <Terminal className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400' />
            )}
            <span>
              {agent === 'claude'
                ? t('Installs the image generation tool')
                : t('Keeps plugins and MCP settings')}
            </span>
          </div>
        </div>
      </div>
    </Dialog>
  )
}
