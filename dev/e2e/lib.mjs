// Helpers for driving the GoPress admin with the local Chrome.
import puppeteer from 'puppeteer-core';
import crypto from 'node:crypto';
import fs from 'node:fs';

export const BASE = process.env.GP_BASE || 'http://localhost:8080';

export async function launch() {
	return puppeteer.launch({
		executablePath: process.env.CHROME || '/usr/bin/google-chrome',
		headless: true,
		args: ['--no-sandbox', '--disable-gpu', '--window-size=1400,1000'],
		defaultViewport: { width: 1400, height: 1000 },
	});
}

function b32decode(s) {
	const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
	let bits = '';
	for (const c of s.replace(/=+$/, '').toUpperCase()) bits += alpha.indexOf(c).toString(2).padStart(5, '0');
	const out = [];
	for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
	return Buffer.from(out);
}

export function totp(secret, t = Date.now()) {
	const step = Math.floor(t / 1000 / 30);
	const msg = Buffer.alloc(8);
	msg.writeBigUInt64BE(BigInt(step));
	const h = crypto.createHmac('sha1', b32decode(secret)).update(msg).digest();
	const o = h[h.length - 1] & 15;
	return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0');
}

// Each TOTP step is accepted once; remember the last one used per secret.
const usedFile = new URL('../../local/e2e-totp-used.json', import.meta.url);
export async function freshTOTP(secret) {
	let used = {};
	try { used = JSON.parse(fs.readFileSync(usedFile)); } catch {}
	for (;;) {
		const step = Math.floor(Date.now() / 30000);
		if (used[secret] !== step) {
			used[secret] = step;
			fs.writeFileSync(usedFile, JSON.stringify(used));
			return totp(secret);
		}
		await new Promise((r) => setTimeout(r, 1000));
	}
}

export async function login(page, { user, password, secret }) {
	await page.goto(BASE + '/login', { waitUntil: 'networkidle0' });
	await page.type('#user_login', user);
	await page.type('#user_pass', password);
	await Promise.all([page.waitForNavigation(), page.click('#wp-submit')]);
	if (page.url().includes('/login/2fa')) {
		await page.type('#authcode', await freshTOTP(secret));
		await Promise.all([page.waitForNavigation(), page.click('form[action="/login/2fa"] input[type=submit]')]);
	}
	if (!page.url().includes('/admin/')) throw new Error('login failed: ' + page.url());
}

export function credentials() {
	const f = new URL('../../local/e2e-credentials.json', import.meta.url);
	return JSON.parse(fs.readFileSync(f));
}
