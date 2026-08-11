# MadAPI release and rollback gate

This directory contains release infrastructure only. It does not modify NewAPI
relay behavior or CPA protocol behavior.

## Codex route boundary

The public `/codex/v1` route and ordinary `/v1` route share one NewAPI instance
and one billing database. NewAPI authenticates the user token first. A private
ingress header then pins only `/codex/v1` to the `codex` channel group, whose
channels point to an unmodified official CPA runtime. Ordinary `/v1` clears the
header and keeps the token's normal channel group.

The official CPA runtime must use:

```yaml
disable-image-generation: "passthrough"
```

This keeps `/v1/images/generations` and `/v1/images/edits` enabled while leaving
non-image Responses tool lists unchanged. Codex supplies the `image_gen`
namespace, then its image tool calls the CPA Image API with `gpt-image-2`.
Do not use `false` for this deployment: that official default injects the hosted
`image_generation` tool into ordinary Responses requests. Validate the runtime
configuration before every switch:

```sh
ops/release/verify-cpa-config.sh /opt/madapi/runtime/cpa-config.yaml
```

Generate one random 64-character hexadecimal secret, set it as both
`TRUSTED_ROUTE_TOKEN` in NewAPI and the input to `render-codex-route.sh`, and set
`TRUSTED_ROUTE_GROUP=codex` in NewAPI. The rendered file contains the secret and
must remain owner-readable only. Never expose or accept this header from a
public client.

```sh
export TRUSTED_ROUTE_TOKEN="$(openssl rand -hex 32)"
ops/release/render-codex-route.sh \
  http://127.0.0.1:3001 \
  /etc/nginx/snippets/madapi-codex-route.conf
```

## Required release shape

- Keep the currently deployed and candidate images running on separate ports.
- Store immutable image IDs, image references, Git commit, CPA configuration,
  edge configuration, deployment environment, and a consistent PostgreSQL dump.
- Switch the Nginx include through an atomic symbolic-link replacement.
- Validate the candidate directly before switching.
- Validate the public edge after reloading Nginx.
- Restore the previous include automatically if edge validation fails.
- Keep the previous container and image until the observation window closes.

## Test-server rehearsal

The rehearsal exercises these transitions:

1. baseline -> candidate
2. candidate -> baseline through the rollback command
3. baseline -> candidate again
4. candidate -> intentionally broken upstream -> automatic candidate recovery

Example:

```sh
chmod +x ops/release/*.sh
ops/release/rehearse-rollback.sh \
  /opt/mad-official-clean-e2e-20260811/release-rehearsal \
  13016 13017 13018
```

Run requests through the edge while it switches six times:

```sh
ops/release/rehearse-concurrent-switch.sh \
  /opt/mad-official-clean-e2e-20260811/release-rehearsal \
  2000 50
```

HTTP 429 is counted separately because it proves the application is reachable
but enforcing its own rate limit. Any network error, HTTP 5xx, unexpected code,
or missing request fails the rehearsal. The concurrent switch test uses `/` by
default so application API rate limiting does not hide edge availability.

## Snapshot gate

Create the snapshot before the production edge is changed. Pass every release
configuration file, including files containing secrets; the snapshot directory
is created with owner-only permissions and must remain on protected storage.

```sh
ops/release/capture-release.sh \
  /opt/madapi-release-snapshots \
  release-20260811-001 \
  mad-new-api \
  mad-cpa \
  mad-postgres \
  root \
  new-api \
  /opt/madapi/source \
  /etc/nginx/sites-enabled/madapi.conf \
  /opt/madapi/runtime/cpa-config.yaml \
  /opt/madapi/runtime/.env
```

Verify that the PostgreSQL dump restores into an isolated temporary database:

```sh
ops/release/verify-postgres-snapshot.sh \
  mad-postgres root new-api \
  /opt/madapi-release-snapshots/release-20260811-001/database.dump \
  release20260811001
```

## Production gates

Do not release unless all of these are true:

- Candidate tests and real Codex client acceptance pass.
- `verify-cpa-config.sh` confirms image generation mode is `passthrough`.
- The production database clone starts successfully with both old and new app
  images and preserves key row counts.
- Snapshot restore verification passes.
- Explicit rollback and automatic post-switch failure recovery pass.
- The old image, old CPA config, old Nginx config, old environment, and database
  snapshot remain available for the entire observation window.
- GitHub and production are changed only after the test evidence is reviewed.

For a production deployment that currently uses SQLite, first import the exact
production image and database clone, then run `verify-sqlite-image-compat.sh`.
The test starts the current image, the candidate image, and the current image
again against the candidate-migrated clone. It also verifies that users,
channels, tokens, and options keep identical value-safe fingerprints.
