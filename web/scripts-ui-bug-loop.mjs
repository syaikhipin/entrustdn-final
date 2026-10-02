// scripts-ui-bug-loop.mjs — browser walk of the org collections flow
// (#6, #7, #9): roster add → open collection → start conversations →
// member answers via the resume URL → sync → completed. Run from web/.
import { chromium } from "playwright";
import { execSync } from "node:child_process";

const BASE = process.argv[2] || "http://localhost:3000";
const API = "http://localhost:8080";
let pass = 0, fail = 0;
const ck = (label, ok, detail = "") => {
  if (ok) pass++;
  else { fail++; console.log(`FAIL: ${label}${detail ? " — " + detail : ""}`); }
};
const sh = (cmd) => {
  try { return execSync(cmd, { shell: "/bin/bash" }).toString(); }
  catch (e) { throw new Error(`sh failed: ${cmd.slice(0, 120)}\n${String(e.stdout || "").slice(0, 200)}`); }
};

const stamp = sh(`grep -o "loop-o[0-9]*" /tmp/thresh-backend.log | tail -1 | grep -o "[0-9]*"`).trim();
const orgEmail = `loop-o${stamp}@t.dev`;
const consStamp = sh(`grep -o "loop-c[0-9]*" /tmp/thresh-backend.log | tail -1 | grep -o "[0-9]*"`).trim();
const consEmail = `loop-c${consStamp}@t.dev`;

// A clarified request for the collection, via the consumer API.
const cLogin = JSON.parse(sh(`curl -s -X POST ${API}/api/v1/login -H 'Content-Type: application/json' -d '{"email":"${consEmail}","password":"pass-word-1"}'`));
const created = JSON.parse(sh(`curl -s -X POST ${API}/api/v1/requests -H "Authorization: Bearer ${cLogin.session.token}" -H 'Content-Type: application/json' -d '{"description":"Org flow walk yields","format":"csv","budget_micros":100000}'`));
const rid = created.request.id;
let clarified = false, reply = "";
for (let i = 0; i < 4 && !clarified; i++) {
  const chat = JSON.parse(sh(`curl -s -m 90 -X POST ${API}/api/v1/requests/${rid}/chat -H "Authorization: Bearer ${cLogin.session.token}" -H 'Content-Type: application/json' -d '{"message":"Per-farm winter cereal yields 2026 Ireland CSV numeric — treat as clarified."}'`));
  clarified = chat.clarified === true; reply = chat.reply || "";
}
ck("request clarified for the walk", clarified, `reply=${String(reply).slice(0, 120)}`);

const browser = await chromium.launch();
const page = await browser.newPage();

// --- org side ---
await page.goto(`${BASE}/login`);
await page.fill('input[type="email"]', orgEmail);
await page.fill('input[type="password"]', "pass-word-1");
await page.click('button[type="submit"]');
await page.waitForURL(/organization/, { timeout: 15000 });
ck("org login", page.url().includes("/organization"));

// --- #6: add a roster member through the UI ---
await page.fill('input[placeholder*="Máire"]', `Walk Member ${stamp}`);
await page.fill('input[placeholder*="email:maire"]', `email:walk${stamp}@t.dev`);
await page.click('button:has-text("Add member")');
await page.waitForSelector(`text=Walk Member ${stamp}`, { timeout: 10000 });
ck("#6 member added via UI", true);

// --- open a collection via the UI ---
const option = await page.locator("select option", { hasText: "Org flow walk yields" }).first().getAttribute("value");
ck("request option present in picker", !!option);
await page.selectOption("select", option);
// Check the walk member's own box (the last roster row), not .first()
// which would pick a stale member from an earlier run.
await page.locator(".member-picks label", { hasText: `Walk Member ${stamp}` }).last().locator("input[type=checkbox]").check();
await page.fill('textarea[placeholder*="farm size"]', "What was your 2026 winter cereal yield in tonnes per hectare?");
await page.click('button:has-text("Open collection")');
await page.waitForSelector('button:has-text("Start conversations")', { timeout: 15000 });
ck("collection opened via UI", true);

// --- #7: start conversations; the resume link appears per member ---
// The newest collection is the one just opened: scope to its row via the
// request id cell, so stacked older collections never take the click.
const row = page.locator("tbody tr", { hasText: rid }).first();
await row.locator('button:has-text("Start conversations")').click();
await row.locator('button:has-text("Copy member link")').waitFor({ timeout: 60000 });
ck("#7 resume link surfaced in UI", true);

// Resolve the member's resume URL via the org's conversation list (the
// clipboard is unreadable headless; the link's token is the same).
const oLogin = JSON.parse(sh(`curl -s -X POST ${API}/api/v1/login -H 'Content-Type: application/json' -d '{"email":"${orgEmail}","password":"pass-word-1"}'`));
const convs = JSON.parse(sh(`curl -s -H "Authorization: Bearer ${oLogin.session.token}" ${API}/api/v1/conversations`));
// The list view carries the request id inside the topic string.
const cv = (convs.conversations || []).find((c) => (c.topic || "").includes(rid) && (c.member_name || "").includes("Walk"));
const memberLink = cv ? `${API}/api/v1/member/resume/${cv.resume_token}` : "";
ck("#7 member resume URL resolved", memberLink.includes("/member/resume/"), memberLink);

// --- the member answers via the resume path ---
const token = memberLink.split("/").pop();
const ans = sh(`curl -s -X POST ${API}/api/v1/member/reply -H 'Content-Type: application/json' -d '{"token":"${token}","message":"9.1 tonnes per hectare"}'`);
ck("#7 member reply accepted", !ans.includes("error"), ans.slice(0, 120));

// --- sync through the UI ---
await page.reload({ waitUntil: "networkidle" });
const row2 = page.locator("tbody tr", { hasText: rid }).first();
await row2.locator('button:has-text("Sync")').click();
await page.waitForSelector('.pill.ok:has-text("Completed")', { timeout: 60000 }).catch(() => {});
const donePill = await page.locator('.pill.ok:has-text("Completed")').count();
ck("#9 collection completed in org UI", donePill > 0);

// --- consumer side: download button appears ---
const cpage = await browser.newPage();
await cpage.goto(`${BASE}/login`);
await cpage.fill('input[type="email"]', consEmail);
await cpage.fill('input[type="password"]', "pass-word-1");
await cpage.click('button[type="submit"]');
await cpage.waitForURL(/consumer/, { timeout: 15000 });
await cpage.waitForSelector('button:has-text("Download delivery")', { timeout: 15000 });
ck("#9 consumer sees Download delivery", true);

await browser.close();
console.log(`PASS=${pass} FAIL=${fail}`);
process.exit(fail === 0 ? 0 : 1);
