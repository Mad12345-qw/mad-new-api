# MadAPI R25 Codex Light Clone Production Manifest

This archive identifies the exact MadAPI main-site production build running on
2026-08-17. It contains source changes and reproducibility metadata only. It
does not contain databases, user data, API keys, OAuth files, SMTP credentials,
logs, or second-site data.

## Production identity

- Image tag: `madapi-v3-single-native:20260817-r25-codex-lightclone`
- Image ID: `sha256:7b62a6fb8c24fd3f4399c143f327afd757c2d305b3a37db912f151666b91197b`
- Main binary SHA256: `dd665738477c759b297aac7405379bcb3f406e61388d7a37e2b6fc79d82f584a`
- Source baseline: `bb9b0872` (`R22/R23 production URL-first 400k`)
- Runtime state at archival check: `running`, restart count `0`
- Docker CPU hard limit: none
- Docker memory hard limit: none
- Runtime settings: `GOGC=100`, `GOMEMLIMIT=512MiB`, `CountToken=false`

## R25 source delta

R25 is based directly on the complete R23 production source baseline. Its
source delta is intentionally limited to these three files:

- Modified `relay/responses_handler.go`
- Added `relay/responses_request_copy.go`
- Added `relay/responses_request_copy_test.go`

The change replaces the generic deep copy of large Responses requests with a
behavior-equivalent lightweight clone and performs top-level filtering directly
on the request DTO. The existing conversion chain and protocol behavior remain
in place.

Source file SHA256 values:

- `relay/responses_handler.go`: `3fb60d1ec453a42c1b19659f228deb23eda9733fb90c7b5a676ce5c0837e4452`
- `relay/responses_request_copy.go`: `0c68a3cc81faf97ef1a2665a36bdaa48b9a0ba8734385dd661070f1f8f64ed09`
- `relay/responses_request_copy_test.go`: `a68be29052be0111e1c31fcb5499714cc67d680e1ae567b4636b1c963cecd0d6`

## Preserved production surfaces

This archive does not intentionally change the UI, Windows or macOS installers,
OAuth/API login behavior, Gemini/Grok/Image2 generation paths, reference-image
ordering, size or quality forwarding, NewAPI billing, channel selection,
channel rotation, retry handling, or the protocol conversion matrix.

## Exact production binary asset

Release asset: `new-api-r25-codex-lightclone.zst`

- Compressed size: `40,029,877` bytes
- Compressed SHA256: `385926047cf591cf37fc1a5a6bb159c0200b8eaf171d73e908492805a132eea9`
- Decompressed binary SHA256: `dd665738477c759b297aac7405379bcb3f406e61388d7a37e2b6fc79d82f584a`

The decompressed checksum matches the binary in the production image recorded
above.

## Archive validation

- `go test ./relay`: passed with Go 1.26 on the test build server
- `git diff --check`: passed
- Source file hashes: matched the exact R25 build source
- Compressed release asset hash: matched the downloaded production artifact
