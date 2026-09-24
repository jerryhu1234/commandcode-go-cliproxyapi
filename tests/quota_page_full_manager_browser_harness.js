#!/usr/bin/env node
/*
 * CPAMP v1.13.0 release acceptance harness. No repository dependencies.
 * Install playwright-core outside the repository and set NODE_PATH.
 *
 * Required asset (exact release, verified by this script):
 *   management.html -- sha256 5974f172549a5d5f5b34a2c9a9c1e60f413bc963f799e430aacd4dd8e0a4eaac
 *
 * Usage:
 *   node tests/quota_page_full_manager_browser_harness.js \
 *     --cdp http://127.0.0.1:19222 --manager /tmp/management.html
 */
"use strict";

const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { chromium } = require("playwright-core");

function arg(name) {
  const i = process.argv.indexOf(name);
  if (i < 0 || !process.argv[i + 1]) throw new Error(`missing ${name}`);
  return process.argv[i + 1];
}
const cdp = arg("--cdp");
const managerPath = path.resolve(arg("--manager"));
const expectedSHA = "5974f172549a5d5f5b34a2c9a9c1e60f413bc963f799e430aacd4dd8e0a4eaac";
const managerHTML = fs.readFileSync(managerPath);
const actualSHA = crypto.createHash("sha256").update(managerHTML).digest("hex");
assert.equal(actualSHA, expectedSHA, "manager asset must be the CPAMP v1.13.0 release management.html");
const quotaHTML = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"));
const HOST = "127.0.0.1", PORT = 18733;
const fakeKey = "FULL-MANAGER-FAKE-KEY";
const requests = [];

function json(res, body, headers = {}) {
  res.writeHead(200, {"content-type": "application/json", ...headers});
  res.end(JSON.stringify(body));
}
const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  let body = "";
  req.on("data", c => { body += c; });
  req.on("end", () => {
    requests.push({method: req.method, path: url.pathname, authorization: req.headers.authorization || "", body});
    if (url.pathname === "/management.html" || url.pathname === "/") {
      res.writeHead(200, {"content-type": "text/html; charset=utf-8"}); res.end(managerHTML); return;
    }
    if (url.pathname === "/v0/resource/plugins/commandcode-go-cliproxyapi/quota") {
      res.writeHead(200, {"content-type": "text/html; charset=utf-8"}); res.end(quotaHTML); return;
    }
    if (url.pathname === "/v0/management/config") {
      json(res, {plugins: {enabled: true}}, {
        "x-cpa-version": "v7.3.2", "x-cpa-support-plugin": "true",
      }); return;
    }
    if (url.pathname === "/v0/management/plugins") {
      json(res, {plugins_enabled: true, plugins_dir: "plugins", plugins: [{
        id: "commandcode-go-cliproxyapi", path: "/isolated/mock/plugin.so",
        configured: true, registered: true, enabled: true, effective_enabled: true,
        supports_oauth: false,
        metadata: {name: "CommandCode", version: "browser-harness", author: "test"},
        menus: [{path: "/v0/resource/plugins/commandcode-go-cliproxyapi/quota", menu: "CommandCode Quota", description: "Quota acceptance"}],
      }]}); return;
    }
    if (url.pathname.endsWith("/quota-usage") && req.method === "POST") {
      const input = JSON.parse(body || "{}");
      if (input.key_id) json(res, {key_id: input.key_id, email: "full-manager@example.test", label: "Full Manager Account", usage: {
        plan: "Pro", credits_left: 42,
        five_hour: {status: "ok", percent: 25, used: 1, cap: 4},
        weekly: {status: "ok", percent: 50, used: 5, cap: 10},
        month: {status: "ok", percent: 10, used: 10, cap: 100},
      }});
      else json(res, {cards: [{key_id: "credential-1", label: "Configured credential"}]});
      return;
    }
    res.writeHead(404, {"content-type": "application/json"}); res.end(JSON.stringify({error: "not mocked"}));
  });
});
function listening() { return new Promise((resolve, reject) => { server.once("listening", resolve); server.once("error", reject); server.listen(PORT, HOST); }); }
function close() { if (server.closeAllConnections) server.closeAllConnections(); return new Promise(r => server.close(r)); }

