#!/usr/bin/env node
// Loads the balancer's management page in Chromium with a management key from the environment,
// the way the Management Center stores it, and reports page errors, console errors, failed
// requests and whether the loading text went away. Reads only; prints no key.
//   CPA_KEY=... node tools/page-check.mjs http://127.0.0.1:8317
// Run from a directory where @playwright/test resolves (e.g. the wapps-platform repo root).
import { chromium } from "@playwright/test";

const base = process.argv[2] ?? "http://127.0.0.1:8317";
const key = process.env.CPA_KEY;
if (!key) {
  console.error("CPA_KEY is required");
  process.exit(2);
}
const browser = await chromium.launch();
const page = await browser.newPage();
const problems = [];
page.on("pageerror", (e) => problems.push(`pageerror: ${String(e).slice(0, 300)}`));
page.on("console", (m) => m.type() === "error" && problems.push(`console: ${m.text().slice(0, 300)}`));
page.on("requestfailed", (r) => problems.push(`requestfailed: ${r.url().replace(/\?.*/, "")} ${r.failure()?.errorText}`));
page.on("response", (r) => r.status() >= 400 && problems.push(`http ${r.status()}: ${r.url().replace(/\?.*/, "")}`));
// The Management Center keeps its session under "cli-proxy-auth" in localStorage.
await page.addInitScript((k) => {
  try {
    localStorage.setItem("cli-proxy-auth", JSON.stringify({ state: { managementKey: k } }));
  } catch {}
}, key);
await page.goto(`${base}/v0/management/plugins/cpa-balancer/status`, { waitUntil: "load", timeout: 30000 });
await page.waitForTimeout(6000);
const loading = await page.getByText(/loading/i).count();
const text = (await page.locator("body").innerText()).slice(0, 400).replace(/\s+/g, " ");
console.log(JSON.stringify({ loadingTexts: loading, bodyStart: text, problems }, null, 2));
await browser.close();
