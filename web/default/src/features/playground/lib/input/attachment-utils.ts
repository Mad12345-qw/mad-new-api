import { nanoid } from 'nanoid'

import type { PlaygroundAttachment } from '../../types'

export const MAX_ATTACHMENT_COUNT = 4
export const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024
export const MAX_TOTAL_ATTACHMENT_BYTES = 20 * 1024 * 1024

function readFileAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('error', () =>
      reject(reader.error ?? new Error('File read failed'))
    )
    reader.addEventListener('load', () => resolve(String(reader.result ?? '')))
    reader.readAsDataURL(file)
  })
}

export async function fileToPlaygroundAttachment(
  file: File
): Promise<PlaygroundAttachment> {
  if (file.size > MAX_ATTACHMENT_BYTES) {
    throw new Error('Each attachment must be 10 MB or smaller')
  }
  const dataUrl = await readFileAsDataUrl(file)
  let kind: PlaygroundAttachment['kind'] = 'file'
  if (file.type.startsWith('image/')) {
    kind = 'image'
  } else if (file.type.startsWith('video/')) {
    kind = 'video'
  }
  return {
    id: nanoid(),
    kind,
    name: file.name || 'attachment',
    mimeType: file.type || 'application/octet-stream',
    size: file.size,
    dataUrl,
  }
}

export async function capturePlaygroundScreenshot(): Promise<File> {
  if (!navigator.mediaDevices?.getDisplayMedia) {
    throw new Error('Screen capture is not supported by this browser')
  }

  const stream = await navigator.mediaDevices.getDisplayMedia({ video: true })
  try {
    const video = document.createElement('video')
    video.srcObject = stream
    video.muted = true
    await video.play()

    const width = video.videoWidth
    const height = video.videoHeight
    if (!width || !height) throw new Error('Unable to read captured screen')

    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Unable to create screenshot')
    context.drawImage(video, 0, 0, width, height)

    const blob = await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob(
        (result) =>
          result ? resolve(result) : reject(new Error('Screenshot failed')),
        'image/png'
      )
    })
    return new File([blob], `screenshot-${Date.now()}.png`, {
      type: 'image/png',
    })
  } finally {
    stream.getTracks().forEach((track) => track.stop())
  }
}

export function dataUrlToBlob(dataUrl: string): Blob {
  const [header, payload] = dataUrl.split(',', 2)
  const mimeType =
    /data:([^;]+)/.exec(header)?.[1] || 'application/octet-stream'
  const binary = atob(payload || '')
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) {
    bytes[index] = binary.charCodeAt(index)
  }
  return new Blob([bytes], { type: mimeType })
}
