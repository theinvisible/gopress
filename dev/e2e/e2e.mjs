// End-to-end test of the GoPress admin (run via dev/e2e/run.sh).
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import { launch, BASE, freshTOTP, makePNG, mailTo } from './lib.mjs';

const PASSWORD = process.env.E2E_PASSWORD;
const results = [];
async function step(name, fn) {
	const t = Date.now();
	try {
		await fn();
		results.push([true, name]);
		console.log(`ok   ${name} (${Date.now() - t} ms)`);
	} catch (e) {
		results.push([false, name]);
		console.log(`FAIL ${name}\n     ${e.stack.split('\n').slice(0, 3).join('\n     ')}`);
		if (process.env.SHOTS) await page.screenshot({ path: `/tmp/e2e-fail-${results.length}.png`, fullPage: true });
	}
}

const browser = await launch();
const page = await browser.newPage();
page.on('pageerror', (e) => console.log('     page error:', e.message));
page.on('dialog', (d) => d.accept());
// The virtual authenticator answers without user interaction, so passkey
// autofill on the login page would log in by itself: tests use the button.
await page.evaluateOnNewDocument(() => {
	if (window.PublicKeyCredential) PublicKeyCredential.isConditionalMediationAvailable = () => Promise.resolve(false);
});
const cdp = await page.createCDPSession();
await cdp.send('WebAuthn.enable');
await cdp.send('WebAuthn.addVirtualAuthenticator', {
	options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true,
		isUserVerified: true, automaticPresenceSimulation: true },
});

const go = (path) => page.goto(BASE + path, { waitUntil: 'networkidle0' });
const text = (sel) => page.$eval(sel, (e) => e.textContent.trim());
async function submit(sel) {
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click(sel)]);
}
async function passwordLogin(user, pw) {
	await go('/login');
	await page.type('#user_login', user);
	await page.type('#user_pass', pw);
	await submit('#wp-submit');
}
async function logout() {
	await page.evaluate(() => document.querySelector('#wp-admin-bar-logout form, form[action="/logout"]').submit());
	await page.waitForNavigation({ waitUntil: 'networkidle0' });
	assert.match(page.url(), /\/login\?msg=loggedout/);
}
async function fetchPage(path) {
	return page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).text(), BASE + path);
}

let secret = '';
let codes = [];

await step('password login forces two-factor setup', async () => {
	await passwordLogin('e2e', PASSWORD);
	assert.match(page.url(), /\/admin\/setup-2fa$/);
	// Everything else redirects back to the setup.
	await go('/admin/posts/');
	assert.match(page.url(), /\/admin\/setup-2fa$/);
});

await step('setup code required before enrolling', async () => {
	assert.ok(await page.$('input[name=setup_code]'), 'setup code form');
	assert.equal(await page.$('#totp-code'), null);
	await page.type('#setup_code', 'AAAA-BBBB-CCCC');
	await submit('form[action="/admin/setup-2fa/code"] button');
	assert.match(await text('.notice-error'), /setup code is incorrect/);
	await page.type('#setup_code', process.env.E2E_SETUP_CODE);
	await submit('form[action="/admin/setup-2fa/code"] button');
	assert.ok(await page.$('#totp-code'), 'enrollment offered after the code');
});

await step('enroll TOTP, get recovery codes', async () => {
	secret = await text('.gp-secret');
	await page.type('#totp-code', await freshTOTP(secret));
	await submit('form[action="/admin/setup-2fa/totp"] button[type=submit]');
	assert.match(page.url(), /\/admin\/recovery-codes$/);
	codes = await page.$$eval('.gp-codes code', (els) => els.map((e) => e.textContent));
	assert.equal(codes.length, 10);
	await go('/admin/recovery-codes'); // shown only once
	assert.match(page.url(), /\/admin\/profile\/security$/);
});

await step('add a passkey', async () => {
	await go('/admin/setup-2fa');
	await page.type('#passkey-name', 'Virtual key');
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click('[data-passkey-register] button')]);
	assert.match(page.url(), /msg=factor-added|\/admin\/profile\/security/);
	await go('/admin/profile/security');
	assert.match(await text('.gp-passkeys'), /Virtual key/);
});

