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
- streaming 4K image cache writes and zero-copy Base64 passthrough for Gemini responses, with separate Gemini and `gpt-image-2-4k` concurrency guards while ordinary `gpt-image-2` keeps its existing concurrency
- paid video idempotency through `Idempotency-Key`, recoverable pre-upstream task records, an authenticated request-ID lookup route, and upstream idempotency forwarding where the supplier supports it
- native xAI video task support for channel 40, including mainstream request normalization, single-image and up-to-seven-reference-image modes, duration and resolution validation, private result proxying, and official task polling
- an isolated Codex Desktop compatibility surface at `/codex/v1`: its catalog is synthesized from each token's current conversation-model access, while Responses requests are normalized once and delegated to New API's existing channel selection, mapping, retry, billing, and channel-type adaptors
- the MIT-licensed CPA HTTP Responses translation path is build-pinned behind `/codex/v1`, with CPA request/response regression coverage plus MadAPI-only normalization for namespace tools, nullable JSON Schemas, images, native search, routing, and billing
- the Codex catalog reads the token's current MadAPI text-model inventory on every Codex catalog request, enriches known models from the build-pinned CPA capability snapshot, gives newly added text models the validated high-end fallback, and keeps media models outside the Codex picker
- optional CPA-compatible Responses WebSocket code remains isolated for sandbox verification; the public one-command setup uses the proven HTTP Responses baseline and does not advertise or enable unverified WebSocket transport
- token-scoped Codex setup from the API key page, with automatic Windows/macOS detection, exact provider identity preservation, the proven direct bearer-token configuration, 360-second stream timeout, three request retries, CPA-style dynamic model discovery, pre/post Codex validation, and automatic rollback around one-command configuration

GitHub Actions applies and verifies every patch, runs backend, frontend, and image compatibility tests, builds the Docker image away from the production server, and publishes stable release artifacts. No production credentials are stored in this repository.

## Model detector edition

The detector is a plugin-style subsystem in the same repository. The normal Mad New API build receives only a versioned, service-token-protected channel contract and a `模型溯源检测` navigation entry; `build-detector-release.yml` independently builds the TraceGuard Python image, Compose file, database and release artifacts. Updating either image no longer rebuilds or replaces the other one.

TraceGuard can synchronize configured New API channels, expand multi-key channels into separately monitored encrypted credentials, apply model mappings before route selection, and retain removed or disabled channels as historical records. Manual and scheduled runs use the same evidence engine. A policy layer compares the configured expected source with the detected source and records the decision, threshold, evidence, notifications and any New API channel action in the report.

Automatic channel disabling is protected by two independent switches (global and per channel), a configurable minimum confidence that cannot be set below 90%, at least one successfully penetrated model probe, and an explicit incompatible-source matrix. Timeouts, rate limits, insufficient balance, 5xx responses, model self-report and other rewriteable single fields remain inconclusive and cannot disable a channel. Confirmed mismatches can be delivered by SMTP, generic webhook, Feishu bot or DingTalk bot.

The detector stores upstream keys encrypted at rest, runs a low-output protocol and contract suite, requires at least 80% probe coverage, and only reports an alternate channel when independent evidence reaches the channel-specific confidence threshold. Active model-output probes are disabled per upstream until an administrator explicitly enables them. Scheduled checks have a separate master switch and remain disabled by default until the operator enables them. Frontier model names are treated as unverified aliases unless stronger transport, contract, tokenizer, or calibrated reference evidence is present.

The detector accepts the existing same-origin New API session only after an internal `/api/user/self` check confirms role `10` or `100`. A separate detector token remains available as an emergency fallback; neither path exposes credentials to the browser application state.

Detector rules are data-driven in `server/model-detector/rules/default.json`. Each report preserves the rule version, redacted response shape, model inventory, SSE sequence, error contract, token fields, evidence strength, and raw-response SHA-256 without storing raw API keys.

The operator supplies only a third-party API base URL and key. The detector queries OpenAI-, Anthropic-, Gemini-, and New API-compatible inventories, keeps current GPT and Claude text models plus `gemini-3.6-flash` and `gpt-image-2`, and assigns a route per selected model. Claude is cross-checked through Anthropic Messages and exposed compatibility routes; current GPT models prefer Responses with guarded fallbacks; Gemini native contract probes still run when discovery advertises only an OpenAI-compatible route; `gpt-image-2` uses non-generation validation probes only against the Images contract.

Active detection is stored and reported per model rather than per model-list endpoint. Reports express a black-box observable chain such as outer New API gateway, protocol translation, probable terminal channel, and unknown intermediate/terminal segments. They only state a provable lower bound for relay hops because a layer that completely removes its fingerprints cannot be enumerated reliably.
