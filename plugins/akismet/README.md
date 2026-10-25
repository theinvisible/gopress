# Akismet Anti-spam

Spam protection with the [Akismet](https://akismet.com) service. This is the GoPress port of the WordPress plugin, and it is bundled with GoPress. It needs an Akismet API key.

- **Checking comments:** each new comment from a visitor is sent to Akismet (comment-check) before it is stored.
  - **Data sent:** the comment, the commenter's IP address, browser and referrer, the post's address, modified date, categories and tags, and the site's address and language.
  - **Spam:** comments Akismet calls spam go to the spam folder. With "Discard it silently", obvious spam (Akismet's `discard` advice) is not stored at all.
  - **Failures:** if Akismet cannot be reached within 3 seconds or answers with an error, the comment is held for moderation, never approved unchecked.
  - **Not checked:** comments of editors and administrators.
- **Learning:** when a moderator marks a comment as spam, or takes one out of spam, the plugin reports it to Akismet (submit-spam / submit-ham), with the moderator's login.
- **Settings:** under Settings › Akismet Anti-spam. The API key is a write-only field; it is checked with Akismet when saved.
- **Dashboard:** the number of spam comments caught (with the count from WordPress), the last six months and the moderators' corrections.
- **Import:** on activation it imports the WordPress options `wordpress_api_key` (unless a key is already set), `akismet_strictness` and `akismet_spam_count`.

## Privacy

Akismet is a service of Automattic. Comments and the commenter data listed above go to Akismet's servers, so mention it in your privacy policy.

## Not ported

- **Akismet's browser script:** it records typing and mouse behavior. GoPress has its own spam trap (hidden field, signed form token, rate limits).
- **Akismet's privacy notice under the comment form.**
- **Comment history and the check status in the comment list.**
- **The webhook, re-checks of held comments and Akismet's statistics pages.**

## Permissions

- **HTTP:** requests to `rest.akismet.com`.
- **Hooks:** checking new comments, and hearing about moderation.
- **Import data:** the imported options `wordpress_api_key`, `akismet_strictness` and `akismet_spam_count`.

## Build

`gopress plugin-pack plugins/akismet` (or `make plugins`). The tests run with `go test` in this directory.
