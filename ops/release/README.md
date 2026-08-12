# MadAPI release and rollback gate

This directory contains release infrastructure only. It does not modify NewAPI
relay behavior or CPA protocol behavior.

## Codex route boundary

The public `/codex/v1` route terminates at the same NewAPI instance as `/v1`.
NewAPI owns authentication, channel selection, retries, billing, refunds, and
logs. After channel selection, NewAPI calls the private official CPA Handler;
CPA owns the Codex protocol, tools, reasoning, Compact, and streaming behavior.

The embedded official CPA configuration uses:

```yaml
disable-image-generation: false
```

This preserves the official CPA image behavior, including the built-in
`image_generation` tool and the default `gpt-image-2` model. Do not replace it
with a custom tool injector or compatibility bridge. A gateway unit test locks
this value and runs before every release image build.

Generate one private control token and set the same value as
`MADAPI_CPA_CONTROL_TOKEN` in the NewAPI and CPA containers. The token never
appears in the public Nginx configuration. NewAPI's private control routes must
only be reachable from the CPA container network.

```sh
ops/release/render-codex-route.sh \
  http://127.0.0.1:18317 \
  http://127.0.0.1:3000 \
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
- Gateway unit tests confirm official image generation remains enabled.
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

## Unified NewAPI orchestration

The unified candidate shape is defined in docker-compose.unified.yml: exactly
one new-api service and one independent cpa-official-gateway service. CPA
points at the main NewAPI private control endpoint through the shared Docker
network. Ordinary `/v1` and `/codex/v1` both point at the unified NewAPI;
only the private synchronous execution hop points at the official CPA gateway.

The deploy entrypoint is deploy-unified-newapi.sh. For SQLite production it
first creates an online SQLite backup and validates candidate NewAPI and CPA
against that private snapshot. After preflight passes, it stops every old
application writer, starts the final pair as the sole writer of the real data
directory, validates NewAPI, CPA health, and the private execute endpoint, then
switches Nginx. Timestamped old containers remain available for rollback. The
old deploy-codex-control-only.sh definition remains unchanged as the emergency
legacy deployment path.

The production database defaults to `$MADAPI_DATA_DIR/one-api.db`. Override it
with `MADAPI_SQLITE_DATABASE` only when the active SQLite file has a different
name; the deploy refuses paths outside the mounted data directory.

After a successful deployment, use the recorded backup directory with
rollback-unified-newapi.sh stops the candidate writers before reopening the
same SQLite database with the old NewAPI and CPA, validates both old services,
and changes Nginx only after that validation succeeds.

Run the offline orchestration gate with:

    ops/release/validate-unified-orchestration.sh
    python3 ops/release/test-patch-nginx-unified-route.py