await step('passkey login (no password)', async () => {
	await logout();
	await go('/login');
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click('[data-passkey-login] button')]);
	assert.match(page.url(), /\/admin\/$/);
	assert.match(await text('#wp-admin-bar-my-account .display-name'), /E2E Tester/);
});

await step('password + TOTP login', async () => {
	await logout();
	await passwordLogin('e2e', PASSWORD);
	assert.match(page.url(), /\/login\/2fa/);
	await page.type('#authcode', await freshTOTP(secret));
	await submit('form[action="/login/2fa"] input[type=submit]');
	assert.match(page.url(), /\/admin\/$/);
});

await step('password + recovery code login; a code works once', async () => {
	await logout();
	await passwordLogin('e2e', PASSWORD);
	await page.evaluate(() => { document.querySelector('.gp-recovery').open = true; });
	await page.type('#recovery_code', codes[0]);
	await submit('details.gp-recovery input[type=submit]');
	assert.match(page.url(), /\/admin\/$/);
	await logout();
	await passwordLogin('e2e', PASSWORD);
	await page.evaluate(() => { document.querySelector('.gp-recovery').open = true; });
	await page.type('#recovery_code', codes[0]);
	await submit('details.gp-recovery input[type=submit]');
	assert.match(page.url(), /\/login\/2fa/);
	assert.match(await text('#login_error'), /incorrect or was already used/);
	await page.type('#authcode', await freshTOTP(secret));
	await submit('form[action="/login/2fa"] input[type=submit]');
	assert.match(page.url(), /\/admin\/$/);
});

// ---- M2b ----

const tmpDir = fs.mkdtempSync(os.tmpdir() + '/gp-e2e-');
const waitFor = async (fn, ms = 10000) => {
	const end = Date.now() + ms;
	for (;;) {
		const v = await fn();
		if (v) return v;
		if (Date.now() > end) throw new Error('timeout');
		await new Promise((r) => setTimeout(r, 200));
	}
};
async function otherBrowser() {
	const ctx = await browser.createBrowserContext();
	const p = await ctx.newPage();
	p.on('dialog', (d) => d.accept());
	return { ctx, p, go: (path) => p.goto(BASE + path, { waitUntil: 'networkidle0' }),
		submit: (sel) => Promise.all([p.waitForNavigation({ waitUntil: 'networkidle0' }), p.click(sel)]) };
}

await step('lost password: reset link, second factor still required', async () => {
	const o = await otherBrowser();
	await o.go('/login/lost');
	await o.p.type('#user_login', 'victim');
	await o.submit('#wp-submit');
	assert.match(o.p.url(), /msg=lost/);
	const mail = await waitFor(() => mailTo('victim@example.org'));
	const link = mail.body.match(/https?:\/\/\S+\/login\/reset\/[A-Za-z0-9_-]{43}/)[0];
	await o.go(new URL(link).pathname);
	await o.p.type('#pass1', 'victim new passphrase');
	await o.p.type('#pass2', 'victim new passphrase');
	await o.submit('#wp-submit');
	assert.match(o.p.url(), /msg=reset/);
	await o.p.type('#user_login', 'victim');
	await o.p.type('#user_pass', 'victim new passphrase');
	await o.submit('#wp-submit');
	assert.match(o.p.url(), /\/admin\/setup-2fa$/);
	assert.ok(await o.p.$('input[name=setup_code]'), 'setup code still required');
	await o.ctx.close();
});

await step('lockout after failed logins, same message for unknown users', async () => {
	await logout();
	const tryLogin = async (user, pw) => {
		await passwordLogin(user, pw);
		return page.$eval('#login_error', (e) => e.textContent.trim()).catch(() => '');
	};
	const unknown = await tryLogin('nobody-here', 'wrong password!!');
	for (let i = 0; i < 5; i++) await tryLogin('victim', 'wrong password ' + i);
	const locked = await tryLogin('victim', PASSWORD); // right password, but locked
	assert.ok(unknown.length > 10);
	assert.equal(locked, unknown);
	assert.match(page.url(), /\/login$/);
});

