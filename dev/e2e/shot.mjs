// Screenshots of admin pages: node shot.mjs /admin/ /admin/posts/ …
// Credentials: local/e2e-credentials.json {"user","password","secret"}.
import { launch, login, credentials, BASE } from './lib.mjs';

const urls = process.argv.slice(2);
const browser = await launch();
const page = await browser.newPage();
await login(page, credentials());
for (const u of urls) {
	await page.goto(BASE + u, { waitUntil: 'networkidle0' });
	const name = u.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'root';
	const file = new URL(`../../local/screens/admin-${name}.png`, import.meta.url).pathname;
	await page.screenshot({ path: file, fullPage: process.env.FULL === '1' });
	console.log(file);
}
await browser.close();
