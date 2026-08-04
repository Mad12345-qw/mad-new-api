# MadAPI 2.0 Archive

This release archives the current production MadAPI platform as a single
recoverable baseline.

## Codex Desktop

- Token-scoped Codex catalogs for OAuth and API Key sessions.
- Native CPA Responses execution, including tools, streaming, search events,
  compaction continuation, WebSocket support, retries, and error recovery.
- Complete MadAPI authentication, billing, channel selection, and settlement
  remain in the New API service layer.
- Windows and macOS setup paths preserve existing Codex login state and local
  configuration outside the MadAPI provider block.
- Local history recovery migrates legacy desktop conversations to the active
  MadAPI provider without changing login state, API keys, models, or projects.

## Product And Billing

- Dynamic model catalogs and verified model metadata for the Codex picker.
- Unified playground support for chat, media, files, search, audio, and video.
- Task idempotency, recoverable task records, media result delivery, and
  compatibility handling for image and video providers.
- Backup-first release automation with health checks and rollback artifacts.

## 2.0 Addition

- The About page now adds a small copy action to every safe HTML code block,
  including normal and isolated content, allowing Windows and macOS recovery
  commands to be copied without selecting the full script manually.
- The standard Windows and macOS Codex installers now include local history
  recovery: they close Codex first, back up local index and project metadata,
  rebuild the conversation index from existing rollout files, and restore
  project ownership only for local folders that still exist.

## Release Baseline

- MadAPI configuration repository commit: this release commit.
- Upstream New API pin: `5a6c53d4966b2e34690ab49f3dd19be01c88fdbe`.
- CPA runtime pin: see `guards/cpa-upstream-commit.txt`.
