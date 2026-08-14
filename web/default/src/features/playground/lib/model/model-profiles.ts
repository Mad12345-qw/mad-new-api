import type { PlaygroundModelKind } from "../../types";
import { getPlaygroundModelKind } from "./model-capabilities";

export type PlaygroundSelectOption = {
  label: string;
  value: string;
};

export type PlaygroundModelProfile = {
  badge: string;
  description: string;
  endpoint: string;
  kind: PlaygroundModelKind;
  requestHints?: PlaygroundSelectOption[];
  imageSizes?: PlaygroundSelectOption[];
  imageResponseFormats?: PlaygroundSelectOption[];
  defaultImageQuality?: string;
  defaultImageResponseFormat?: "b64_json" | "url";
  defaultImageSize?: string;
  defaultTtsFormat?: string;
  defaultTtsVoice?: string;
  ttsFormats?: PlaygroundSelectOption[];
  ttsVoiceLabel?: string;
  ttsVoicePlaceholder?: string;
  videoDurationMax?: number;
  videoDurationMin?: number;
  videoSizes?: PlaygroundSelectOption[];
  defaultVideoSeconds?: number;
  defaultVideoSize?: string;
};

export const MOSS_TTS_DEFAULT_VOICE_ID = "86a2ab25-a94b-4feb-af3b-c853629f067f";

const STANDARD_IMAGE_SIZES: PlaygroundSelectOption[] = [
  { value: "1024x1024", label: "1:1 | 1024x1024" },
  { value: "1536x1024", label: "3:2 | 1536x1024" },
  { value: "1024x1536", label: "2:3 | 1024x1536" },
];

const GEMINI_IMAGE_RESOLUTIONS: PlaygroundSelectOption[] = [
  { value: "4K", label: "4K" },
];

const GPT_IMAGE_2_4K_SIZES: PlaygroundSelectOption[] = [
  { value: "3840x2160", label: "16:9 | 3840x2160 | UHD 4K" },
  {
    value: "1024x1024",
    label: "1:1 | Request 1024x1024 | Output 2880x2880",
  },
];

const GPT_IMAGE_2_RESPONSE_FORMATS: PlaygroundSelectOption[] = [
  { value: "b64_json", label: "Base64 (recommended)" },
  { value: "url", label: "Image URL" },
];

const STANDARD_TTS_FORMATS: PlaygroundSelectOption[] = [
  { value: "mp3", label: "MP3" },
  { value: "wav", label: "WAV" },
  { value: "opus", label: "Opus" },
  { value: "flac", label: "FLAC" },
];

const SEEDANCE_720P_SIZES: PlaygroundSelectOption[] = [
  { value: "1280x720", label: "16:9 | HD 720p" },
  { value: "720x1280", label: "9:16 | HD 720p" },
  { value: "720x720", label: "1:1 | HD 720p" },
  { value: "1680x720", label: "21:9 | HD 720p" },
  { value: "720x960", label: "3:4 | HD 720p" },
  { value: "960x720", label: "4:3 | HD 720p" },
];

const SEEDANCE_CF_1080P_SIZES: PlaygroundSelectOption[] = [
  { value: "1920x1080", label: "16:9 | CF 1080p" },
  { value: "1080x1920", label: "9:16 | CF 1080p" },
  { value: "1080x1080", label: "1:1 | CF 1080p" },
  { value: "2520x1080", label: "21:9 | CF 1080p" },
  { value: "1080x1440", label: "3:4 | CF 1080p" },
  { value: "1440x1080", label: "4:3 | CF 1080p" },
];

const MIKOTO_SEEDANCE_720P_SIZES: PlaygroundSelectOption[] = [
  { value: "1280x720", label: "16:9 | HD 720p" },
  { value: "720x1280", label: "9:16 | HD 720p" },
  { value: "720x720", label: "1:1 | HD 720p" },
  { value: "720x960", label: "3:4 | HD 720p" },
  { value: "960x720", label: "4:3 | HD 720p" },
];

const MIKOTO_SEEDANCE_1080P_SIZES: PlaygroundSelectOption[] = [
  { value: "1920x1080", label: "16:9 | Full HD 1080p" },
  { value: "1080x1920", label: "9:16 | Full HD 1080p" },
  { value: "1080x1080", label: "1:1 | Full HD 1080p" },
  { value: "1080x1440", label: "3:4 | Full HD 1080p" },
  { value: "1440x1080", label: "4:3 | Full HD 1080p" },
];

export function isMossTtsModel(model: string): boolean {
  return /(?:^|[-_.])(moss[-_.]?tts|speechify[-_.]?tts)(?:$|[-_.])/i.test(
    model,
  );
}

