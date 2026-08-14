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
import { DownloadIcon, FileIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  CodeBlock,
  CodeBlockCopyButton,
} from '@/components/ai-elements/code-block'
import { Loader } from '@/components/ai-elements/loader'
import { MessageContent } from '@/components/ai-elements/message'
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from '@/components/ai-elements/reasoning'
import { Response } from '@/components/ai-elements/response'
import { Shimmer } from '@/components/ai-elements/shimmer'
import {
  Source,
  Sources,
  SourcesContent,
  SourcesTrigger,
} from '@/components/ai-elements/sources'
import { cn } from '@/lib/utils'

import { MESSAGE_STATUS } from '../../constants'
import {
  getMessageAlignmentClass,
  getMessageContentState,
  isErrorMessage,
  type MessageAlignment,
} from '../../lib'
import { getMessageContentStyles } from '../../lib/message/message-styles'
import type { Message } from '../../types'
import { MessageError } from './message-error'
import { MessageMetadata } from './message-metadata'

function formatFileSize(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${Math.ceil(size / 1024)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

type PlaygroundMessageContentProps = {
  actions: ReactNode
  alignment: MessageAlignment
  errorActions?: ReactNode
  isSourceVisible?: boolean
  message: Message
  versionContent: string
}

export function PlaygroundMessageContent({
  actions,
  alignment,
  errorActions,
  isSourceVisible = false,
  message,
  versionContent,
}: PlaygroundMessageContentProps) {
  const { t } = useTranslation()
  const {
    displayContent,
    hasReasoning,
    hasSources,
    reasoningContent,
    showLoader,
    showMessageContent,
    sources,
  } = getMessageContentState(message, versionContent)
  const isError = isErrorMessage(message)
  const isMessageFinal =
    message.status !== MESSAGE_STATUS.LOADING &&
    message.status !== MESSAGE_STATUS.STREAMING
  const attachments = message.attachments ?? []
  const media = message.media ?? []
  const hasRichContent = attachments.length > 0 || media.length > 0

  return (
    <div
      className={cn(
        'flex w-full min-w-0 flex-col',
        getMessageAlignmentClass(alignment)
      )}
    >
      {hasSources && (
        <Sources>
          <SourcesTrigger count={sources.length} />
          <SourcesContent>
            {sources.map((source) => (
              <Source
                href={source.href}
                key={`${source.href}-${source.title}`}
                title={source.title}
              />
            ))}
          </SourcesContent>
        </Sources>
      )}

      {hasReasoning && (
        <Reasoning
          defaultOpen
          duration={message.reasoning?.duration}
          isStreaming={message.isReasoningStreaming}
        >
          <ReasoningTrigger />
          <ReasoningContent>{reasoningContent}</ReasoningContent>
        </Reasoning>
      )}

      {showLoader && (
        <div className='flex items-center gap-2 py-2'>
          <Loader />
          <Shimmer className='text-sm' duration={1}>
            {t('Responding...')}
          </Shimmer>
        </div>
      )}

      {isError && (
        <>
          <MessageError message={message} className='mb-2' />
          <MessageMetadata alignment={alignment} message={message} />
          {errorActions}
        </>
      )}

      {!isError && (showMessageContent || hasRichContent) && (
        <>
          {attachments.length > 0 && (
            <div className='mb-2 grid max-w-2xl gap-2 sm:grid-cols-2'>
              {attachments.map((attachment) => {
                if (attachment.kind === 'image') {
                  return (
                  <a
                    className='border-border/70 bg-muted/20 overflow-hidden rounded-lg border'
                    download={attachment.name}
                    href={attachment.dataUrl}
                    key={attachment.id}
                  >
                    <img
                      alt={attachment.name}
                      className='max-h-72 w-full object-contain'
                      src={attachment.dataUrl}
                    />
                    <div className='text-muted-foreground flex items-center justify-between gap-2 px-2.5 py-2 text-xs'>
                      <span className='truncate'>{attachment.name}</span>
                      <span className='shrink-0'>
                        {formatFileSize(attachment.size)}
                      </span>
                    </div>
                  </a>
                  )
                }

                if (attachment.kind === 'video') {
                  return (
                  <div
                    className='border-border/70 bg-muted/20 overflow-hidden rounded-lg border'
                    key={attachment.id}
                  >
                    <video
                      className='max-h-72 w-full bg-black object-contain'
                      controls
                      playsInline
                      preload='metadata'
                      src={attachment.dataUrl}
                    />
                    <div className='text-muted-foreground flex items-center justify-between gap-2 px-2.5 py-2 text-xs'>
                      <span className='truncate'>{attachment.name}</span>
                      <span className='shrink-0'>
                        {formatFileSize(attachment.size)}
                      </span>
                    </div>
                  </div>
                  )
                }

                return (
                  <a
                    className='border-border/70 bg-muted/25 hover:bg-muted/40 flex min-w-0 items-center gap-3 rounded-lg border px-3 py-2.5 transition-colors'
                    download={attachment.name}
                    href={attachment.dataUrl}
                    key={attachment.id}
                  >
                    <FileIcon className='text-muted-foreground size-5 shrink-0' />
                    <span className='min-w-0 flex-1'>
                      <span className='block truncate text-sm font-medium'>
                        {attachment.name}
                      </span>
                      <span className='text-muted-foreground text-xs'>
                        {formatFileSize(attachment.size)}
                      </span>
                    </span>
                    <DownloadIcon className='text-muted-foreground size-4 shrink-0' />
                  </a>
                )
              })}
            </div>
          )}

          {media.length > 0 && (
            <div className='mb-2 grid max-w-3xl gap-3'>
              {media.map((item) => (
                <div
                  className='border-border/70 bg-muted/15 overflow-hidden rounded-lg border'
                  key={`${item.kind}-${item.url}`}
                >
                  {item.kind === 'image' && (
                    <a href={item.url} rel='noreferrer' target='_blank'>
                      <img
                        alt={item.name || t('Generated image')}
                        className='max-h-[36rem] w-full object-contain'
                        src={item.url}
                      />
                    </a>
                  )}
                  {item.kind === 'video' && (
                    <video
                      className='max-h-[36rem] w-full bg-black'
                      controls
                      playsInline
                      preload='metadata'
                      src={item.url}
                    />
                  )}
                  {item.kind === 'audio' && (
                    <div className='p-3'>
                      <audio
                        className='w-full'
                        controls
                        preload='metadata'
                        src={item.url}
                      />
                    </div>
                  )}
                  <div className='border-border/60 flex items-center justify-end border-t px-3 py-2'>
                    <a
                      className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium'
                      download={item.name || undefined}
                      href={item.url}
                    >
                      <DownloadIcon size={14} />
                      {t('Download')}
                    </a>
                  </div>
                </div>
              ))}
            </div>
          )}

          {showMessageContent &&
            (isSourceVisible ? (
              <CodeBlock
                code={versionContent}
                className='my-0 group-[.is-assistant]:w-full group-[.is-assistant]:max-w-[78ch]'
                collapsedLines={24}
                defaultCollapsed={false}
                language='markdown'
                maxExpandedLines={48}
                showLineNumbers
                showToolbar
                title={t('Raw response')}
              >
                <CodeBlockCopyButton />
              </CodeBlock>
            ) : (
              <MessageContent
                variant='flat'
                className={cn(getMessageContentStyles())}
              >
                <Response final={isMessageFinal}>{displayContent}</Response>
              </MessageContent>
            ))}
          <MessageMetadata alignment={alignment} message={message} />
          {actions}
        </>
      )}
    </div>
  )
}