await step('admin pages are not cached and framing is denied', async () => {
	const h = await page.evaluate(async (u) => {
		const r = await fetch(u);
		return { cc: r.headers.get('cache-control'), xfo: r.headers.get('x-frame-options'), csp: r.headers.get('content-security-policy') };
	}, BASE + '/login');
	assert.equal(h.cc, 'no-store');
	assert.equal(h.xfo, 'DENY');
	assert.match(h.csp, /frame-ancestors 'none'/);
});

await step('login again with passkey', async () => {
	await go('/login');
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click('[data-passkey-login] button')]);
	assert.match(page.url(), /\/admin\/$/);
});

let newURL = '';
await step('write and publish a post with the editor', async () => {
	await go('/admin/posts/new');
	await page.waitForSelector('.gp-content');
	await page.type('#title', 'E2E Test Post');
	await page.click('.gp-content');
	await page.keyboard.type('Intro paragraph with ');
	await page.keyboard.down('Control'); await page.keyboard.press('b'); await page.keyboard.up('Control');
	await page.keyboard.type('bold');
	await page.keyboard.down('Control'); await page.keyboard.press('b'); await page.keyboard.up('Control');
	await page.keyboard.type(' text.');
	await page.keyboard.press('Enter');
	// Slash command: heading.
	await page.keyboard.type('/head');
	await page.waitForSelector('.gp-slash-item');
	await page.keyboard.press('Enter');
	await page.keyboard.type('A heading');
	await page.keyboard.press('Enter');
	// Markdown shortcut: list.
	await page.keyboard.type('- first');
	await page.keyboard.press('Enter');
	await page.keyboard.type('second');
	await page.keyboard.press('Enter');
	await page.keyboard.press('Enter');
	// Image from the media library.
	await page.click('.gp-add-media');
	await page.waitForSelector('.gp-media-grid li img');
	await page.click('.gp-media-grid li');
	await page.click('.gp-media-frame footer .button-primary');
	await page.waitForSelector('.gp-content img');
	// Gallery via slash menu.
	await page.click('.gp-content p:last-of-type').catch(() => {});
	await page.keyboard.press('End');
	await page.keyboard.press('Enter');
	await page.keyboard.type('/gallery');
	await page.waitForSelector('.gp-slash-item');
	await page.keyboard.press('Enter');
	await page.waitForSelector('.gp-media-grid li img');
	const tiles = await page.$$('.gp-media-grid li');
	await tiles[0].click();
	await tiles[1].click();
	await page.click('.gp-media-frame footer .button-primary');
	await page.waitForSelector('.gp-sc');
	await page.click('#categorychecklist input[type=checkbox]');
	await submit('#publish');
	assert.match(page.url(), /\/admin\/posts\/\d+\/edit\?msg=published|\/edit$/);
	newURL = await page.$eval('#edit-slug-box a.button', (a) => a.href);
	const html = await fetchPage(new URL(newURL).pathname);
	assert.match(html, /<h1 class="entry-title">E2E Test Post<\/h1>/);
	assert.match(html, /<strong>bold<\/strong>/);
	assert.match(html, /<h2[^>]*>A heading<\/h2>/);
	assert.match(html, /<li>first<\/li>/);
	assert.match(html, /wp-image-\d+/);
	assert.match(html, /class=['"]gallery /);
	const home = await fetchPage('/');
	assert.match(home, /E2E Test Post/, 'home page cache was invalidated');
});

await step('drafts are private; preview shows the unsaved change', async () => {
	await go('/admin/posts/new');
	await page.waitForSelector('.gp-content');
	await page.type('#title', 'E2E Draft');
	await page.click('.gp-content');
	await page.keyboard.type('Draft body.');
	await submit('#save-post');
	const id = page.url().match(/posts\/(\d+)\/edit/)[1];
	const anon = await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).status, BASE + '/?p=' + id + '&preview=true');
	assert.equal(anon, 404, 'preview without login');
	await page.click('.gp-content');
	await page.keyboard.type(' Changed before preview.');
	const popup = new Promise((r) => browser.once('targetcreated', (t) => r(t.page())));
	await page.click('#post-preview');
	const pv = await popup;
	await pv.waitForFunction(() => location.href.includes('preview=true'), { timeout: 10000 });
	await pv.waitForSelector('.entry-content');
	assert.match(await pv.$eval('.entry-content', (e) => e.textContent), /Changed before preview/);
	await pv.close();
});

