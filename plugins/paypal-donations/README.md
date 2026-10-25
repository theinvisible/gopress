# PayPal Donations

A PayPal donation button. This is the GoPress port of the WordPress plugin "PayPal Donations", and it is bundled with GoPress.

- **Shortcode:** `[paypal-donation]`, with the optional attributes `purpose`, `reference`, `amount`, `return_page` and `button_url`. Empty attributes use the settings. An invalid amount or URL is ignored.
- **Widget:** "PayPal Donations", with a title, a text (plain text, paragraphs and line breaks) and a purpose and reference.
- **Settings:** under Settings › PayPal Donations. They hold the PayPal account, currency, default purpose, reference and amount, the button (PayPal's in 16 languages, or your own image), the return page, page style, checkout language, a new tab, centering, the sandbox and the tracking pixel.
- **Recording donations (optional):** "Record donations (PayPal IPN)" turns it on.
  - **The form:** it asks PayPal to report each donation to `/plugins/paypal-donations/ipn`. The old URL `/?ppd_paypal_ipn=process` still works.
  - **Checks:** a donation counts only if PayPal confirms the message (it is sent back unchanged and the answer must be `VERIFIED`). It must also be for the configured account, be a new transaction and be completed. Refunds and reversals update the original donation. Messages from the sandbox count only while the sandbox is on.
  - **What is stored:** date, amount, fee, currency, status, type, name, email, purpose, reference and message. Addresses, payer ids and the raw message are not stored.
  - **Where it shows:** donations appear under Donations in the admin (search, delete) and in a dashboard box. Administrators get a live notice and, optionally, an email.
  - **Forged messages:** messages PayPal does not confirm are not recorded, and the administrators are warned at most once an hour.
- **Templates:** `templates/form.html`, `widget.html` and `dashboard.html`. A theme can replace them under `plugins/paypal-donations/`.
- **Import:** on activation, the WordPress options (`paypal_donations_options`) become the settings. Recording stays off, as in the WordPress plugin. Donations of the older plugin that stored them as `donation_list` posts are imported with the fields above; those PayPal had not confirmed are marked "not verified".

## Changes from the WordPress plugin

- **IPN:** the WordPress plugin only checked notifications and stored nothing; it did not check the account, transaction or status. The `validate_ipn` shortcode attribute is replaced by the "Record donations" setting.
- **Widget text:** it is plain text; the WordPress widget printed it as raw HTML.
- **Options left out:** "Remove line feeds" is gone. "Center the button" is set on the button itself, not in the page head.

## Permissions

- **HTTP:** requests to `ipnpb.paypal.com` and `ipnpb.sandbox.paypal.com`, to confirm notifications.
- **Routes:** `/ipn`, and the old query URL `?ppd_paypal_ipn=process`.
- **Import data:** the imported option `paypal_donations_options` and the `donation_list` posts.
- **Administrators:** email to the administrators, and live notices in the admin.

## Build

`gopress plugin-pack plugins/paypal-donations` (or `make plugins`). The tests run with `go test` in this directory.
