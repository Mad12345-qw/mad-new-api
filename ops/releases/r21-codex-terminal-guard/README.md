# MadAPI r21 Codex terminal guard production snapshot

This branch is an exact source snapshot of the release deployed on 2026-08-17.
It was created from the complete `r20i` production source, not reconstructed
from the default branch.

## Release identity

- Production image: `madapi-v3-single-native:20260817-r21-codex-terminal-guard`
- Production image ID: `sha256:5c053d5d8a65d90a0810da224b21194efa3761fc563aed568411a9eef0e51a68`
- Runtime binary SHA256: `30e42d31d9f452ffd4ec6f08d829318da8a81ed9a1b262ddc2020f4ee3140e44`
- Mother image: `madapi-v3-single-native:20260816-r20i-gpt-search-fold`
- Mother image ID: `sha256:2debacd0081d26c06c985d43eb9b6fc5fc4d87358d1ec6787b74a756a6094d25`
- Mother binary SHA256: `351e105f1cd53b19e80b841a8296fb3cc559f8c29f2b11444a866f5b288fe432`

## Scope of r21

The r21 delta is limited to the `/codex` internal Responses stream boundary:

- require a legal `response.completed`, `response.failed`, or
  `response.incomplete` terminal event for internal `/codex` streams;
- stop treating a terminal-free EOF as a complete Codex turn;
- emit `response.incomplete` with `adapter_eof` for clean early EOF;
- emit `response.failed` for explicit parsing or transport errors;
- retain the existing generic EOF behavior for ordinary `/v1` requests;
- record the terminal event in structured stream audit metadata.

No UI, installer, history recovery, image generation, Gemini request routing,
OAuth, pricing, channel selection, retry policy, concurrency limit, cache, or
database schema was changed by the r21 delta.

## Acceptance evidence

- Targeted packages passed: `relay/common`, `relay/helper`, `service`, and
  `controller`.
- 16 concurrent workers ran the four changed terminal scenarios 20 times each
  with zero failures.
- A real `/codex/v1/responses` request completed with exactly one
  `response.completed` event on the test server, production server, and new
  server.
- Production and new server both run the same image ID and binary SHA256.
- The candidate root filesystem matched r20i in all 4,294 entries after
  excluding `/new-api` and container-generated files.
- Homepage, Windows installer, macOS installer, and history recovery script
  hashes remained identical to r20i.

Rollback containers are intentionally server-local and are not part of this
source snapshot.