await step('saving an unchanged existing post keeps the page identical', async () => {
	// The second newest post is an imported one (the newest is the test post).
	await go('/admin/posts/');
	const [editPath, viewURL] = await page.$$eval('#the-list tr', (rows) =>
		[rows[1].querySelector('.row-title').getAttribute('href'), rows[1].querySelector('.view a').href]);
	const path = new URL(viewURL).pathname;
	// Whitespace between tags and <br /> vs. <br> render the same.
	const content = (html) => html.slice(html.indexOf('<div class="entry-content">'), html.indexOf('<footer class="entry-meta">'))
		.replace(/>\s+</g, '><').replace(/\s*\/>/g, '>');
	const before = content(await fetchPage(path));
	await go(editPath);
	await page.waitForSelector('.gp-content');
	await submit('#publish');
	const after = content(await fetchPage(path));
	assert.ok(before.length > 200);
	if (after !== before) {
		let i = 0;
		while (i < after.length && after[i] === before[i]) i++;
		assert.fail('differs at ' + i + ':\n  before: ' + before.slice(Math.max(0, i - 80), i + 120) + '\n  after:  ' + after.slice(Math.max(0, i - 80), i + 120));
	}
});

await step('quick edit and trash/restore from the list', async () => {
	await go('/admin/posts/');
	const row = await page.$('#the-list tr');
	await row.hover();
	await row.$eval('.editinline', (b) => b.click());
	await page.waitForSelector('.gp-quick-edit-form');
	await page.$eval('.gp-quick-edit-form input[name=title]', (i) => { i.value = 'E2E Test Post (edited)'; });
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.click('.gp-quick-edit-form button[type=submit]')]);
	assert.match(await text('#the-list tr .row-title'), /E2E Test Post \(edited\)/);
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.$eval('#the-list tr .trash button', (b) => b.click())]);
	assert.match(await text('.notice'), /Trash/);
	const gone = await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).status, newURL);
	assert.equal(gone, 404);
});

await step('comment moderation: approve, reply, spam', async () => {
	await go('/admin/comments/?comment_status=pending');
	const pending = await page.$eval('.gp-count-pending', (e) => Number(e.textContent));
	const id = await page.$eval('#the-comment-list tr', (tr) => tr.dataset.commentId);
	const postLink = await page.$eval('#the-comment-list tr .comments-edit-item-link', (a) => a.getAttribute('href'));
	await page.$eval('#the-comment-list tr [data-moderate=approve]', (b) => b.click());
	await page.waitForFunction((i) => !document.querySelector('#comment-' + i), {}, id);
	assert.equal(await page.$eval('.gp-count-pending', (e) => Number(e.textContent)), pending - 1);
	assert.equal(await page.$eval('#wp-admin-bar-comments .pending-count', (e) => Number(e.textContent)), pending - 1);
	// Reply to the next pending comment: it gets approved and the reply is
	// published on the site at once.
	const replyTo = await page.$eval('#the-comment-list tr', (tr) => tr.dataset.commentId);
	const replyPost = await page.$eval('#the-comment-list tr .comments-edit-item-link', (a) => a.getAttribute('href'));
	await page.$eval('#comment-' + replyTo + ' [data-comment-reply]', (b) => b.click());
	await page.waitForSelector('.gp-reply-form textarea');
	await page.type('.gp-reply-form textarea', 'E2E reply 4711');
	await page.click('.gp-reply-form button[type=submit]');
	await page.waitForFunction(() => !document.querySelector('.gp-reply-form'));
	assert.equal(await page.$eval('.gp-count-pending', (e) => Number(e.textContent)), pending - 2);
	const pid = replyPost.match(/\/(\d+)\/edit/)[1];
	// The reply sits in its parent's thread (maybe on an older comment
	// page); the post's comment feed lists the newest comments.
	const permalink = await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).url, BASE + '/?p=' + pid);
	const feed = new URL(permalink).pathname + 'feed/';
	assert.match(await fetchPage(feed), /E2E reply 4711/);
	// Mark the reply as spam: it disappears from the site.
	await go('/admin/comments/?s=E2E+reply+4711');
	await page.$eval('#the-comment-list tr [data-moderate=spam]', (b) => b.click());
	await page.waitForFunction(() => !document.querySelector('#the-comment-list tr[data-comment-id]'));
	assert.doesNotMatch(await fetchPage(feed), /E2E reply 4711/);
});

