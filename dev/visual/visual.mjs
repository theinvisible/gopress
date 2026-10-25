// Visual comparison of two sites page by page (e.g. live WordPress vs.
// GoPress staging): full-page screenshots at desktop and phone width and
// a pixel diff computed in the browser (no image libraries needed).
//
//   node dev/visual/visual.mjs --ref https://example.org --got https://staging.example.org [--max 0.5] [paths…]
//
// Paths come from the arguments, else from local/visual-urls.txt (one per
// line, site-specific, not committed), else "/". Screenshots and diff
// images go to local/visual/. Uses puppeteer-core from dev/e2e and the
// local Chrome (CHROME=…).
import fs from 'node:fs';
import { launch } from '../e2e/lib.mjs';

const args = process.argv.slice(2);
const opt = (name, def) => {
	const i = args.indexOf('--' + name);
	if (i < 0) return def;
	const v = args[i + 1];
	args.splice(i, 2);
	return v;
};
const ref = opt('ref', 'http://127.0.0.1:8081').replace(/\/$/, '');
const got = opt('got', 'http://127.0.0.1:8080').replace(/\/$/, '');
const maxPct = Number(opt('max', '0.5')); // allowed share of differing pixels
const out = new URL('../../local/visual/', import.meta.url).pathname;
let paths = args;
if (paths.length === 0) {
	try {
		paths = fs.readFileSync(new URL('../../local/visual-urls.txt', import.meta.url), 'utf8')
			.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
	} catch {
		paths = ['/'];
	}
}
const widths = [1280, 375];
fs.mkdirSync(out, { recursive: true });

const browser = await launch();
const page = await browser.newPage();
await page.setCacheEnabled(false); // no conditional requests (304)
await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }]);
await page.setUserAgent((await browser.userAgent()) + ' GoPress-visual/1.0');

async function shot(base, path, width) {
	await page.setViewport({ width, height: 900 });
	const resp = await page.goto(base + path, { waitUntil: 'networkidle0', timeout: 60000 });
	await page.evaluate(() => document.fonts.ready);
	// Freeze anything that still moves (carets, transitions).
	await page.addStyleTag({ content: '*,*::before,*::after{transition:none!important;animation:none!important;caret-color:transparent!important}' });
	await new Promise((r) => setTimeout(r, 300));
	return { status: resp ? resp.status() : 0, png: await page.screenshot({ fullPage: true }) };
}

// diff runs in a blank page: decodes both PNGs, counts pixels whose color
// differs by more than a small tolerance and paints them red over a faded
// copy of the reference.
async function diff(a, b) {
	const blank = await browser.newPage();
	const res = await blank.evaluate(async (a64, b64) => {
		const load = (src) => new Promise((ok, fail) => { const i = new Image(); i.onload = () => ok(i); i.onerror = fail; i.src = src; });
		const [ia, ib] = await Promise.all([load('data:image/png;base64,' + a64), load('data:image/png;base64,' + b64)]);
		const w = Math.max(ia.width, ib.width), h = Math.max(ia.height, ib.height);
		const px = (img) => { const c = new OffscreenCanvas(w, h); const x = c.getContext('2d'); x.fillStyle = '#fff'; x.fillRect(0, 0, w, h); x.drawImage(img, 0, 0); return x.getImageData(0, 0, w, h); };
		const da = px(ia), db = px(ib);
		const outC = new OffscreenCanvas(w, h), ox = outC.getContext('2d'), od = ox.createImageData(w, h);
		let n = 0;
		for (let i = 0; i < da.data.length; i += 4) {
			const d = Math.max(Math.abs(da.data[i] - db.data[i]), Math.abs(da.data[i + 1] - db.data[i + 1]), Math.abs(da.data[i + 2] - db.data[i + 2]));
			if (d > 24) {
				n++;
				od.data.set([255, 0, 0, 255], i);
			} else {
				const g = 255 - (255 - (da.data[i] + da.data[i + 1] + da.data[i + 2]) / 3) * 0.25;
				od.data.set([g, g, g, 255], i);
			}
		}
		ox.putImageData(od, 0, 0);
		const blob = await outC.convertToBlob({ type: 'image/png' });
		const buf = new Uint8Array(await blob.arrayBuffer());
		let s = '';
		for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode(...buf.subarray(i, i + 0x8000));
		return { pct: (100 * n) / (w * h), sizes: [ia.width, ia.height, ib.width, ib.height], png: btoa(s) };
	}, Buffer.from(a).toString('base64'), Buffer.from(b).toString('base64'));
	await blank.close();
	return res;
}

let failed = 0;
for (const path of paths) {
	for (const width of widths) {
		const name = (path.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'home') + '-' + width;
		const [a, b] = [await shot(ref, path, width), await shot(got, path, width)];
		fs.writeFileSync(out + name + '-ref.png', a.png);
		fs.writeFileSync(out + name + '-got.png', b.png);
		const d = await diff(a.png, b.png);
		fs.writeFileSync(out + name + '-diff.png', Buffer.from(d.png, 'base64'));
		const sameSize = d.sizes[0] === d.sizes[2] && d.sizes[1] === d.sizes[3];
		const ok = a.status === b.status && sameSize && d.pct <= maxPct;
		if (!ok) failed++;
		console.log(`${ok ? 'ok  ' : 'DIFF'} ${path} @${width}: ${d.pct.toFixed(2)}% pixels differ` +
			(sameSize ? '' : `, size ref=${d.sizes[0]}x${d.sizes[1]} got=${d.sizes[2]}x${d.sizes[3]}`) +
			(a.status === b.status ? '' : `, status ref=${a.status} got=${b.status}`));
	}
}
await browser.close();
console.log(`\n${paths.length * widths.length} screenshots compared, ${failed} differ (images in ${out})`);
process.exit(failed ? 1 : 0);