(async () => {
  await listening();
  let browser;
  try {
    browser = await chromium.connectOverCDP(cdp);
    console.log(`ASSET CPAMP v1.13.0 management.html sha256:${actualSHA}`);
    console.log(`BROWSER ${await browser.version()}`);
    const context = browser.contexts()[0] || await browser.newContext();
    const page = await context.newPage();
    await page.goto(`http://${HOST}:${PORT}/management.html`);
    await page.evaluate(() => localStorage.clear());
    await page.reload();
    await page.getByPlaceholder(/management key|管理密钥|管理員金鑰/i).fill(fakeKey);
    const remember = page.locator('input[type="checkbox"]').last();
    assert.equal(await remember.count(), 1, "real Manager login must expose Remember password");
    if (!await remember.isChecked()) await remember.evaluate(node => node.click());
    assert.equal(await remember.isChecked(), true, "Remember credential must be selected through the real login form");
    await page.getByRole("button", {name: /login|登录|登入/i}).click();
    await page.getByText(/CommandCode Quota/i, {exact: true}).first().waitFor({timeout: 30000});

    const stored = await page.evaluate(() => localStorage.getItem("cli-proxy-auth"));
    assert.match(stored || "", /^enc::v2::/, "real Manager login must persist v2 auth storage");
    assert.ok(!stored.includes(fakeKey), "persisted auth must not contain plaintext key");
    const pluginNav = page.getByText(/CommandCode Quota/i, {exact: true}).first();
    await pluginNav.evaluate(node => node.closest("a,button")?.click());
    const iframe = page.frameLocator("iframe");
    try {
      await iframe.getByText("Full Manager Account", {exact: false}).waitFor({timeout: 30000});
    } catch (error) {
      console.error("DEBUG URL", page.url());
      console.error("DEBUG FRAMES", page.frames().map(f => f.url()));
      console.error("DEBUG FRAME BODY", await iframe.locator("body").innerText().catch(e => String(e)));
      console.error("DEBUG STORAGE", stored);
      console.error("DEBUG REQUESTS", requests.map(r => `${r.method} ${r.path} ${r.authorization}`));
      console.error("DEBUG BODY", (await page.locator("body").innerText()).slice(0, 3000));
      throw error;
    }
    await iframe.getByText("25%", {exact: true}).waitFor();
    await iframe.getByText("50%", {exact: true}).waitFor();

    const quotaRequests = requests.filter(r => r.path.endsWith("/quota-usage"));
    assert.deepEqual(quotaRequests.map(r => JSON.parse(r.body)), [{}, {key_id: "credential-1"}]);
    assert.ok(quotaRequests.every(r => r.authorization === `Bearer ${fakeKey}`));
    const pluginList = requests.find(r => r.path === "/v0/management/plugins");
    assert.equal(pluginList.authorization, `Bearer ${fakeKey}`);
    const frameURL = page.frames().find(f => f.url().includes("/v0/resource/plugins/commandcode-go-cliproxyapi/quota"))?.url();
    assert.equal(frameURL, `http://${HOST}:${PORT}/v0/resource/plugins/commandcode-go-cliproxyapi/quota`);
    console.log("PASS real login persisted enc::v2 auth storage");
    console.log("PASS real plugin discovery/menu route loaded current quota resource in iframe");
    console.log("PASS iframe decoded Manager auth, sent fake Authorization, and rendered quota card");
    console.log("RESULT 3/3 full-manager acceptance checks passed");
    await page.close();
  } finally {
    if (browser) await browser.close().catch(() => {});
    await close();
  }
})().catch(e => { console.error(e.stack || e); process.exitCode = 1; });