await step('media library: upload, edit alt text', async () => {
	await go('/admin/media/');
	const file = makePNG(tmpDir + '/E2E Upload Grafik.png', 1200, 800);
	const input = await page.$('[data-file-input]');
	await input.uploadFile(file);
	await page.waitForSelector('.gp-media-tile[aria-label="E2E Upload Grafik"]', { timeout: 15000 });
	await page.click('.gp-media-tile[aria-label="E2E Upload Grafik"]');
	await page.waitForSelector('.gp-media-details[open]');
	await page.type('.gp-media-details [data-field=alt]', 'A gradient');
	await page.$eval('.gp-media-details [data-field=alt]', (e) => e.dispatchEvent(new Event('change', { bubbles: true })));
	await page.waitForFunction(() => document.querySelector('.gp-details-status').textContent === 'Saved.');
	const url = await page.$eval('.gp-media-details [data-url]', (e) => e.value);
	assert.match(url, /\/wp-content\/uploads\/\d{4}\/\d{2}\/E2E-Upload-Grafik\.png$/);
	assert.equal(await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).status, url), 200);
	await page.keyboard.press('Escape');
});

let pastedPost = '';
await step('editor: paste a screenshot, set a featured image, publish', async () => {
	await go('/admin/posts/new');
	await page.waitForSelector('.gp-content');
	await page.type('#title', 'E2E Paste');
	await page.click('.gp-content');
	await page.keyboard.type('Screenshot:');
	await page.evaluate(async () => {
		const c = document.createElement('canvas'); c.width = 800; c.height = 400;
		const g = c.getContext('2d'); g.fillStyle = '#2271b1'; g.fillRect(0, 0, 800, 400);
		const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
		const dt = new DataTransfer();
		dt.items.add(new File([blob], 'image.png', { type: 'image/png' }));
		document.querySelector('.gp-content').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
	});
	await page.waitForFunction(() => { const i = document.querySelector('.gp-content img'); return i && /\/wp-content\/uploads\//.test(i.getAttribute('src')); }, { timeout: 15000 });
	assert.match(page.url(), /\/admin\/posts\/\d+\/edit$/, 'draft created for the upload');
	await page.click('.gp-featured-set');
	await page.waitForSelector('.gp-media-dialog li[role=option]');
	await page.click('.gp-media-dialog li[role=option]');
	await page.click('.gp-media-frame footer .button-primary');
	await page.waitForFunction(() => document.querySelector('input[name=featured_media]').value !== '');
	await submit('#publish');
	pastedPost = await page.$eval('#sample-permalink a, .gp-permalink a, #edit-slug-box a', (a) => a.href).catch(() => '');
	const id = page.url().match(/posts\/(\d+)\/edit/)[1];
	const html = await fetchPage('/?p=' + id);
	assert.match(html, /wp-image-\d+/);
	assert.match(html, /class="attachment-post-thumbnail size-post-thumbnail wp-post-image"/);
});

await step('categories: add one, it has an archive', async () => {
	await go('/admin/categories/');
	await page.type('#tag-name', 'E2E Kategorie');
	await submit('#addtag input[type=submit]');
	assert.match(await text('#the-list'), /E2E Kategorie/);
	assert.equal(await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).status, BASE + '/category/e2e-kategorie/'), 200);
});

