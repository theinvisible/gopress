# Deploying GoPress

GoPress ships as one static binary. The container image (`Dockerfile`) holds only that binary, the CA
certificates and a user entry. Everything that changes lives in one data directory.

- [compose.example.yaml](deploy/compose.example.yaml): a hardened service behind Traefik.
- [runbook.md](deploy/runbook.md): moving a WordPress site to GoPress: staging, acceptance, cutover,
  rollback.

## Image

```sh
make release        # tests, govulncheck, plugin check, image, image contents check, smoke test
make image-push     # docker push $(IMAGE):$(VERSION) and :latest (needs docker login)
```

- **Image name:** `IMAGE` defaults to `ghcr.io/theinvisible/gopress`. Override it for another registry.
- **Version:** builds from a dirty tree are tagged `…-dirty` and are never pushed.
- **`make image-check`:** fails if the image holds any file besides the binary, the CA bundle,
  `/etc/passwd`, `/etc/group` and `/data`.
- **`make image-smoke`:** starts the image the way production runs it (see below) and checks:
  - the health check;
  - the file permissions in the volume;
  - a plugin activation.

**Defaults of the image:**

| Setting | Value |
| --- | --- |
| User | `65532:65532` (no shell, no home besides `/data`) |
| Command | `/gopress serve` |
| Listen address | `GOPRESS_ADDR=:8080` |
| Data | `GOPRESS_DATA=/data` (volume) |
| Health check | `/gopress healthcheck` (GET `/healthz` on the listen address) |

## Data directory

| Path | Content |
| --- | --- |
| `gopress.db` (+ `-wal`, `-shm`) | SQLite database (WAL) |
| `secret.key` | the site secret (0600). All keys are derived from it: sessions, CSRF, 2FA secrets, SMTP password. GoPress refuses to start if it is readable by others |
| `uploads/` | media, with the WordPress paths (`/wp-content/uploads/…`) |
| `plugins/` | uploaded plugin packages (bundled plugins are in the binary) |
| `cache/wasm/` | compiled plugins, rebuilt when missing |

- **Ownership:** the directory must belong to the container user. A named volume gets this from the
  image. For a bind mount, create it with:

  ```sh
  install -d -o 65532 -g 65532 -m 0750 /srv/gopress/data
  ```

- **Writes:** GoPress writes nothing outside the data directory and needs no `/tmp`, so the root
  filesystem can be read-only.
- **Permissions:** files are created with umask 027.
- **Losing `secret.key`:** logs everyone out and makes the stored 2FA secrets and the SMTP password
  unreadable. Back it up together with the database; `gopress backup` does.

## Configuration

All `serve` flags can also be set through the environment:

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--addr` | `GOPRESS_ADDR` | listen address (image: `:8080`) |
| `--data` | `GOPRESS_DATA` | data directory (image: `/data`) |
| `--base-url` | `GOPRESS_BASE_URL` | public URL, e.g. `https://example.org`; overrides the `home` option |
| `--trusted-proxies` | `GOPRESS_TRUSTED_PROXIES` | CIDRs of the reverse proxy (see below) |
| `--cache-mb` | `GOPRESS_CACHE_MB` | page cache size (default 256, 0 disables) |
| `--noindex` | `GOPRESS_NOINDEX` | staging: robots.txt disallows everything, every response says `X-Robots-Tag: noindex` |
| `--disable-outbound` | `GOPRESS_DISABLE_OUTBOUND` | staging: plugins cannot reach other servers (Akismet holds comments, PayPal IPN cannot verify). Mail is not sent: it is marked as failed, so it never goes out later. The CLI (`mail test`, `plugin`) follows the variable too |

The switches accept `true`/`false` (also `1`/`0`, `yes`/`no`, `on`/`off`). Any other value stops
GoPress from starting, so a typo cannot silently leave a staging copy open. The log shows their
state at every start (`staging switches`).

- **`import-wp` DSN:** `import-wp` reads it from `GOPRESS_IMPORT_DSN`, so the database password does
  not show up in the process list.
- **Mail:** SMTP is set in the admin under Settings › Email. It needs TLS or STARTTLS; plain SMTP is
  only allowed to a relay on localhost.
- **Base URL:** must be `https://` in production. Admin cookies are `__Host-` cookies with
  `Secure`, so login only works over HTTPS (browsers make an exception for `http://localhost`).
  GoPress warns at startup otherwise.

## Behind a reverse proxy (Traefik)

**`GOPRESS_TRUSTED_PROXIES`:** set it to the proxy's address only. Every peer in this range may
claim any client address.

