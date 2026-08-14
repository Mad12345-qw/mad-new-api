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
import { useQuery } from '@tanstack/react-query'
import { Bell, Send } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Dialog } from '@/components/dialog'
import { RichContent } from '@/components/rich-content'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { getNotice } from '@/lib/api'

const TELEGRAM_URL = 'https://t.me/madapimyddnsme'
const CLOSE_DATE_KEY = 'mad_telegram_announcement_close_date'
const SESSION_SEEN_KEY = 'mad_telegram_announcement_seen'

export function MadTelegramAnnouncement() {
  const [open, setOpen] = useState(false)
  const { data: noticeResponse, isLoading } = useQuery({
    queryKey: ['notice'],
    queryFn: getNotice,
    staleTime: 10 * 1000,
    refetchOnWindowFocus: true,
  })
  const notice = noticeResponse?.success
    ? (noticeResponse.data || '').trim()
    : ''

  useEffect(() => {
    if (window.location.pathname.startsWith('/setup')) return

    const today = new Date().toDateString()
    const closedToday = window.localStorage.getItem(CLOSE_DATE_KEY) === today
    const seenThisSession =
      window.sessionStorage.getItem(SESSION_SEEN_KEY) === 'true'

    if (!closedToday && !seenThisSession) {
      setOpen(true)
    }
  }, [])

  const closeForSession = () => {
    window.sessionStorage.setItem(SESSION_SEEN_KEY, 'true')
    setOpen(false)
  }

  const closeForToday = () => {
    window.localStorage.setItem(CLOSE_DATE_KEY, new Date().toDateString())
    window.sessionStorage.setItem(SESSION_SEEN_KEY, 'true')
    setOpen(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          closeForSession()
          return
        }
        setOpen(true)
      }}
      title={
        <span className='flex items-center gap-2 text-zinc-950'>
          <Bell className='size-4 text-[#9b793b]' />
          系统公告
        </span>
      }
      footer={
        <>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-8 flex-1 rounded-lg px-3 text-xs text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900'
            onClick={closeForToday}
          >
            今日关闭
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-8 flex-1 rounded-lg px-3 text-xs text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900'
            onClick={closeForSession}
          >
            关闭公告
          </Button>
        </>
      }
      contentClassName='w-[calc(100vw-2rem)] max-w-[350px] gap-2 border-zinc-200 bg-white p-3 text-zinc-950 sm:max-w-[350px] sm:p-3'
      headerClassName='border-b border-zinc-100 pb-2'
      bodyClassName='space-y-2 py-0'
      footerClassName='-mx-3 -mb-3 border-t border-zinc-100 bg-white px-3 py-2 sm:-mx-3 sm:-mb-3 sm:p-2'
      contentHeight='214px'
      showCloseButton
    >
      <div className='h-[88px] overflow-hidden rounded-lg border border-zinc-200 bg-zinc-50/80'>
        <ScrollArea className='h-full'>
          <div className='space-y-2 px-3 py-2.5 text-left text-xs leading-5 text-zinc-700'>
            {isLoading ? (
              <p className='text-zinc-400'>公告加载中...</p>
            ) : notice ? (
              <RichContent
                breaks
                className='break-words [&_a]:text-[#8e6a31] [&_a]:underline [&_p]:m-0'
                content={notice}
              />
            ) : (
              <p className='text-zinc-400'>暂无系统公告</p>
            )}
          </div>
        </ScrollArea>
      </div>
      <div className='flex items-center gap-2.5'>
        <img
          src='/mad-home/assets/mad-logo.svg'
          alt='Mad API'
          className='size-12 shrink-0 rounded-lg border border-[#4a391d] bg-[#090907] shadow-[0_10px_26px_-16px_rgba(142,106,49,0.85)]'
        />
        <div className='min-w-0 text-left'>
          <h2 className='text-xs font-semibold text-zinc-950'>
            Mad API 官方频道
          </h2>
          <p className='mt-0.5 text-[11px] leading-4 text-zinc-500'>
            Telegram 频道入口，获取最新公告、模型更新与使用通知
          </p>
        </div>
      </div>
      <Button
        render={
          <a href={TELEGRAM_URL} target='_blank' rel='noreferrer noopener' />
        }
        className='h-9 w-full rounded-lg bg-[#c8a96a] text-[#15120d] shadow-[inset_0_1px_0_rgba(255,255,255,0.35)] hover:bg-[#d4b978]'
      >
        <Send className='size-4' />
        立即加入
      </Button>
    </Dialog>
  )
}