await step('invite a user: link by mail, password and 2FA setup', async () => {
	await go('/admin/users/new');
	await page.type('#user_login', 'e2e-invitee');
	await page.type('#email', 'invitee@example.org');
	await page.select('#role', 'author');
	await submit('form[action="/admin/users/new"] input[type=submit]');
	if (page.url().includes('/admin/reauth')) { // the login was a while ago
		await page.type('#pwd', PASSWORD);
		await page.type('#code', await freshTOTP(secret));
		await submit('form[action="/admin/reauth"] button[type=submit]');
		await go('/admin/users/new');
		await page.type('#user_login', 'e2e-invitee');
		await page.type('#email', 'invitee@example.org');
		await page.select('#role', 'author');
		await submit('form[action="/admin/users/new"] input[type=submit]');
	}
	assert.match(await text('.gp-onetime'), /Invitation link/);
	const mail = await waitFor(() => mailTo('invitee@example.org'));
	const link = mail.body.match(/https?:\/\/\S+\/login\/invite\/[A-Za-z0-9_-]{43}/)[0];
	const o = await otherBrowser();
	await o.go(new URL(link).pathname);
	await o.p.type('#pass1', 'invitee long passphrase');
	await o.p.type('#pass2', 'invitee long passphrase');
	await o.submit('#wp-submit');
	assert.match(o.p.url(), /\/admin\/setup-2fa$/);
	const s = await o.p.$eval('.gp-secret', (e) => e.textContent.trim());
	await o.p.type('#totp-code', await freshTOTP(s));
	await o.submit('form[action="/admin/setup-2fa/totp"] button');
	assert.match(o.p.url(), /\/admin\/recovery-codes/);
	await o.ctx.close();
});

await step('settings: posts per page shows on the site', async () => {
	await go('/admin/settings/reading');
	const before = await page.$eval('#posts_per_page', (i) => i.value);
	await page.$eval('#posts_per_page', (i) => { i.value = '2'; });
	await submit('form[action="/admin/settings/reading"] input[type=submit]');
	const n = (await fetchPage('/')).match(/<article id="post-/g).length;
	await go('/admin/settings/reading');
	await page.$eval('#posts_per_page', (i, v) => { i.value = v; }, before);
	await submit('form[action="/admin/settings/reading"] input[type=submit]');
	assert.ok(n <= 3, 'articles on the front page: ' + n); // plus a sticky post at most
});

await step('widgets: add, move, delete', async () => {
	await go('/admin/widgets');
	const meta = await page.$$eval('.gp-widget-type', (els) => els.findIndex((e) => /Meta/.test(e.textContent)));
	const types = await page.$$('.gp-widget-type');
	await types[meta].$eval('summary', (s) => s.click());
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), types[meta].$eval('form', (f) => f.submit())]);
	const id = await page.$$eval('#sidebar-1 .gp-widget', (els) => els.map((e) => e.dataset.id).filter((x) => x.startsWith('meta-')).pop());
	assert.ok(id, 'meta widget added');
	assert.match(await fetchPage('/'), new RegExp('id="' + id + '"'));
	// Move it to the inactive widgets.
	await page.$eval(`[data-id="${id}"] details`, (d) => { d.open = true; });
	await page.select(`[data-id="${id}"] [data-move-to]`, 'wp_inactive_widgets');
	await page.waitForFunction(() => document.querySelector('.gp-widgets-status').textContent === 'Order saved.');
	assert.doesNotMatch(await fetchPage('/'), new RegExp('id="' + id + '"'));
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }),
		page.$eval(`[data-id="${id}"] [formaction$="/delete"]`, (b) => b.click())]);
	assert.equal(await page.$(`[data-id="${id}"]`), null);
});

