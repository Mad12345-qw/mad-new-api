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
  Check,
  Copy,
  Monitor,
  ShieldCheck,
  SquareTerminal,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { copyToClipboard } from '@/lib/copy-to-clipboard'

import {
  buildCodexInstallCommand,
  detectCodexPlatform,
  type CodexLoginMode,
  type CodexPlatform,
} from '../../lib/codex-config'

type CodexSetupDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  tokenKey: string
  tokenName?: string
}

function getInitialPlatform(): CodexPlatform {
  if (typeof navigator === 'undefined') return 'windows'
  return detectCodexPlatform(navigator.userAgent)
}

export function CodexSetupDialog({
  open,
  onOpenChange,
  tokenKey,
  tokenName,
}: CodexSetupDialogProps) {
  const { t } = useTranslation()
  const [platform, setPlatform] = useState<CodexPlatform>(getInitialPlatform)
  const [loginMode, setLoginMode] = useState<CodexLoginMode>('oauth')
  const [copied, setCopied] = useState(false)
  const copiedTimer = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => {
    if (!open) return
    setPlatform(getInitialPlatform())
    setLoginMode('oauth')
    setCopied(false)
  }, [open])

  useEffect(() => {
    return () => clearTimeout(copiedTimer.current)
  }, [])

  const installCommand = useMemo(
    () => buildCodexInstallCommand({ apiKey: tokenKey, platform, loginMode }),
    [loginMode, platform, tokenKey]
  )

  const handleCopy = async () => {
    const success = await copyToClipboard(installCommand)
    if (!success) {
      toast.error(t('Failed to copy configuration'))
      return
    }

    setCopied(true)
    toast.success(t('Codex configuration copied'))
    clearTimeout(copiedTimer.current)
    copiedTimer.current = setTimeout(() => setCopied(false), 2000)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='max-h-[min(90vh,760px)] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden p-0 sm:max-w-2xl'>
        <DialogHeader className='px-4 pt-4 pr-12 sm:px-5 sm:pt-5 sm:pr-12'>
          <div className='flex items-center gap-2.5'>
            <div className='border-border bg-muted/50 flex size-8 shrink-0 items-center justify-center rounded-lg border'>
              <SquareTerminal className='size-4' />
            </div>
            <div className='min-w-0'>
              <DialogTitle>{t('Codex Setup')}</DialogTitle>
              <DialogDescription className='mt-1 truncate text-xs'>
                {t('API key')}: {tokenName || t('Current key')}
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <div className='min-h-0 space-y-4 overflow-y-auto px-4 pb-1 sm:px-5'>
          <div className='space-y-1.5'>
            <div className='text-muted-foreground text-xs font-medium'>
              {t('Login mode')}
            </div>
            <Tabs
              value={loginMode}
              onValueChange={(value) => setLoginMode(value as CodexLoginMode)}
            >
              <TabsList className='grid w-full grid-cols-2'>
                <TabsTrigger value='oauth'>{t('OAuth login')}</TabsTrigger>
                <TabsTrigger value='apikey'>{t('API Key login')}</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>

          <div className='space-y-1.5'>
            <div className='text-muted-foreground flex items-center gap-1.5 text-xs font-medium'>
              <Monitor className='size-3.5' />
              {t('Computer system detected automatically')}
            </div>
            <Tabs
              value={platform}
              onValueChange={(value) => setPlatform(value as CodexPlatform)}
            >
              <TabsList className='grid w-full grid-cols-2 sm:w-72'>
                <TabsTrigger value='windows'>
                  <Monitor /> Windows
                </TabsTrigger>
                <TabsTrigger value='macos'>
                  <Apple /> macOS
                </TabsTrigger>
              </TabsList>
            </Tabs>
          </div>

          <div className='bg-muted/35 rounded-lg border'>
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
            <pre className='text-foreground max-h-36 overflow-auto p-3 font-mono text-[11px] leading-5 break-all whitespace-pre-wrap sm:text-xs'>
              {installCommand}
            </pre>
          </div>

          <div className='grid grid-cols-1 gap-2 text-xs sm:grid-cols-3'>
            {[
              t('Backs up the original configuration'),
              t('Keeps plugins and MCP settings'),
              t('Updates the MadAPI provider safely'),
            ].map((label) => (
              <div key={label} className='flex items-center gap-1.5'>
                <ShieldCheck className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400' />
                <span>{label}</span>
              </div>
            ))}
          </div>

          <div className='space-y-2 text-xs'>
            <div className='flex gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                1
              </span>
              <span>{t('Copy the one-command setup below')}</span>
            </div>
            <div className='flex gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                2
              </span>
              <span>
                {platform === 'windows'
                  ? t('Open PowerShell, paste the command, and press Enter')
                  : t('Open Terminal, paste the command, and press Enter')}
              </span>
            </div>
            <div className='flex gap-2'>
              <span className='bg-foreground text-background flex size-5 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold'>
                3
              </span>
              <span>
                {loginMode === 'oauth'
                  ? t('Restart Codex and complete ChatGPT sign-in')
                  : t('Restart Codex and choose an available model')}
              </span>
            </div>
          </div>
        </div>

        <DialogFooter className='m-0 px-4 py-3 sm:px-5'>
          <Button className='w-full sm:w-auto sm:min-w-40' onClick={handleCopy}>
            {copied ? <Check /> : <Copy />}
            {copied ? t('Copied!') : t('Copy one-command setup')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