export function isGptImage2FourKModel(model: string): boolean {
  return /gpt[-_.]?image[-_.]?2[-_.]?4k/i.test(model);
}

export function isGptImage2Model(model: string): boolean {
  return /gpt[-_.]?image[-_.]?2(?:[-_.]?4k)?/i.test(model);
}

export function isGeminiFourKImageModel(model: string): boolean {
  const normalized = model.trim().toLowerCase();
  return (
    normalized === "gemini-3.1-flash-image-preview" ||
    normalized === "gemini-3-pro-image-preview"
  );
}

export function isGrokImagineVideo15Model(model: string): boolean {
  return /^grok-imagine-video-1\.5(?:-preview|-\d{4}-\d{2}-\d{2})?$/i.test(
    model.trim(),
  );
}

export function isGrokImagineVideoModel(model: string): boolean {
  const normalized = model.trim().toLowerCase();
  return (
    normalized === "grok-imagine-video" ||
    isGrokImagineVideo15Model(normalized)
  );
}

export function isSeedanceVideoModel(model: string): boolean {
  switch (model.trim().toLowerCase()) {
    case "seedance-2.0":
    case "seedance-2.0-fast":
    case "doubao-seedance-2.0-720p":
    case "doubao-seedance-2.0-cf-1080p":
    case "seedance-2.0-1080p":
    case "seedance-2.0-720p":
    case "seedance-fast-720p":
      return true;
    default:
      return false;
  }
}

export function isMikotoSeedanceModel(model: string): boolean {
  switch (model.trim().toLowerCase()) {
    case "seedance-2.0-1080p":
    case "seedance-2.0-720p":
    case "seedance-fast-720p":
      return true;
    default:
      return false;
  }
}

function getMikotoSeedancePrice(model: string): string {
  switch (model.trim().toLowerCase()) {
    case "seedance-2.0-1080p":
      return "¥0.60/second";
    case "seedance-2.0-720p":
      return "¥0.44/second";
    case "seedance-fast-720p":
      return "¥0.40/second";
    default:
      return "";
  }
}

export function isSeedanceCf1080pModel(model: string): boolean {
  return model.trim().toLowerCase() === "doubao-seedance-2.0-cf-1080p";
}

export function getVideoAspectRatio(size: string): string {
  const [width, height] = size.split("x").map(Number);
  if (!width || !height) return "16:9";

  const ratio = width / height;
  const supported = [
    { value: "21:9", ratio: 21 / 9 },
    { value: "16:9", ratio: 16 / 9 },
    { value: "4:3", ratio: 4 / 3 },
    { value: "1:1", ratio: 1 },
    { value: "3:4", ratio: 3 / 4 },
    { value: "9:16", ratio: 9 / 16 },
  ];
  return supported.reduce((closest, candidate) =>
    Math.abs(candidate.ratio - ratio) < Math.abs(closest.ratio - ratio)
      ? candidate
      : closest,
  ).value;
}