await step('menus: custom link in the primary menu', async () => {
	await go('/admin/menus');
	await page.type('#new-menu-name', 'E2E Menu');
	await submit('form[action="/admin/menus/new"] button');
	await page.$eval('.gp-menu-source[data-kind=custom]', (d) => { d.open = true; });
	await page.type('[data-custom-url]', 'https://example.org/e2e');
	await page.type('[data-custom-label]', 'E2E Link');
	await page.click('[data-add-custom]');
	await page.$eval('.gp-menu-source[data-kind=page]', (d) => { d.open = true; });
	await page.$$eval('.gp-menu-source[data-kind=page] input[type=checkbox]', (cbs) => { if (cbs[0]) cbs[0].checked = true; });
	await page.click('.gp-menu-source[data-kind=page] [data-add-items]');
	await page.click('.gp-menu-item:last-child [data-move="in"]');
	await page.click('input[name="menu-locations[primary]"]');
	await submit('#update-nav-menu .gp-menu-header button[type=submit]');
	const html = await fetchPage('/');
	assert.match(html, /<ul id="menu-e2e-menu" class="nav-menu">/);
	assert.match(html, />E2E Link<\/a>\s*<ul class="sub-menu">/);
	await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle0' }), page.$eval('#update-nav-menu [formaction$="/delete"]', (b) => b.click())]);
	assert.match(await fetchPage('/'), /<div class="nav-menu"><ul>/);
});

await step('customizer: preview first, publish later', async () => {
	await go('/admin/customize');
	const old = await page.$eval('input[name=blogname]', (i) => i.value);
	await page.$eval('input[name=blogname]', (i) => { i.value = ''; });
	await page.type('input[name=blogname]', 'E2E Preview Title');
	await page.waitForFunction(() => /Preview updated/.test(document.querySelector('.gp-customize-status').textContent), { timeout: 10000 });
	const frame = page.frames().find((f) => f.name() === 'gp-preview');
	await frame.waitForFunction(() => /E2E Preview Title/.test(document.querySelector('.site-title').textContent), { timeout: 10000 });
	assert.doesNotMatch(await fetchPage('/'), /E2E Preview Title/, 'not public before publishing');
	await submit('#customize-form .gp-customize-head button');
	assert.match(await fetchPage('/'), /E2E Preview Title/);
	await page.$eval('input[name=blogname]', (i, v) => { i.value = v; }, old);
	await submit('#customize-form .gp-customize-head button');
	assert.doesNotMatch(await fetchPage('/'), /E2E Preview Title/);
});

await step('toolbar on the site for logged-in users', async () => {
	await go('/');
	assert.ok(await page.$('#wpadminbar #wp-admin-bar-my-account'), 'toolbar');
	assert.doesNotMatch(await fetchPage('/'), /wpadminbar/, 'no toolbar for visitors');
});

await step('scheduled post is published by the scheduler', async () => {
	await go('/admin/posts/new');
	await page.waitForSelector('.gp-content');
	await page.type('#title', 'E2E Scheduled');
	const when = new Date(Date.now() + 65000);
	const local = new Date(when.getTime() - when.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
	await page.$eval('input[name=post_date]', (i, v) => { i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); }, local);
	assert.equal(await page.$eval('#publish', (b) => b.value), 'Schedule');
	await submit('#publish');
	assert.match(await text('#post-status-display'), /Scheduled/);
	const id = page.url().match(/posts\/(\d+)\/edit/)[1];
	let status = 0;
	for (let i = 0; i < 30 && status !== 200; i++) {
		await new Promise((r) => setTimeout(r, 5000));
		status = await page.evaluate(async (u) => (await fetch(u, { credentials: 'omit' })).status, BASE + '/?p=' + id);
	}
	assert.equal(status, 200);
});

await browser.close();
const failed = results.filter((r) => !r[0]).length;
console.log(`\n${results.length - failed}/${results.length} passed`);
process.exit(failed ? 1 : 0);
