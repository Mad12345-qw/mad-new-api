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
import DOMPurify, { type Config } from 'dompurify'
import { useEffect, useMemo, useRef } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { CopyButton } from '@/components/copy-button'
import { cn } from '@/lib/utils'

export type HtmlContentVariant = 'inline' | 'isolated'

interface HtmlContentProps {
  content: string
  className?: string
  variant?: HtmlContentVariant
}

const isolatedContentSandbox =
  'allow-forms allow-popups allow-popups-to-escape-sandbox allow-presentation'

const isolatedContentBaseStyles = `
<style>
  :host {
    display: block;
    width: 100%;
    color: inherit;
    font: inherit;
  }

  *,
  *::before,
  *::after {
    box-sizing: border-box;
  }

  img,
  video,
  iframe {
    max-width: 100%;
  }

  iframe {
    border: 0;
  }
</style>
`

const isolatedSanitizeOptions = {
  ADD_ATTR: [
    'allowfullscreen',
    'autoplay',
    'class',
    'controls',
    'default',
    'id',
    'kind',
    'label',
    'loading',
    'loop',
    'muted',
    'playsinline',
    'poster',
    'preload',
    'referrerpolicy',
    'rel',
    'srclang',
    'style',
    'target',
  ],
  ADD_TAGS: ['audio', 'iframe', 'picture', 'source', 'style', 'track', 'video'],
  FORBID_ATTR: ['srcdoc'],
  FORBID_TAGS: ['base', 'embed', 'link', 'meta', 'object', 'script'],
  FORCE_BODY: true,
} satisfies Config

function hardenIsolatedHtml(html: string): string {
  if (typeof document === 'undefined') {
    return html
  }

  const template = document.createElement('template')
  template.innerHTML = html

  template.content.querySelectorAll('a[target="_blank"]').forEach((link) => {
    const rel = new Set(
      link
        .getAttribute('rel')
        ?.split(/\s+/)
        .filter(Boolean) ?? []
    )

    rel.add('noopener')
    rel.add('noreferrer')
    link.setAttribute('rel', [...rel].join(' '))
  })

  template.content.querySelectorAll('iframe').forEach((frame) => {
    frame.removeAttribute('srcdoc')
    frame.setAttribute('sandbox', isolatedContentSandbox)
    frame.setAttribute('referrerpolicy', 'no-referrer')

    if (!frame.hasAttribute('loading')) {
      frame.setAttribute('loading', 'lazy')
    }
  })

  return template.innerHTML
}

function sanitizeHtmlContent(
  content: string,
  variant: HtmlContentVariant
): string {
  if (variant === 'isolated') {
    const html = DOMPurify.sanitize(content, isolatedSanitizeOptions)

    return hardenIsolatedHtml(html)
  }

  return DOMPurify.sanitize(content)
}

function syncDarkClass(wrapper: HTMLElement): void {
  const isDark = document.documentElement.classList.contains('dark')
  wrapper.classList.toggle('dark', isDark)
}

function IsolatedHtmlContent(props: {
  className?: string
  html: string
}): React.ReactElement {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const container = containerRef.current
    if (!container) {
      return
    }

    const shadowRoot =
      container.shadowRoot ?? container.attachShadow({ mode: 'open' })
    const applicationStyleNodes = [
      ...document.head.querySelectorAll<HTMLLinkElement | HTMLStyleElement>(
        'style, link[rel="stylesheet"]'
      ),
    ].map((node) => node.cloneNode(true))

    const wrapper = document.createElement('div')
    syncDarkClass(wrapper)
    wrapper.innerHTML = props.html

    const copyButtonRoots: Root[] = []
    wrapper.querySelectorAll('pre > code').forEach((code) => {
      const pre = code.parentElement
      if (!pre) return

      const buttonHost = document.createElement('div')
      buttonHost.style.cssText =
        'position:absolute;top:10px;right:10px;z-index:1;display:flex;'
      pre.style.position = 'relative'
      pre.append(buttonHost)

      const root = createRoot(buttonHost)
      root.render(
        <CopyButton
          value={code.textContent ?? ''}
          tooltip='Copy code'
          successTooltip='Copied!'
          className='h-7 w-7 border border-white/10 bg-black/30 text-white hover:bg-black/50'
          iconClassName='size-3.5'
        />
      )
      copyButtonRoots.push(root)
    })

    const contentTemplate = document.createElement('template')
    contentTemplate.innerHTML = isolatedContentBaseStyles

    shadowRoot.replaceChildren(
      ...applicationStyleNodes,
      contentTemplate.content,
      wrapper
    )

    const observer = new MutationObserver(() => syncDarkClass(wrapper))
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })

    return () => {
      observer.disconnect()
      copyButtonRoots.forEach((root) => root.unmount())
    }
  }, [props.html])

  return (
    <div
      ref={containerRef}
      className={cn('block w-full', props.className)}
    />
  )
}

function InlineHtmlContent(props: {
  className?: string
  html: string
}): React.ReactElement {
  const containerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    const copyButtonRoots: Root[] = []
    container.querySelectorAll('pre > code').forEach((code) => {
      const pre = code.parentElement
      if (!pre) return

      const buttonHost = document.createElement('div')
      buttonHost.style.cssText =
        'position:absolute;top:10px;right:10px;z-index:1;display:flex;'
      pre.style.position = 'relative'
      pre.append(buttonHost)

      const root = createRoot(buttonHost)
      root.render(
        <CopyButton
          value={code.textContent ?? ''}
          tooltip='Copy code'
          successTooltip='Copied!'
          className='h-7 w-7 border border-border/70 bg-background/90 shadow-sm hover:bg-muted'
          iconClassName='size-3.5'
        />
      )
      copyButtonRoots.push(root)
    })

    return () => copyButtonRoots.forEach((root) => root.unmount())
  }, [props.html])

  return (
    <div
      ref={containerRef}
      className={cn(
        'prose prose-neutral dark:prose-invert max-w-none',
        props.className
      )}
      // eslint-disable-next-line react/no-danger -- html is sanitized above
      dangerouslySetInnerHTML={{ __html: props.html }}
    />
  )
}

export function HtmlContent(props: HtmlContentProps) {
  const variant = props.variant ?? 'inline'
  const html = useMemo(
    () => sanitizeHtmlContent(props.content, variant),
    [props.content, variant]
  )

  if (variant === 'isolated') {
    return <IsolatedHtmlContent className={props.className} html={html} />
  }

  return <InlineHtmlContent className={props.className} html={html} />
}
