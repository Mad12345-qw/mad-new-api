import type { PlaygroundModelKind } from '../../types'

const VIDEO_MODEL_PATTERN =
  /(?:^|[-_.])(video|veo|sora|seedance|kling|hailuo|wan2?\.?\d)(?:$|[-_.])/i
const TTS_MODEL_PATTERN = /(?:tts|text[-_.]?to[-_.]?speech|speechify)/i
const IMAGE_MODEL_PATTERN =
  /(?:gpt[-_.]?image|image[-_.]?\d|imagen|dall[-_.]?e|flux|ideogram|recraft|grok[-_.]?imagine[-_.]?image|nano[-_.]?banana|image[-_.]?preview)/i

export function getPlaygroundModelKind(model: string): PlaygroundModelKind {
  const normalized = model.trim()
  if (TTS_MODEL_PATTERN.test(normalized)) return 'tts'
  if (VIDEO_MODEL_PATTERN.test(normalized)) return 'video'
  if (IMAGE_MODEL_PATTERN.test(normalized)) return 'image'
  return 'chat'
}

export function isGeminiImageModel(model: string): boolean {
  return /gemini/i.test(model) && /image|imagen|banana/i.test(model)
}
