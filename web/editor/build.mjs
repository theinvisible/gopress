// Bundles the editor into ../admin/static/editor (committed, so building
// GoPress needs no Node) and writes the licenses of the bundled packages
// next to it. Run: npm run build
import * as esbuild from 'esbuild';
import fs from 'node:fs';
import path from 'node:path';

const outdir = '../admin/static/editor';
const opts = {
	entryPoints: { editor: 'src/editor.js' },
	bundle: true,
	minify: true,
	format: 'iife',
	target: ['es2020'],
	outdir,
	legalComments: 'none',
	logLevel: 'info',
	metafile: true,
};

function writeLicenses(metafile) {
	const pkgs = new Map();
	for (const input of Object.keys(metafile.inputs)) {
		const m = input.match(/node_modules\/((?:@[^/]+\/)?[^/]+)\//);
		if (m) pkgs.set(m[1], path.join('node_modules', m[1]));
	}
	let out = 'GoPress editor (GPL-2.0-or-later) bundles the following packages:\n';
	for (const [name, dir] of [...pkgs].sort()) {
		const pj = JSON.parse(fs.readFileSync(path.join(dir, 'package.json')));
		const lic = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
		out += `\n${'='.repeat(72)}\n${name}@${pj.version} (${pj.license})\n${'='.repeat(72)}\n`;
		if (lic) out += fs.readFileSync(path.join(dir, lic), 'utf8').trim() + '\n';
	}
	fs.writeFileSync(path.join(outdir, 'THIRD-PARTY-LICENSES.txt'), out);
}

if (process.argv.includes('--watch')) {
	const ctx = await esbuild.context(opts);
	await ctx.watch();
} else {
	const res = await esbuild.build(opts);
	writeLicenses(res.metafile);
}
