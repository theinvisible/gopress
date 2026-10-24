// Prints the HTML of an admin page (debugging): node dump.mjs /admin/…
import { launch, login, credentials, BASE } from './lib.mjs';
const browser = await launch();
const page = await browser.newPage();
await login(page, credentials());
const res = await page.goto(BASE + process.argv[2]);
console.log(await res.text());
await browser.close();
