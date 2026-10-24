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

// makePNG writes a w×h PNG with a gradient (no image library needed).
import zlib from 'node:zlib';
export function makePNG(file, w, h) {
	const crcTable = Array.from({ length: 256 }, (_, n) => {
		let c = n;
		for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
		return c >>> 0;
	});
	const crc = (buf) => { let c = 0xffffffff; for (const b of buf) c = crcTable[(c ^ b) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
	const chunk = (type, data) => {
		const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
		const td = Buffer.concat([Buffer.from(type), data]);
		const c = Buffer.alloc(4); c.writeUInt32BE(crc(td));
		return Buffer.concat([len, td, c]);
	};
	const ihdr = Buffer.alloc(13);
	ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
	const raw = Buffer.alloc((w * 3 + 1) * h);
	for (let y = 0; y < h; y++) {
		for (let x = 0; x < w; x++) {
			const o = y * (w * 3 + 1) + 1 + x * 3;
			raw[o] = (x * 255) / w; raw[o + 1] = (y * 255) / h; raw[o + 2] = 160;
		}
	}
	fs.writeFileSync(file, Buffer.concat([Buffer.from('\x89PNG\r\n\x1a\n', 'binary'), chunk('IHDR', ihdr),
		chunk('IDAT', zlib.deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]));
	return file;
}

// mailTo returns the newest message to addr from the mail sink directory
// (subject and decoded body), or null.
export function mailTo(addr) {
	const dir = process.env.E2E_MAIL_DIR;
	if (!dir) return null;
	const files = fs.readdirSync(dir).filter((f) => f.endsWith('.eml')).sort();
	for (const f of files.reverse()) {
		const raw = fs.readFileSync(dir + '/' + f, 'latin1');
		const [head, ...rest] = raw.split('\r\n\r\n');
		if (!new RegExp('^To: <' + addr.replace(/[.]/g, '\\.') + '>', 'mi').test(head)) continue;
		const body = Buffer.from(rest.join('\r\n\r\n').replace(/=\r\n/g, '').replace(/=([0-9A-F]{2})/g,
			(_, h) => String.fromCharCode(parseInt(h, 16))), 'latin1').toString('utf8');
		return { subject: (head.match(/^Subject: (.*)$/m) || [])[1], body };
	}
	return null;
}
