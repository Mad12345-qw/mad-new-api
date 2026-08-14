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
import { SidebarTrigger } from '@/components/ui/sidebar'
import { cn } from '@/lib/utils'

type HeaderProps = React.HTMLAttributes<HTMLElement> & {
  scrolled?: boolean
}

export function Header({ className, children, scrolled = false, ...props }: HeaderProps) {
  return (
    <header
      className={cn(
        'pointer-events-none relative z-40 h-[var(--app-header-height,4rem)] w-full shrink-0',
        className
      )}
      {...props}
    >
      <div className='pointer-events-auto absolute top-0 left-2 flex h-16 items-center'>
        <SidebarTrigger variant='ghost' className='size-8' />
      </div>
      <div
        className={cn(
          'pointer-events-auto mx-auto h-full transition-all duration-700 ease-[cubic-bezier(0.16,1,0.3,1)]',
          scrolled ? 'max-w-[80rem] px-3 pt-3' : 'max-w-none px-0 pt-0'
        )}
      >
        <div
          className={cn(
            'flex items-center gap-1.5 whitespace-nowrap transition-all duration-700 ease-[cubic-bezier(0.16,1,0.3,1)] sm:gap-2',
            scrolled
              ? 'bg-background/60 ring-border/50 h-12 rounded-2xl pr-1.5 pl-4 shadow-[0_2px_16px_-6px_rgba(0,0,0,0.08),0_0_0_0.5px_rgba(0,0,0,0.02)] ring-[0.5px] backdrop-blur-2xl dark:shadow-[0_2px_16px_-6px_rgba(0,0,0,0.4)]'
              : 'h-16 pr-2 pl-12 sm:pr-3 sm:pl-14'
          )}
        >
          {children}
        </div>
      </div>
    </header>
  )
}
