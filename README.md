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
- a versioned image compatibility service that preserves reliable `gpt-image-2` responses and converts Gemini text-to-image and multipart image-edit requests into the working multimodal chat route, while honoring `b64_json` and `url` client formats

GitHub Actions applies and verifies every patch, runs backend, frontend, and image compatibility tests, builds the Docker image away from the production server, and publishes stable release artifacts. No production credentials are stored in this repository.
