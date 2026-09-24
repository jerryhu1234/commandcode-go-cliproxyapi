#!/usr/bin/env node
/*
 * Real-browser harness for resources/quota_page.html. It intentionally has no
 * repository dependency: point NODE_PATH at a temporary playwright-core
 * install, then pass a CDP endpoint for any Chromium browser.
 *
 *   NODE_PATH=/tmp/pw/node_modules node tests/quota_page_browser_harness.js \
 *     --cdp http://127.0.0.1:9222
 *
 * This is a Manager iframe fixture, not a full CPAMP/Manager build. It models
 * only the relevant same-origin iframe and localStorage behavior.
 */
"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { TextEncoder } = require("node:util");
const { chromium } = require("playwright-core");

const cdpArg = process.argv.indexOf("--cdp");
if (cdpArg < 0 || !process.argv[cdpArg + 1]) throw new Error("usage: --cdp http://127.0.0.1:PORT");
const cdp = process.argv[cdpArg + 1];
const quotaHTML = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"));
const HOST = "127.0.0.1";
const SAME_PORT = 18731;
const CROSS_PORT = 18732;
const secret = "BROWSER-HARNESS-FAKE-KEY";
const leakedBody = "UPSTREAM-SECRET-BODY";
const requests = [];
let responseStatus = 200;

function state(key) { return JSON.stringify({state: {managementKey: key}}); }
function encrypt(version, value, host, userAgent) {
  const seedText = version === "v2"
    ? `cli-proxy-api-webui::secure-storage|v2|${host}`
    : `cli-proxy-api-webui::secure-storage|${host}|${userAgent}`;
  const bytes = new TextEncoder().encode(value);
  const seed = new TextEncoder().encode(seedText);
  for (let i = 0; i < bytes.length; i++) bytes[i] ^= seed[i % seed.length];
  return `enc::${version}::${Buffer.from(bytes).toString("base64")}`;
}
function fixture(raw, iframeOrigin = "") {
  const set = raw === undefined ? "localStorage.removeItem('cli-proxy-auth')" :
    `localStorage.setItem('cli-proxy-auth', ${JSON.stringify(raw)})`;
  return `<!doctype html><html data-theme="dark"><body><script>${set}</script>` +
    `<iframe id="quota" src="${iframeOrigin}/resources/quota_page.html"></iframe></body></html>`;
}
function usageResponse() {
  return {
    key_id: "credential-1", email: "browser@example.test", label: "Browser account",
    usage: {
      plan: "Pro", credits_left: 42,
      five_hour: {status: "ok", percent: 25, used: 1, cap: 4},
      weekly: {status: "ok", percent: 50, used: 5, cap: 10},
      month: {status: "ok", percent: 10, used: 10, cap: 100},
    },
  };
}
function server(port) {
  return http.createServer((req, res) => {
    const url = new URL(req.url, `http://${req.headers.host}`);
    if (url.pathname === "/resources/quota_page.html") {
      res.writeHead(200, {"content-type": "text/html; charset=utf-8"}); res.end(quotaHTML); return;
    }
    if (url.pathname === "/fixture") {
      const raw = url.searchParams.has("raw") ? url.searchParams.get("raw") : undefined;
      const origin = url.searchParams.get("iframeOrigin") || "";
      res.writeHead(200, {"content-type": "text/html; charset=utf-8"}); res.end(fixture(raw, origin)); return;
    }
    if (url.pathname.endsWith("/quota-usage") && req.method === "POST") {
      let body = "";
      req.on("data", chunk => { body += chunk; });
      req.on("end", () => {
        requests.push({authorization: req.headers.authorization || "", body, origin: req.headers.origin || ""});
        res.writeHead(responseStatus, {"content-type": "application/json"});
        if (responseStatus !== 200) { res.end(JSON.stringify({error: leakedBody, echoed_key: secret})); return; }
        const input = JSON.parse(body);
        res.end(JSON.stringify(input.key_id ? usageResponse() : {cards: [{key_id: "credential-1", label: "Configured credential"}]}));
      });
      return;
    }
    res.writeHead(404); res.end("not found");
  }).listen(port, HOST);
}
function listening(s) { return new Promise((resolve, reject) => { s.once("listening", resolve); s.once("error", reject); }); }
function close(s) {
  if (s.closeAllConnections) s.closeAllConnections();
  return new Promise(resolve => s.close(resolve));
}
function frame(page) { return page.frameLocator("#quota"); }
async function shown(page, text) { await frame(page).getByText(text, {exact: false}).first().waitFor(); }
async function open(page, port, raw, iframeOrigin = "") {
  const query = new URLSearchParams();
  if (raw !== undefined) query.set("raw", raw);
  if (iframeOrigin) query.set("iframeOrigin", iframeOrigin);
  await page.goto(`http://${HOST}:${port}/fixture?${query}`);
}
async function runCase(name, fn) {
  requests.length = 0; responseStatus = 200;
  await fn();
  console.log(`PASS ${name}`);
}

