# MadAPI production recovery

This directory defines the production backup and recovery contract. The public
source repository never receives plaintext production data.

## Schedule

- Local consistent SQLite and runtime snapshot: hourly at minute 15.
- Encrypted private-GitHub snapshot: every six hours at minute 30.
- Local retention: 72 hourly archives.
- Private GitHub retention: 28 six-hour snapshots and 30 daily snapshots.
- CPA upstream check: 04:00 Asia/Shanghai.
- CPA-only server deployment: 04:45 Asia/Shanghai.

The six-hour off-site interval limits whole-server-loss exposure while keeping
encrypted Git repository growth controlled. Local operator mistakes retain a
one-hour recovery point.

## Security

Each off-site archive uses a random AES-256 key and HMAC-SHA256 authentication.
The random key material is wrapped with RSA-OAEP-SHA256. The production server
contains only the recovery public key. The private key is kept outside the
server and in the private backup repository's GitHub Actions secret store.

The `production-backups` branch is force-squashed on every publish so encrypted
hourly history cannot grow without bound. The private repository's `main`
branch remains documentation and recovery automation only.

## Recovery boundary

The archive contains the active NewAPI database, Compose and environment files,
CPA runtime state, active Nginx site, TLS state, updater scripts, service units,
and deployed release hashes. It does not contain host SSH credentials.

On a compatible Debian or Ubuntu x86-64 server, install Docker Compose, Nginx,
Git, OpenSSL, and Python 3. Then decrypt and verify a selected bundle:

```sh
python3 restore.py /path/to/bundle --private-key /secure/recovery-private.pem --output /root/madapi-restore
```

Always inspect the verification result before copying the extracted root tree
onto a replacement server and starting the restored Compose project.

