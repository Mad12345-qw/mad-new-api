# Mad New API

Custom build pipeline for Mad API.

This repository applies a small backend patch to the upstream New API source:

- four-digit numeric email verification codes
- a branded, mobile-friendly verification email template
- fixed-price task models charge exactly once per request
- fixed-price audio models preserve `ModelPrice` during settlement
- a model-aware playground for chat, image, video, file, search, and TTS testing
- verified presets for `gpt-image-2-4k` and MOSS/Speechify TTS
- browser-memory attachment and media cleanup without a server upload directory
- a versioned media compatibility service that normalizes mainstream OpenAI, New API, Gemini, and Seedance/Volc image and video request shapes into the site's verified canonical routes, while honoring image URL/Base64 responses and asynchronous video task polling
- isolated media resource controls so large Gemini image responses cannot consume the New API container's memory budget, plus route-level handling that prevents endpoint-fallback clients from submitting the same video through multiple aliases

GitHub Actions applies and verifies every patch, runs backend, frontend, and image compatibility tests, builds the Docker image away from the production server, and publishes stable release artifacts. No production credentials are stored in this repository.

## Model detector edition

`build-detector-release.yml` builds an isolated deployment edition from the same pinned New API baseline. It deliberately excludes the old site logo, avatar, promotion, Telegram, email-branding, and AdForge patches, then adds a same-origin `模型真伪检测` entry backed by a separate detector container.

The detector stores upstream keys encrypted at rest, runs a six-request non-billing protocol suite by default, requires at least 80% probe coverage, and only reports an alternate channel when two independent evidence categories reach the confidence threshold. Active model-output probes are disabled per upstream until an administrator explicitly enables them. Fable 5, Opus 5, and GPT-5.6-sol names are treated as unverified aliases unless a trusted paired reference and stronger evidence are present.

The detector accepts the existing same-origin New API session only after an internal `/api/user/self` check confirms role `10` or `100`. A separate detector token remains available as an emergency fallback; neither path exposes credentials to the browser application state.

Detector rules are data-driven in `server/model-detector/rules/default.json`. Each report preserves the rule version, redacted response shape, model inventory, SSE sequence, error contract, token fields, evidence strength, and raw-response SHA-256 without storing raw API keys.

The operator now supplies only a third-party API base URL and key. The detector safely queries OpenAI, Anthropic, and Gemini model-list contracts, filters the result to mainstream text models from those three families, and assigns a route per selected model from advertised endpoint capabilities. Claude can use Anthropic Messages or an explicitly reported OpenAI translation route; current OpenAI reasoning/coding models prefer Responses with a guarded Chat fallback; Gemini prefers native `generateContent` when advertised.

Active detection is stored and reported per model rather than per model-list endpoint. Reports express a black-box observable chain such as outer New API gateway, protocol translation, probable terminal channel, and unknown intermediate/terminal segments. They only state a provable lower bound for relay hops because a layer that completely removes its fingerprints cannot be enumerated reliably.
