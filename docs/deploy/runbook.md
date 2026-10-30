# Runbook: moving a WordPress site to GoPress

This runbook covers a WordPress site that runs in Docker (WordPress plus MariaDB) behind Traefik.
The URLs, content and comments stay the same: GoPress imports a fresh copy of the WordPress
database at the moment of the switch. WordPress is stopped, never deleted, so going back takes
a minute.

## Placeholders

Fill them in once, in a private note outside the repository.

| Placeholder | Meaning | How to find it |
| --- | --- | --- |
| `<domain>` | the site's host name | |
| `<staging>` | staging host name, e.g. `staging.<domain>` | |
| `<image>` | `ghcr.io/…/gopress:<version>` | |
| `<wp-app>` | WordPress container | `docker ps` |
| `<wp-db>` | MariaDB container | `docker ps` |
| `<wp-net>` | Docker network of `<wp-db>` | `docker inspect <wp-db> -f '{{json .NetworkSettings.Networks}}'` |
| `<wp-uploads>` | host path of `wp-content/uploads` | `docker inspect <wp-app> -f '{{json .Mounts}}'` |
| `<proxy-net>` | a network only GoPress and Traefik join (see [deploy.md](../deploy.md#behind-a-reverse-proxy-traefik)) | |
| `<proxy-subnet>` | subnet of `<proxy-net>` | `docker network inspect <proxy-net>` |

For the commands below:

```sh
RUN="docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges"
```

## 0. Preparation

- **Image:** a release tag publishes it (see [deploy.md](../deploy.md#image)). Use the fixed version
  tag as `<image>` in compose, never `latest`, so the cutover runs exactly the accepted version. A
  public image needs no registry login on the server. For a private one, run `docker login ghcr.io`
  with a token that may only read packages (`read:packages`).
- **Database access for the import:** the importer only reads.
  - Best: a database user that may only read, created with the MariaDB root password:

    ```sql
    CREATE USER 'gopress_import'@'%' IDENTIFIED BY '<random password>';
    GRANT SELECT ON `<wp database>`.* TO 'gopress_import'@'%';
    ```

    Drop it after the cutover: `DROP USER 'gopress_import'@'%';`.
  - Store the DSN in a 0600 file that only root can read, and remove it after the cutover:

    ```sh
    umask 077
    printf 'GOPRESS_IMPORT_DSN=gopress_import:%s@tcp(<wp-db>:3306)/<wp database>\n' '<random password>' > /root/gopress-import.env
    ```

  - Or, with WordPress' own database user:

    ```sh
    umask 077
    docker exec <wp-db> sh -c 'printf "GOPRESS_IMPORT_DSN=%s:%s@tcp(<wp-db>:3306)/%s\n" "$MYSQL_USER" "$MYSQL_PASSWORD" "$MYSQL_DATABASE"' > /root/gopress-import.env
    ```

## 1. Staging

1. **DNS:** create `<staging>` pointing to the server.
2. **Data directories:**

   ```sh
   install -d -o 65532 -g 65532 -m 0750 /srv/gopress-staging/data /srv/gopress-staging/backups
   ```

3. **Import:**

   ```sh
   $RUN --network <wp-net> --env-file /root/gopress-import.env \
     -v <wp-uploads>:/wp-uploads:ro -v /srv/gopress-staging/data:/data <image> import-wp --uploads /wp-uploads
   ```

   It prints counts and warnings. Uploads must be readable for uid 65532; WordPress' default
   modes (644/755) are.
4. **Plugins:**

   ```sh
   $RUN -v /srv/gopress-staging/data:/data <image> plugin activate --import foto-slider-lightbox paypal-donations akismet
   ```

5. **Login:**

   ```sh
   $RUN -v /srv/gopress-staging/data:/data <image> user list
   $RUN -v /srv/gopress-staging/data:/data <image> user setup-code <login>
   ```

   Imported users keep their WordPress password. The setup code (valid 72 h) lets them set up the
   second factor at the first login.
6. **Start:** use your compose file for staging, like
   [compose.example.yaml](compose.example.yaml), with:
   - `GOPRESS_BASE_URL=https://<staging>`;
   - `GOPRESS_NOINDEX=true`;
   - `GOPRESS_DISABLE_OUTBOUND=true`;
   - its own router names and `Host(<staging>)`;
   - an IP allow list on the router: your own address (and the machine running the comparison).
     Staging holds real personal data and the imported password hashes; it must not be open to
     everyone.

   Staging then neither talks to Akismet or PayPal nor sends mail, and search engines stay out.

## 2. Compare with the live site

From the development machine (read-only requests, two at a time):

```sh
go run ./dev/parity -ref https://<domain> -got https://<staging> -site-host <domain> -concurrency 2 -delay 200ms
node dev/visual/visual.mjs --ref https://<domain> --got https://<staging>
```

- **Parity:** compares status codes, redirects, titles, content and the sidebar of every URL in the
  live sitemap, plus `local/parity-urls.txt`.
- **Visual comparison:** takes screenshots of the paths in `local/visual-urls.txt` at desktop and
  phone width and writes difference images to `local/visual/`.

## 3. Acceptance on staging

- [ ] Login with password and setup code, set up the second factor, store the recovery codes.
- [ ] Write a post, preview, publish, edit; it shows on the home page, in the feed and in the
      sitemap.
- [ ] Comment as a visitor. It is held, because Akismet cannot be reached, and appears in the
      moderation queue with a live notice. Approve it and reply.
- [ ] Gallery page: slider, lightbox, keyboard (arrows, Esc).
- [ ] Donate page and the sidebar widget show the PayPal button. Check the form only; do not pay.
- [ ] Media upload, featured image, widgets, menus, customizer.
- [ ] Backup and restore:

      ```sh
      docker exec <staging-container> /gopress backup --out /backups
      ```

      Then restore into a fresh directory and start a throwaway container on it.
- [ ] Logs (`docker logs`) without errors; memory after a while (`docker stats`).

**After acceptance:** remove staging, including `/srv/gopress-staging`. It holds a copy of personal
data: commenters and donors.

## 4. Cutover

Pick a quiet time. The site is down for a few minutes, from step 3 to step 6.

1. **Final WordPress backup:**

   ```sh
   umask 077
   docker exec <wp-db> sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mariadb-dump -u"$MYSQL_USER" --single-transaction "$MYSQL_DATABASE"' | gzip > /root/wp-final.sql.gz
   tar -czf /root/wp-content-final.tgz -C "$(dirname <wp-uploads>)" .
   ```

2. **Data directories:**

   ```sh
   install -d -o 65532 -g 65532 -m 0750 /srv/gopress/data /srv/gopress/backups
   ```

3. **Content freeze:** run `docker stop <wp-app>`.
   - Nobody can comment or donate now, so nothing gets lost.
   - `<wp-db>` keeps running for the import.
   - Traefik drops the WordPress routers, so the site answers 404 until GoPress runs.
4. **Import, plugins, login:** run steps 1.3 to 1.5 with `/srv/gopress/data`.
   - Compare the counts the importer prints with WordPress, for example:

     ```sh
     docker exec <wp-db> sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mariadb -u"$MYSQL_USER" "$MYSQL_DATABASE" -e "SELECT post_type, post_status, COUNT(*) FROM wp_posts GROUP BY 1,2; SELECT comment_approved, COUNT(*) FROM wp_comments GROUP BY 1"'
     ```

5. **Start GoPress:** use your production compose with `GOPRESS_BASE_URL=https://<domain>` and
   without the staging variables. Its Traefik labels take over what `<wp-app>` had:
   - the router for `<domain>`;
   - the redirect router for the other host names;
   - the middlewares the site used, e.g. a geo block or a redirect of old paths.
6. **Smoke test:**
   - `curl -sI https://<domain>/` answers 200.
   - `https://<domain>/healthz` answers `ok`.
   - Open a post, an image under `/wp-content/uploads/…`, `/feed/`, `/wp-sitemap.xml` and an old
     `/?p=<id>` link.
7. **Admin:**
   - Log in with the setup code and set up the second factor.
   - Settings › Email: set up SMTP and send a test mail.
   - Akismet: saving its settings checks the key.
   - PayPal: "Record donations (IPN)" stays off until you want it.
8. **Backups:** set up the daily backup (see [deploy.md](../deploy.md#backups)) and run the first
   one now.
9. **Clean up:** `rm /root/gopress-import.env`; drop the import database user if you created one.
10. **A few days later:** `docker stop <wp-db>`. Keep the WordPress containers, volumes, the
    wp-content directory and the final backup until you are sure. Never use `docker compose down -v`
    on the WordPress project.

## Rollback

1. Stop GoPress: `docker compose -p <gopress project> stop`.
2. Start WordPress: `docker start <wp-db> <wp-app>`. Its labels bring back its Traefik routers.

What was written in GoPress after the switch (comments, posts) is not in WordPress. The GoPress data
directory stays as it is; carry over what matters by hand.
