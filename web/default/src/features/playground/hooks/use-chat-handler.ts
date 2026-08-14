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
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  fetchVideoTask,
  sendChatCompletion,
  sendImageEdit,
  sendImageGeneration,
  sendSpeech,
  submitVideo,
} from '../api'
import { ERROR_MESSAGES } from '../constants'
import {
  applyStreamingChunk,
  buildChatCompletionPayload,
  updateAssistantMessageWithError,
  updateLastAssistantMessage,
  parseRequestErrorDetails,
  applyChatCompletionResponse,
  completeAssistantMessage,
  hasChatCompletionChoice,
  isAssistantMessageFinal,
  isAssistantMessagePending,
  dataUrlToBlob,
  getMessageContent,
  getPlaygroundModelKind,
  getVideoAspectRatio,
  isGrokImagineVideo15Model,
  isGrokImagineVideoModel,
  isGptImage2Model,
  isGeminiImageModel,
  isMikotoSeedanceModel,
  isSeedanceCf1080pModel,
  isSeedanceVideoModel,
  updateCurrentVersionContent,
} from '../lib'
import type {
  Message,
  PlaygroundConfig,
  ParameterEnabled,
  PlaygroundMedia,
  VideoTaskResponse,
} from '../types'
import { useStreamRequest } from './use-stream-request'

interface UseChatHandlerOptions {
  config: PlaygroundConfig
  parameterEnabled: ParameterEnabled
  onMessageUpdate: (updater: (prev: Message[]) => Message[]) => void
}

const KNOWN_ERROR_MESSAGES = new Set<string>(Object.values(ERROR_MESSAGES))
const STREAM_UPDATE_FLUSH_MS = 50
const VIDEO_POLL_INTERVAL_MS = 3000
const VIDEO_POLL_TIMEOUT_MS = 12 * 60 * 1000

type PendingStreamChunks = {
  content: string
  reasoning: string
}

function getVideoTaskErrorMessage(task: VideoTaskResponse): string {
  if (task.fail_reason) return task.fail_reason
  if (typeof task.error === 'string') return task.error
  if (
    task.error &&
    typeof task.error === 'object' &&
    'message' in task.error &&
    typeof task.error.message === 'string'
  ) {
    return task.error.message
  }
  return 'Video generation failed'
}

function getLastUserMessage(messages: Message[]): Message | undefined {
  return [...messages].reverse().find((message) => message.from === 'user')
}

function makeObjectUrl(dataUrl: string): string {
  return URL.createObjectURL(dataUrlToBlob(dataUrl))
}

function imageResponseMedia(response: {
  data?: Array<{ url?: string; b64_json?: string; revised_prompt?: string }>
}): { media: PlaygroundMedia; text: string } {
  const item = response.data?.[0]
  if (!item) throw new Error('Image generation returned no image')
  const text = item.revised_prompt || 'Image generated'
  if (item.url) {
    return { media: { kind: 'image', url: item.url }, text }
  }
  if (item.b64_json) {
    return {
      media: {
        kind: 'image',
        url: makeObjectUrl(`data:image/png;base64,${item.b64_json}`),
        mimeType: 'image/png',
        revokeOnDispose: true,
      },
      text,
    }
  }
  throw new Error('Image generation returned no usable image data')
}

function getGeminiImageSize(size: string): '1K' | '2K' | '4K' {
  const normalized = size.trim().toUpperCase()
  if (normalized === '4K' || normalized.includes('3840')) {
    return '4K'
  }
  if (normalized === '2K' || normalized.includes('2160')) {
    return '2K'
  }
  return '1K'
}

function waitForVideoPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const handleAbort = () => {
      window.clearTimeout(timeout)
      reject(new DOMException('Aborted', 'AbortError'))
    }
    const timeout = window.setTimeout(() => {
      signal.removeEventListener('abort', handleAbort)
      resolve()
    }, VIDEO_POLL_INTERVAL_MS)
    signal.addEventListener('abort', handleAbort, { once: true })
  })
}