- **Use a dedicated network:** give GoPress and Traefik a network of their own, which no other
  container joins, and trust its subnet:

  ```sh
  docker network create gopress-proxy
  docker network connect gopress-proxy traefik
  docker network inspect gopress-proxy -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}'
  ```

  Trusting a network shared with other applications would let any of their containers fake client
  addresses.
- **Requests from other peers:** GoPress removes `X-Real-Ip`, `X-Forwarded-For`,
  `X-Forwarded-Proto`, `X-Forwarded-Host` and `Forwarded` before any handler sees them.
- **Requests from the proxy:**
  - The client address is the last `X-Forwarded-For` entry over all header lines, i.e. the one the
    proxy appended. `X-Real-Ip` counts only without `X-Forwarded-For`.
  - HTTPS is the last `X-Forwarded-Proto` value.
- **What the proxy must do:** append to `X-Forwarded-For` (or overwrite `X-Real-Ip`). Traefik does
  this unless `forwardedHeaders.trustedIPs` / `insecure` lets clients' headers through.
- **Why it matters:** client addresses are used for rate limits, lockouts, comment checks and the
  audit log.
- **No trusted proxy set:** GoPress warns at startup if the base URL is https without one. Without
  it, every client looks like the proxy and HSTS is not sent.

**Security headers:** GoPress sets them itself: CSP, HSTS over HTTPS, `X-Content-Type-Options`,
`Referrer-Policy`, `Permissions-Policy`, COOP. The admin gets stricter ones. A proxy middleware
that sets headers too (e.g. Traefik `headers`) overwrites GoPress' values. That is harmless as long
as it is not weaker; leave it out for GoPress if in doubt.

**Timeouts:**
- GoPress: reading a request 30 s, writing a response 60 s.
- The admin's live updates use Server-Sent Events. A stream ends after 30 minutes and the browser
  reconnects. Proxy timeouts of 60 s or more (or none) are fine.

**Restricting the admin:** optional. A second router for `PathPrefix(`/admin`) || Path(`/login`)`
with a higher priority can add an IP allow list or a geo block in front of the login. Keep these
public, so they are not blocked by that router:
- `/mcp`: if agents use it;
- `/plugins/…`: e.g. the PayPal IPN;
- the comment form at `/wp-comments-post.php`.

## Hardening

Run the container like this (the example compose does):

- `read_only: true`, with the data directory as the only writable volume;
- `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`;
- the image user (65532), never root (GoPress warns when it runs as root);
- a memory limit (see below);
- no published ports: only the proxy reaches the container over the shared network.

## Resources

- **Memory peaks:**
  - password hashing (argon2id: 64 MiB, at most 4 at a time);
  - plugins (at most 64 MiB per instance, up to 4 instances per plugin);
  - the page cache (`--cache-mb`).
- **Small site:** `mem_limit: 768m` with `GOMEMLIMIT=600MiB` leaves room for all of these.
- **CPU:** one or two cores are plenty; cached pages are served from memory.

## Backups

```sh
gopress backup --out /backups --keep 14      # also while the server runs
gopress restore --from /backups/gopress-20261025T030000Z.tar.gz --data /new/data
```

**What a backup holds:** one `tar.gz` (0600) with:
- a consistent copy of the database (`VACUUM INTO`, checked with `integrity_check`);
- `secret.key`;
- the uploads, uploaded plugins and theme overrides.

Caches and temporary files are left out. `--keep N` deletes the oldest archives in the target so
that N remain. It only touches files named `gopress-<timestamp>.tar.gz`, never the one just
written. Give each instance its own backup directory.

**Restore:**
- It only writes into an empty or missing directory.
- In a container, the archive must be readable by uid 65532. Archives written by the container
  itself are.
- It refuses archives with unexpected entries, links or paths outside the directory.
- It refuses backups from a newer GoPress (schema version).
- It then checks and migrates the database.

**The secret:** the archive contains `secret.key`. Keep backups as private as the server itself,
encrypted when they leave it.

**Daily backups:** with the container from the example compose, run a host timer or cron job:

```sh
docker exec gopress /gopress backup --out /backups --keep 14
```

Copy `/backups` to another machine (e.g. `restic`, `rsync`); a backup on the same disk does not
survive a lost disk.

## Updates

1. Back up (above).
2. `docker compose pull && docker compose up -d`. Migrations run at startup.
3. Check `docker compose logs gopress` and the site.

**Going back to an older image:** an older GoPress refuses to open a database that a newer one has
migrated. Restore the backup from step 1 into a fresh directory, or stay on the newer version.

## Logs and health

- **Logs:** go to stderr as `key=value` lines (`docker logs`). Limit their size with the Docker
  logging options (see the example).
- **`/healthz`:** answers `ok`. The image's `HEALTHCHECK` calls it through `gopress healthcheck`.
