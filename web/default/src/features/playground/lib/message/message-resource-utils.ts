import type { Message } from '../../types'

function getDisposableObjectUrls(messages: Message[]): Set<string> {
  const urls = new Set<string>()

  for (const message of messages) {
    for (const media of message.media ?? []) {
      if (media.revokeOnDispose && media.url.startsWith('blob:')) {
        urls.add(media.url)
      }
    }
  }

  return urls
}

export function revokeRemovedMessageResources(
  previousMessages: Message[],
  nextMessages: Message[]
): void {
  const previousUrls = getDisposableObjectUrls(previousMessages)
  const nextUrls = getDisposableObjectUrls(nextMessages)

  for (const url of previousUrls) {
    if (!nextUrls.has(url)) {
      URL.revokeObjectURL(url)
    }
  }
}

export function revokeMessageResources(messages: Message[]): void {
  for (const url of getDisposableObjectUrls(messages)) {
    URL.revokeObjectURL(url)
  }
}
