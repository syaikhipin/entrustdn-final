// scripts-consumer-ui-loop.mjs — browser loop for the consumer-home UI bugs
// (#3 facet radios, #4 reload persistence, #14 logout). Run from web/.
import { chromium } from "playwright";
import { execSync } from "node:child_process";

const BASE = process.argv[2] || "http://localhost:3000";
const API = "http://localhost:8080";
const STAMP = Math.floor(Math.random() * 100000);
let pass = 0, fail = 0;
const ck = (label, ok, detail = "") => {
  if (ok) pass++;
  else { fail++; console.log(`FAIL: ${label}${detail ? " — " + detail : ""}`); }
};
const sh = (cmd) => {
  try { return execSync(cmd, { shell: "/bin/bash" }).toString(); }
  catch (e) { throw new Error(`sh failed: ${cmd.slice(0, 120)}`); }
};

const stamp = sh(`grep -o "loop-c[0-9]*" /tmp/thresh-backend.log | tail -1 | grep -o "[0-9]*"`).trim();
const email = `loop-c${stamp}@t.dev`;
ck("found loop consumer", !!stamp);

const browser = await chromium.launch();
const page = await browser.newPage();

// --- log in as the loop consumer ---
await page.goto(`${BASE}/login`);
await page.fill('input[type="email"]', email);
await page.fill('input[type="password"]', "pass-word-1");
await page.click('button[type="submit"]');
await page.waitForURL(/consumer/, { timeout: 15000 });
ck("login lands on consumer home", page.url().includes("/consumer"));

// --- #4: full page reload keeps the session ---
await page.reload({ waitUntil: "networkidle" });
await page.waitForTimeout(1500);
ck("#4 reload keeps session", !page.url().includes("/login"), `landed at ${page.url()}`);

// --- #3: facet radios check, filter, and toggle off ---
// Seed one shared asset via the org API so facets exist.
{
  const oStamp = sh(`grep -o "loop-o[0-9]*" /tmp/thresh-backend.log | tail -1 | grep -o "[0-9]*"`).trim();
  const oLogin = JSON.parse(sh(`curl -s -X POST ${API}/api/v1/login -H 'Content-Type: application/json' -d '{"email":"loop-o${oStamp}@t.dev","password":"pass-word-1"}'`));
  sh(`printf 'farm,crop,yield_t_h\\nF1,winter wheat,8.2\\n' > /tmp/seed-${STAMP}.csv && curl -s -X POST ${API}/api/v1/assets -H "Authorization: Bearer ${oLogin.session.token}" -F "file=@/tmp/seed-${STAMP}.csv;type=text/csv" -F "name=UI loop yields ${STAMP}" -F "description=Winter cereal yields for the consumer UI loop"`);
  await page.goto(`${BASE}/consumer`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
}
await page.waitForSelector(".facets input[type=radio]", { timeout: 20000 });
const firstRadio = page.locator(".facets .facet").first().locator("input[type=radio]").first();
await firstRadio.click({ force: true });
await page.waitForTimeout(500);
ck("#3 radio becomes checked", await firstRadio.isChecked());
ck("#3 facet panel still rendered", (await page.locator(".facets").count()) === 1);
await firstRadio.click({ force: true });
await page.waitForTimeout(400);
ck("#3 second click clears the axis", !(await firstRadio.isChecked()));

// --- #14: logout actually logs out ---
await page.click("button.link:has-text('Log out')");
await page.waitForTimeout(1200);
ck("#14 logout navigates home", (await page.url()) === BASE + "/" || page.url().endsWith("/"), `at ${page.url()}`);
ck("#14 nav shows Log in again", (await page.locator('a:has-text("Log in")').count()) > 0);

await browser.close();
console.log(`PASS=${pass} FAIL=${fail}`);
process.exit(fail === 0 ? 0 : 1);