export function getPlaygroundModelProfile(
  model: string,
): PlaygroundModelProfile {
  const kind = getPlaygroundModelKind(model);

  if (isGptImage2FourKModel(model)) {
    return {
      badge: "4K Image",
      description:
        "Native 4K image generation verified at 3840x2160; enter a prompt and generate the result directly.",
      endpoint: "/v1/images/generations",
      kind,
      requestHints: [
        { label: "model", value: model },
        { label: "size", value: "3840x2160" },
        { label: "n", value: "1" },
        { label: "response_format", value: "b64_json" },
      ],
      imageSizes: GPT_IMAGE_2_4K_SIZES,
      imageResponseFormats: GPT_IMAGE_2_RESPONSE_FORMATS,
      defaultImageSize: "3840x2160",
      defaultImageQuality: "hd",
      defaultImageResponseFormat: "b64_json",
    };
  }

  if (isGeminiFourKImageModel(model)) {
    return {
      badge: "4K Image",
      description:
        "Gemini native 4K image generation; the playground always requests 4K output.",
      endpoint: "/v1/images/generations",
      kind,
      requestHints: [
        { label: "model", value: model },
        { label: "image_size", value: "4K" },
      ],
      imageSizes: GEMINI_IMAGE_RESOLUTIONS,
      defaultImageSize: "4K",
      defaultImageQuality: "hd",
    };
  }

  if (kind === "image") {
    const image2 = isGptImage2Model(model);
    return {
      badge: "Image",
      description:
        "Generate images from text or upload an image to test compatible editing models.",
      endpoint: "/v1/images/generations",
      kind,
      requestHints: image2
        ? [
            { label: "model", value: model },
            { label: "response_format", value: "b64_json" },
          ]
        : undefined,
      imageSizes: STANDARD_IMAGE_SIZES,
      imageResponseFormats: image2 ? GPT_IMAGE_2_RESPONSE_FORMATS : undefined,
      defaultImageSize: "1024x1024",
      defaultImageQuality: "hd",
      defaultImageResponseFormat: image2 ? "b64_json" : undefined,
    };
  }

  if (isMossTtsModel(model)) {
    return {
      badge: "Voice",
      description:
        "MOSS speech synthesis using the verified OpenAI-compatible audio request.",
      endpoint: "/v1/audio/speech",
      kind,
      requestHints: [
        { label: "model", value: model },
        { label: "input", value: "Text" },
        { label: "voice", value: "Voice ID" },
        { label: "response_format", value: "mp3" },
      ],
      defaultTtsVoice: MOSS_TTS_DEFAULT_VOICE_ID,
      defaultTtsFormat: "mp3",
      ttsFormats: [{ value: "mp3", label: "MP3" }],
      ttsVoiceLabel: "Voice ID",
      ttsVoicePlaceholder: MOSS_TTS_DEFAULT_VOICE_ID,
    };
  }

  if (kind === "tts") {
    return {
      badge: "Voice",
      description:
        "Enter text and choose a voice and audio format supported by the model.",
      endpoint: "/v1/audio/speech",
      kind,
      defaultTtsVoice: "alloy",
      defaultTtsFormat: "mp3",
      ttsFormats: STANDARD_TTS_FORMATS,
      ttsVoiceLabel: "Voice",
      ttsVoicePlaceholder: "alloy / voice ID",
    };
  }

  if (isGrokImagineVideo15Model(model)) {
    return {
      badge: "Image to Video",
      description:
        "Grok Imagine Video 1.5 requires one source image and supports 1-15 second output at 720p or 1080p.",
      endpoint: "/v1/videos",
      kind,
      requestHints: [
        { label: "model", value: model },
        { label: "image", value: "Uploaded image" },
        { label: "duration", value: "1-15 seconds" },
        { label: "resolution", value: "720p / 1080p" },
      ],
      videoDurationMax: 15,
      videoDurationMin: 1,
    };
  }

  if (isSeedanceVideoModel(model)) {
	if (isMikotoSeedanceModel(model)) {
	  const is1080p = model.trim().toLowerCase() === "seedance-2.0-1080p";
	  const price = getMikotoSeedancePrice(model);
	  return {
		badge: "Seedance Video",
		description: `Seedance 2.0 ${is1080p ? "1080p" : "720p"}; ${price}, billed by requested duration. Supports text, images, reference video and reference audio for 4-15 second generation.`,
		endpoint: "/v1/videos",
		kind,
		requestHints: [
		  { label: "model", value: model },
		  { label: "price", value: price },
		  { label: "duration", value: "4-15 seconds" },
		  { label: "aspect_ratio", value: "16:9 / 9:16 / 1:1 / 4:3 / 3:4" },
		  { label: "request", value: "JSON: model + prompt + duration + aspect_ratio" },
		],
		videoDurationMax: 15,
		videoDurationMin: 4,
		videoSizes: is1080p
		  ? MIKOTO_SEEDANCE_1080P_SIZES
		  : MIKOTO_SEEDANCE_720P_SIZES,
		defaultVideoSeconds: 4,
		defaultVideoSize: is1080p ? "1920x1080" : "1280x720",
	  };
	}
    const cf1080p = isSeedanceCf1080pModel(model);
    return {
      badge: "Seedance Video",
      description: cf1080p
        ? "Seedance 2.0 CF uses the provider's fixed 1080p output and supports 4-15 second text or image-to-video requests."
        : "Seedance 2.0 generates HD 720p video from text or up to four reference images, with a supported duration of 4-15 seconds.",
      endpoint: "/v1/videos",
      kind,
      requestHints: [
        { label: "model", value: model },
        { label: "duration", value: "4-15 seconds" },
        { label: "aspect_ratio", value: "Selected ratio" },
        {
          label: "resolution",
          value: cf1080p ? "CF 1080p (fixed)" : "720p",
        },
      ],
      videoDurationMax: 15,
      videoDurationMin: 4,
      videoSizes: cf1080p ? SEEDANCE_CF_1080P_SIZES : SEEDANCE_720P_SIZES,
      defaultVideoSeconds: 4,
      defaultVideoSize: cf1080p ? "1920x1080" : "1280x720",
    };
  }

  if (kind === "video") {
    return {
      badge: "Video",
      description:
        "Generate video from text or upload an image for compatible image-to-video models.",
      endpoint: "/v1/videos",
      kind,
    };
  }

  return {
    badge: "Chat",
    description:
      "Chat with image and file understanding; enable search for compatible web-enabled models.",
    endpoint: "/v1/chat/completions",
    kind,
  };
}