function mergePendingStreamChunk(
  currentChunk: string,
  nextChunk: string
): string {
  if (!currentChunk || !nextChunk.startsWith(currentChunk)) {
    return currentChunk + nextChunk
  }

  return nextChunk
}

/**
 * Hook for handling chat message sending and receiving
 */
export function useChatHandler({
  config,
  parameterEnabled,
  onMessageUpdate,
}: UseChatHandlerOptions) {
  const { t } = useTranslation()
  const { sendStreamRequest, stopStream, isStreaming } = useStreamRequest()
  const [isRequesting, setIsRequesting] = useState(false)
  const abortControllerRef = useRef<AbortController | null>(null)
  const requestIdRef = useRef(0)
  const pendingStreamChunksRef = useRef<PendingStreamChunks>({
    content: '',
    reasoning: '',
  })
  const streamFlushTimerRef = useRef<number | null>(null)

  const flushStreamUpdates = useCallback(() => {
    if (streamFlushTimerRef.current !== null) {
      window.clearTimeout(streamFlushTimerRef.current)
      streamFlushTimerRef.current = null
    }

    const pendingChunks = pendingStreamChunksRef.current
    if (!pendingChunks.reasoning && !pendingChunks.content) {
      return
    }

    pendingStreamChunksRef.current = { content: '', reasoning: '' }
    onMessageUpdate((prev) =>
      updateLastAssistantMessage(prev, (message) => {
        let updatedMessage = message

        if (pendingChunks.reasoning) {
          updatedMessage = applyStreamingChunk(
            updatedMessage,
            'reasoning',
            pendingChunks.reasoning
          )
        }

        if (pendingChunks.content) {
          updatedMessage = applyStreamingChunk(
            updatedMessage,
            'content',
            pendingChunks.content
          )
        }

        return updatedMessage
      })
    )
  }, [onMessageUpdate])

  const scheduleStreamFlush = useCallback(() => {
    if (streamFlushTimerRef.current !== null) {
      return
    }

    streamFlushTimerRef.current = window.setTimeout(
      flushStreamUpdates,
      STREAM_UPDATE_FLUSH_MS
    )
  }, [flushStreamUpdates])

  useEffect(
    () => () => {
      if (streamFlushTimerRef.current !== null) {
        window.clearTimeout(streamFlushTimerRef.current)
      }
    },
    []
  )

  const getDisplayError = useCallback(
    (error: string) => {
      if (KNOWN_ERROR_MESSAGES.has(error)) {
        return t(error)
      }

      const connectionClosedSuffix = `: ${ERROR_MESSAGES.CONNECTION_CLOSED}`
      if (error.endsWith(connectionClosedSuffix)) {
        return `${error.slice(0, -ERROR_MESSAGES.CONNECTION_CLOSED.length)}${t(
          ERROR_MESSAGES.CONNECTION_CLOSED
        )}`
      }

      return error
    },
    [t]
  )

  // Handle stream update
  const handleStreamUpdate = useCallback(
    (type: 'reasoning' | 'content', chunk: string) => {
      pendingStreamChunksRef.current[type] = mergePendingStreamChunk(
        pendingStreamChunksRef.current[type],
        chunk
      )
      scheduleStreamFlush()
    },
    [scheduleStreamFlush]
  )

  // Handle stream complete
  const handleStreamComplete = useCallback(() => {
    flushStreamUpdates()
    setIsRequesting(false)
    onMessageUpdate((prev) =>
      updateLastAssistantMessage(prev, (message) =>
        isAssistantMessageFinal(message)
          ? message
          : completeAssistantMessage(message)
      )
    )
  }, [flushStreamUpdates, onMessageUpdate])

  // Handle stream error
  const handleStreamError = useCallback(
    (error: string, errorCode?: string) => {
      flushStreamUpdates()
      setIsRequesting(false)
      const displayError = getDisplayError(error)
      toast.error(displayError)
      const errorTitle = t(ERROR_MESSAGES.API_REQUEST_ERROR)
      onMessageUpdate((prev) =>
        updateAssistantMessageWithError(
          prev,
          displayError,
          errorCode,
          errorTitle
        )
      )
    },
    [flushStreamUpdates, getDisplayError, onMessageUpdate, t]
  )

  // Send streaming chat request
  const sendStreamingChat = useCallback(
    (messages: Message[]) => {
      setIsRequesting(true)
      const payload = buildChatCompletionPayload(
        messages,
        config,
        parameterEnabled
      )
      sendStreamRequest(
        payload,
        handleStreamUpdate,
        handleStreamComplete,
        handleStreamError
      )
    },
    [
      config,
      parameterEnabled,
      sendStreamRequest,
      handleStreamUpdate,
      handleStreamComplete,
      handleStreamError,
    ]
  )

  // Send non-streaming chat request
  const sendNonStreamingChat = useCallback(
    async (messages: Message[]) => {
      const payload = buildChatCompletionPayload(
        messages,
        config,
        parameterEnabled
      )
      const requestId = requestIdRef.current + 1
      const abortController = new AbortController()

      requestIdRef.current = requestId
      abortControllerRef.current = abortController

      try {
        setIsRequesting(true)
        const response = await sendChatCompletion(
          payload,
          abortController.signal
        )
        if (abortController.signal.aborted) return

        if (!hasChatCompletionChoice(response)) {
          handleStreamError(ERROR_MESSAGES.API_REQUEST_ERROR)
          return
        }

        onMessageUpdate((prev) =>
          updateLastAssistantMessage(prev, (message) => {
            const updatedMessage = applyChatCompletionResponse(
              message,
              response
            )

            return updatedMessage ?? message
          })
        )
      } catch (error: unknown) {
        if (abortController.signal.aborted) return

        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        handleStreamError(errorMessage, errorCode)
      } finally {
        if (requestIdRef.current === requestId) {
          abortControllerRef.current = null
          setIsRequesting(false)
        }
      }
    },
    [config, parameterEnabled, onMessageUpdate, handleStreamError]
  )

  const completeMediaMessage = useCallback(
    (text: string, media: PlaygroundMedia[]) => {
      onMessageUpdate((prev) =>
        updateLastAssistantMessage(prev, (message) =>
          completeAssistantMessage({
            ...updateCurrentVersionContent(message, text),
            media,
          })
        )
      )
    },
    [onMessageUpdate]
  )

  const sendSpecializedRequest = useCallback(
    async (messages: Message[]) => {
      const requestId = requestIdRef.current + 1
      const abortController = new AbortController()
      requestIdRef.current = requestId
      abortControllerRef.current = abortController
      setIsRequesting(true)

      try {
        const userMessage = getLastUserMessage(messages)
        const prompt = userMessage ? getMessageContent(userMessage) : ''
        const attachments = userMessage?.attachments ?? []
        const imageAttachment = attachments.find(
          (attachment) => attachment.kind === 'image'
        )
        const modelKind = getPlaygroundModelKind(config.model)

        if (modelKind === 'image') {
          if (isGeminiImageModel(config.model)) {
            if (attachments.length === 0) {
              const imageSize = getGeminiImageSize(config.imageSize)
              const result = imageResponseMedia(
                await sendImageGeneration(
                  {
                    model: config.model,
                    group: config.group,
                    prompt,
                    image_size: imageSize,
                    size: imageSize,
                    response_format: 'url',
                    n: 1,
                  },
                  abortController.signal
                )
              )
              completeMediaMessage(result.text, [result.media])
            } else {
              const imageAttachments = attachments.filter(
                (attachment) => attachment.kind === 'image'
              )
              if (imageAttachments.length === 0) {
                throw new Error('Image editing requires an image attachment')
              }
              const payload = new FormData()
              payload.append('model', config.model)
              payload.append('group', config.group)
              payload.append('prompt', prompt || 'Edit this image')
              payload.append(
                'image_size',
                getGeminiImageSize(config.imageSize)
              )
              payload.append('response_format', 'url')
              payload.append('n', '1')
              for (const attachment of imageAttachments) {
                payload.append(
                  'image',
                  dataUrlToBlob(attachment.dataUrl),
                  attachment.name
                )
              }
              const result = imageResponseMedia(
                await sendImageEdit(payload, abortController.signal)
              )
              completeMediaMessage(result.text, [result.media])
            }
          } else if (imageAttachment) {
            const payload = new FormData()
            payload.append('model', config.model)
            payload.append('group', config.group)
            payload.append('prompt', prompt || 'Edit this image')
            payload.append('size', config.imageSize)
            if (isGptImage2Model(config.model)) {
              payload.append('response_format', config.imageResponseFormat)
            }
            if (config.imageQuality !== 'auto') {
              payload.append('quality', config.imageQuality)
            }
            payload.append('n', '1')
            payload.append(
              'image',
              dataUrlToBlob(imageAttachment.dataUrl),
              imageAttachment.name
            )
            const result = imageResponseMedia(
              await sendImageEdit(payload, abortController.signal)
            )
            completeMediaMessage(result.text, [result.media])
          } else {
            const imagePayload: Record<string, unknown> = {
              model: config.model,
              group: config.group,
              prompt,
              size: config.imageSize,
              n: 1,
            }
            if (isGptImage2Model(config.model)) {
              imagePayload.response_format = config.imageResponseFormat
            }
            if (config.imageQuality !== 'auto') {
              imagePayload.quality = config.imageQuality
            }
            const result = imageResponseMedia(
              await sendImageGeneration(imagePayload, abortController.signal)
            )
            completeMediaMessage(result.text, [result.media])
          }
          return
        }

        if (modelKind === 'tts') {
          const audio = await sendSpeech(
            {
              model: config.model,
              group: config.group,
              input: prompt,
              voice: config.ttsVoice,
              response_format: config.ttsFormat,
            },
            abortController.signal
          )
          const audioUrl = URL.createObjectURL(audio)
          completeMediaMessage(t('Speech generated'), [
            {
              kind: 'audio',
              url: audioUrl,
              mimeType: audio.type,
              name: `speech.${config.ttsFormat}`,
              revokeOnDispose: true,
            },
          ])
          return
        }

        if (modelKind === 'video') {
          let payload: FormData | Record<string, unknown>
          if (isMikotoSeedanceModel(config.model)) {
            const duration = Math.max(4, Math.min(15, config.videoSeconds))
            const imageAttachments = attachments.filter(
              (attachment) => attachment.kind === 'image'
            )
            const videoAttachments = attachments.filter(
              (attachment) => attachment.kind === 'video'
            )
            const audioAttachments = attachments.filter(
              (attachment) => attachment.mimeType.startsWith('audio/')
            )
            payload = {
              model: config.model,
              group: config.group,
              prompt,
              duration,
              aspect_ratio: getVideoAspectRatio(config.videoSize),
              generate_audio: true,
            }
            if (imageAttachments.length > 0) {
              payload.images = imageAttachments
                .slice(0, 9)
                .map((attachment) => attachment.dataUrl)
              payload.reference_mode =
                imageAttachments.length >= 3 ? 'media' : 'frame'
            }
            if (videoAttachments.length > 0) {
              payload.referenceVideos = videoAttachments
                .slice(0, 3)
                .map((attachment) => attachment.dataUrl)
            }
            if (audioAttachments.length > 0) {
              payload.referenceAudios = audioAttachments
                .slice(0, 3)
                .map((attachment) => attachment.dataUrl)
            }
          } else if (isSeedanceVideoModel(config.model)) {
            const duration = Math.max(4, Math.min(15, config.videoSeconds))
            const imageAttachments = attachments.filter(
              (attachment) => attachment.kind === 'image'
            )
            payload = {
              model: config.model,
              group: config.group,
              prompt,
              duration,
              aspect_ratio: getVideoAspectRatio(config.videoSize),
              audio: true,
            }
            if (!isSeedanceCf1080pModel(config.model)) {
              payload.resolution = '720p'
            }
            if (imageAttachments[0]) {
              payload.image_url = imageAttachments[0].dataUrl
            }
            if (imageAttachments.length > 1) {
              payload.reference_image_urls = imageAttachments
                .slice(1, 4)
                .map((attachment) => attachment.dataUrl)
            }
          } else if (isGrokImagineVideo15Model(config.model)) {
            if (!imageAttachment) {
              throw new Error(
                t('Grok Imagine Video 1.5 requires an uploaded image')
              )
            }

            const [width = 1280, height = 720] = config.videoSize
              .split('x')
              .map(Number)
            payload = {
              model: config.model,
              group: config.group,
              prompt: prompt || 'Animate this image naturally',
              image: imageAttachment.dataUrl,
              duration: Math.max(1, Math.min(15, config.videoSeconds)),
              aspect_ratio: width < height ? '9:16' : '16:9',
              resolution: Math.max(width, height) >= 1920 ? '1080p' : '720p',
            }
          } else if (isGrokImagineVideoModel(config.model)) {
            payload = {
              model: config.model,
              group: config.group,
              prompt,
              seconds: String(config.videoSeconds),
              size: config.videoSize,
            }
            if (imageAttachment) {
              payload.input_reference = imageAttachment.dataUrl
            }
          } else {
            const formData = new FormData()
            formData.append('model', config.model)
            formData.append('group', config.group)
            formData.append('prompt', prompt)
            formData.append('seconds', String(config.videoSeconds))
            formData.append('size', config.videoSize)
            if (imageAttachment) {
              formData.append(
                'input_reference',
                dataUrlToBlob(imageAttachment.dataUrl),
                imageAttachment.name
              )
            }
            payload = formData
          }

          const created = await submitVideo(payload, abortController.signal)
          const taskId = created.id || created.task_id
          if (!taskId) throw new Error('Video request returned no task ID')

          const startedAt = Date.now()
          let task = created
          while (Date.now() - startedAt < VIDEO_POLL_TIMEOUT_MS) {
            const status = String(task.status || '').toLowerCase()
            if (status === 'completed' || status === 'succeeded') {
              completeMediaMessage(t('Video generated'), [
                {
                  kind: 'video',
                  url: `/v1/videos/${encodeURIComponent(taskId)}/content`,
                  name: `${config.model}-${taskId}.mp4`,
                },
              ])
              return
            }
            if (
              ['failed', 'failure', 'cancelled', 'canceled'].includes(status) ||
              task.error
            ) {
              throw new Error(getVideoTaskErrorMessage(task))
            }
            await waitForVideoPoll(abortController.signal)
            task = await fetchVideoTask(taskId, abortController.signal)
          }
          throw new Error('Video generation timed out')
        }
      } catch (error: unknown) {
        if (abortController.signal.aborted) return
        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        handleStreamError(errorMessage, errorCode)
      } finally {
        if (requestIdRef.current === requestId) {
          abortControllerRef.current = null
          setIsRequesting(false)
        }
      }
    },
    [config, completeMediaMessage, handleStreamError, t]
  )

  // Send chat request (stream or non-stream based on config)
  const sendChat = useCallback(
    (messages: Message[]) => {
      if (getPlaygroundModelKind(config.model) !== 'chat') {
        void sendSpecializedRequest(messages)
        return
      }
      if (config.stream) {
        sendStreamingChat(messages)
      } else {
        sendNonStreamingChat(messages)
      }
    },
    [
      config.model,
      config.stream,
      sendStreamingChat,
      sendNonStreamingChat,
      sendSpecializedRequest,
    ]
  )

  // Stop generation
  const stopGeneration = useCallback(() => {
    stopStream()
    flushStreamUpdates()
    abortControllerRef.current?.abort()
    abortControllerRef.current = null
    setIsRequesting(false)
    onMessageUpdate((prev) =>
      updateLastAssistantMessage(prev, (message) =>
        isAssistantMessagePending(message)
          ? completeAssistantMessage(message)
          : message
      )
    )
  }, [stopStream, flushStreamUpdates, onMessageUpdate])

  return {
    sendChat,
    stopGeneration,
    isGenerating: isStreaming || isRequesting,
  }
}
