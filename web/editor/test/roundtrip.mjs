// Editor round-trip test: loads every post and page into the editor (in
// the local Chrome, on a running GoPress admin) and checks that writing it
// back changes nothing that matters, and that a second round trip is
// stable.
//
//   go run ./dev/exportcontent > local/editor-content.json
//   node web/editor/test/roundtrip.mjs local/editor-content.json
import fs from 'node:fs';
import { launch, login, credentials, BASE } from '../../../dev/e2e/lib.mjs';

const posts = JSON.parse(fs.readFileSync(process.argv[2] || 'local/editor-content.json'));
const browser = await launch();
const page = await browser.newPage();
await login(page, credentials());
await page.goto(BASE + '/admin/posts/new', { waitUntil: 'networkidle0' });

const results = await page.evaluate((posts) => {
	const t = window.gopress.editor._test;
	const ed = window.gopress.editor.instance.tiptap;
	const out = [];
	for (const p of posts) {
		const doc = t.importHTML(ed.schema, p.content);
		const html = t.exportHTML(ed.schema, doc);
		const html2 = t.exportHTML(ed.schema, t.importHTML(ed.schema, html));
		let blocks = 0, raw = 0, sc = 0;
		doc.content.forEach((n) => {
			blocks++;
			if (n.type.name === 'rawHTML') raw++;
			if (n.type.name === 'shortcode') sc++;
		});
		const a = t.canonical(p.content), b = t.canonical(html);
		let diff = '';
		if (a !== b) {
			let i = 0;
			while (i < a.length && a[i] === b[i]) i++;
			diff = 'orig: …' + a.slice(Math.max(0, i - 60), i + 80) + '\n      out:  …' + b.slice(Math.max(0, i - 60), i + 80);
		}
		out.push({ id: p.id, title: p.title, html, equal: a === b, stable: html === html2, blocks, raw, sc, diff,
			rawSamples: doc.content.content.filter((n) => n.type.name === 'rawHTML').slice(0, 3).map((n) => n.attrs.html.slice(0, 90)) });
	}
	return out;
}, posts);
await browser.close();

// The Go test internal/content TestEditorRoundTripRendersTheSame renders
// these outputs and compares them with the originals.
fs.writeFileSync(new URL('../../../local/editor-roundtrip.json', import.meta.url), JSON.stringify(results.map((r) => ({ id: r.id, html: r.html }))));

let failed = 0, totalBlocks = 0, totalRaw = 0;
for (const r of results) {
	totalBlocks += r.blocks;
	totalRaw += r.raw;
	if (!r.equal || !r.stable) {
		failed++;
		console.log(`FAIL ${r.id} ${r.title} equal=${r.equal} stable=${r.stable}\n      ${r.diff}`);
	}
	if (process.env.V && r.raw) console.log(`raw ${r.id} ${r.title}: ${r.raw}/${r.blocks}\n  ` + r.rawSamples.map((s) => s.replace(/\s+/g, ' ')).join('\n  '));
}
console.log(`${results.length} documents, ${totalBlocks} blocks, ${totalRaw} kept as HTML (${(100 * totalRaw / totalBlocks).toFixed(1)}%), ${failed} failed`);
process.exit(failed ? 1 : 0);
