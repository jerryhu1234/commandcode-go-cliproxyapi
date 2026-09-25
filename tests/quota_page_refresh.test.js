const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const html = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"), "utf8");
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)][1][1];

class Node {
  constructor(tag = "div") { this.tagName = tag; this.children = []; this.style = {}; this.attributes = {}; this.disabled = false; this.onclick = null; }
  append(...nodes) { this.children.push(...nodes); }
  appendChild(node) { this.children.push(node); return node; }
  set textContent(value) { this._text = String(value); this.children = []; }
  get textContent() { return this._text || this.children.map(child => child.textContent || "").join(""); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  querySelectorAll(selector) { const found = []; const visit = node => { if (selector === "button" && node.tagName === "button") found.push(node); node.children.forEach(visit); }; visit(this); return found; }
  set className(value) { this._className = value; }
  get className() { return this._className || ""; }
}

const usage = { five_hour: { percent: 10, used: 1, cap: 10, status: "ok" }, weekly: { percent: 20, used: 2, cap: 10, status: "ok" }, credits_left: 3 };
const response = body => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
const waitTurn = () => new Promise(resolve => setImmediate(resolve));

async function waitFor(condition, message) {
  for (let i = 0; i < 100; i++) { if (condition()) return; await waitTurn(); }
  assert.fail(message || "condition did not complete within 100 turns");
}

function boot(cards, { cache = {}, outcomes = [] } = {}) {
  const nodes = new Map([["app", new Node()], ["refresh-all", new Node("button")], ["quota-summary", new Node()]]);
  const calls = []; let active = 0; let peak = 0; let nextOutcome = 0;
  const stored = JSON.stringify(cache); const writes = [];
  const context = {
    document: { getElementById: id => nodes.get(id), createElement: tag => new Node(tag), documentElement: new Node("html"), body: new Node("body") },
    window: {}, location: { host: "test.local" }, navigator: { userAgent: "quota-test" },
    localStorage: { getItem: () => JSON.stringify({ state: { managementKey: "page-key" } }) },
    sessionStorage: { getItem: () => stored, setItem: (key, value) => writes.push({ key, value }) },
    fetch: (url, options) => {
      const body = JSON.parse(options.body || "{}");
      if (!body.key_id) return response({ cards });
      active++; peak = Math.max(peak, active);
      const call = { key_id: body.key_id, active, resolve: null };
      calls.push(call);
      const outcome = outcomes[nextOutcome++] || { ok: true };
      return new Promise(resolve => {
        call.resolve = () => { active--; resolve(outcome.ok ? response({ usage, label: "Account " + body.key_id }) : response({ error: "SERVER-SECRET-DO-NOT-SHOW" })); };
      });
    },
    MutationObserver: class { observe() {} }, getComputedStyle: () => ({ getPropertyValue: () => "" }),
    TextEncoder, TextDecoder, atob: globalThis.atob, btoa: globalThis.btoa, Promise, Date, Math, JSON, console,
  };
  context.window = context;
  vm.runInNewContext(script, context);
  return { nodes, calls, writes, stats: () => ({ active, peak }) };
}

function cardsWithEmail(count = 14) { return Array.from({ length: count }, (_, i) => ({ key_id: "key-" + i, label: "Account " + i, email: "user" + i + "@test" })); }
function callsFor(page, key) { return page.calls.filter(call => call.key_id === key); }
async function resolveAll(page) { while (page.calls.some(call => call.resolve)) { const pending = page.calls.filter(call => call.resolve); pending.forEach(call => { const done = call.resolve; call.resolve = null; done(); }); await waitTurn(); } }

test("14 accounts are refreshed once with a measured concurrency peak of two", async () => {
  const page = boot(cardsWithEmail());
  await waitFor(() => page.nodes.get("app").querySelectorAll("button").length === 14);
  page.nodes.get("refresh-all").onclick();
  await waitFor(() => page.calls.length === 2, "two requests should start");
  assert.equal(page.stats().active, 2);
  await resolveAll(page);
  assert.equal(page.calls.length, 14);
  assert.equal(new Set(page.calls.map(call => call.key_id)).size, 14);
  assert.ok(page.stats().peak <= 2);
});

test("mixed results report exact success/failure counts and retain cached data on failure", async () => {
  const cards = cardsWithEmail();
  const old = { five_hour: { percent: 77, used: 7, cap: 10, status: "ok" }, weekly: { percent: 88, used: 8, cap: 10, status: "ok" } };
  const cache = { "key-0": { usage: old, fetched_at: "2020-01-01T00:00:00.000Z", label: "Old account" }, "key-1": { usage: old, fetched_at: "2020-01-01T00:00:00.000Z", label: "Old account" } };
  const page = boot(cards, { cache, outcomes: Array.from({ length: 14 }, (_, i) => ({ ok: i > 1 })) });
  await waitFor(() => page.nodes.get("app").querySelectorAll("button").length === 14); page.nodes.get("refresh-all").onclick(); await waitFor(() => page.calls.length === 2); await resolveAll(page);
  await waitFor(() => page.nodes.get("quota-summary").textContent.startsWith("Finished:"));
  assert.equal(page.nodes.get("quota-summary").textContent, "Finished: 12 succeeded, 2 failed");
  assert.match(page.nodes.get("app").textContent, /Quota refresh failed/);
  assert.doesNotMatch(page.nodes.get("app").textContent, /SERVER-SECRET/);
  const written = JSON.parse(page.writes.at(-1).value);
  assert.deepEqual(written["key-0"].usage, old);
  assert.equal(written["key-0"].fetched_at, "2020-01-01T00:00:00.000Z");
});

test("a running single-card refresh joins batch and duplicate clicks are disabled", async () => {
  const page = boot(cardsWithEmail());
  await waitFor(() => page.nodes.get("app").querySelectorAll("button").length === 14);
  page.nodes.get("app").querySelectorAll("button")[0].onclick();
  await waitFor(() => page.calls.length === 1);
  page.nodes.get("refresh-all").onclick(); page.nodes.get("refresh-all").onclick();
  await resolveAll(page);
  assert.equal(callsFor(page, "key-0").length, 1);
  assert.ok(page.stats().peak <= 2);
});

test("first-time accounts without email share the same two-request limit", async () => {
  const page = boot(cardsWithEmail().map(card => ({ ...card, email: "" })));
  await waitFor(() => page.nodes.get("app").querySelectorAll("button").length === 14);
  await waitFor(() => page.calls.length === 2);
  assert.ok(page.stats().peak <= 2);
  await resolveAll(page); assert.equal(page.calls.length, 14);
});

test("empty lists disable batch, completion enables a second full batch, and retry clears an error", async () => {
  const empty = boot([]); await waitFor(() => empty.nodes.get("refresh-all").disabled); assert.equal(empty.nodes.get("refresh-all").disabled, true);
  const page = boot(cardsWithEmail(), { outcomes: [{ ok: false }, ...Array(13).fill({ ok: true }), { ok: true }] });
  await waitFor(() => page.nodes.get("app").querySelectorAll("button").length === 14); page.nodes.get("refresh-all").onclick(); await waitFor(() => page.calls.length === 2); await resolveAll(page); await waitFor(() => page.calls.length === 14);
  assert.equal(page.nodes.get("refresh-all").disabled, false); page.nodes.get("refresh-all").onclick(); await resolveAll(page); await waitFor(() => page.calls.length === 28);
  assert.equal(page.calls.length, 28); assert.equal(page.nodes.get("refresh-all").disabled, false);
  assert.doesNotMatch(page.nodes.get("app").textContent, /Quota refresh failed/);
});

test("the page has no timer polling and exposes polite progress text", () => {
  assert.match(html, /aria-live="polite"/); assert.match(html, /Refreshing \" \+ batchState\.done/);
  assert.doesNotMatch(html, /setInterval|setTimeout|visibilitychange|document\.hidden/);
});