(async () => {
  const same = server(SAME_PORT), cross = server(CROSS_PORT);
  await Promise.all([listening(same), listening(cross)]);
  let browser;
  try {
    browser = await chromium.connectOverCDP(cdp);
    console.log(`BROWSER ${await browser.version()}`);
    const context = browser.contexts()[0] || await browser.newContext();
    const page = await context.newPage();
    const host = `${HOST}:${SAME_PORT}`;
    const ua = await page.evaluate(() => navigator.userAgent);

    for (const [label, raw] of [
      ["v2 storage, authenticated request, and rendered card", encrypt("v2", state(secret), host, ua)],
      ["v1 storage compatibility", encrypt("v1", state(secret), host, ua)],
      ["plaintext storage compatibility", state(secret)],
    ]) await runCase(label, async () => {
      await open(page, SAME_PORT, raw);
      await shown(page, "Browser account"); await shown(page, "25%"); await shown(page, "50%");
      assert.equal(requests.length, 2);
      assert.ok(requests.every(r => r.authorization === `Bearer ${secret}`));
      assert.deepEqual(requests.map(r => JSON.parse(r.body)), [{}, {key_id: "credential-1"}]);
    });

    for (const [label, raw, expected] of [
      ["missing storage does not request", undefined, "credentials were not found"],
      ["malformed storage does not request", "not-json", "could not be decoded"],
    ]) await runCase(label, async () => {
      await open(page, SAME_PORT, raw); await shown(page, expected); assert.equal(requests.length, 0);
      assert.ok(!(await frame(page).locator("body").innerText()).includes(secret));
    });

    for (const status of [401, 403]) await runCase(`${status} response is redacted`, async () => {
      responseStatus = status; await open(page, SAME_PORT, state(secret)); await shown(page, String(status));
      const body = await frame(page).locator("body").innerText();
      assert.ok(!body.includes(leakedBody)); assert.ok(!body.includes(secret)); assert.equal(requests.length, 1);
    });

    await runCase("cross-origin iframe cannot read top-level key and sends no request", async () => {
      await page.goto(`http://${HOST}:${SAME_PORT}/fixture`);
      await page.evaluate(() => localStorage.removeItem("cli-proxy-auth"));
      await open(page, CROSS_PORT, state(secret), `http://${HOST}:${SAME_PORT}`);
      await shown(page, "same origin"); assert.equal(requests.length, 0);
    });

    await runCase("sign-in followed by iframe refresh recovers", async () => {
      await open(page, SAME_PORT, undefined); await shown(page, "credentials were not found");
      await page.evaluate(value => localStorage.setItem("cli-proxy-auth", value), encrypt("v2", state(secret), host, ua));
      await frame(page).locator("html").evaluate(node => node.ownerDocument.defaultView.location.reload());
      await shown(page, "Browser account"); assert.equal(requests.length, 2);
    });
    await page.close();
    console.log("RESULT 9/9 cases passed");
  } finally {
    if (browser) await browser.close().catch(() => {});
    await Promise.all([close(same), close(cross)]);
  }
})().catch(error => { console.error(error.stack || error); process.exitCode = 1; });
