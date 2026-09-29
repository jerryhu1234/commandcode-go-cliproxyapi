const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const html = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"), "utf8");
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)][1][1];
const key = "commandcode-go-cliproxyapi:quota";
const usage = { five_hour: { percent: 12, used: 1, cap: 8, status: "ok" }, weekly: { percent: 24, used: 2, cap: 8, status: "ok" }, credits_left: 6 };
const response = body => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) });
const turn = () => new Promise(resolve => setImmediate(resolve));

class Node {
  constructor(tag = "div") { this.tagName = tag; this.children = []; this.style = {}; this.disabled = false; }
  append(...nodes) { this.children.push(...nodes); }
  appendChild(node) { this.children.push(node); return node; }
  set textContent(value) { this._text = String(value); this.children = []; }
  get textContent() { return this._text || this.children.map(node => node.textContent || "").join(""); }
  setAttribute() {}
  querySelectorAll(selector) { const found = []; const visit = node => { if (selector === "button" && node.tagName === "button") found.push(node); node.children.forEach(visit); }; visit(this); return found; }
  set className(value) { this._className = value; }
  get className() { return this._className || ""; }
}

class Storage {
  constructor(data = {}, fail = false) { this.data = data; this.fail = fail; this.writes = []; }
  getItem(name) { if (this.fail && name === key) throw new Error("blocked"); return this.data[name] ?? null; }
  setItem(name, value) { if (this.fail && name === key) throw new Error("blocked"); this.data[name] = String(value); this.writes.push({ name, value: String(value) }); }
}

function boot({ local = {}, session = {}, localFail = false, outcomes = [] } = {}) {
  const nodes = new Map([["app", new Node()], ["refresh-all", new Node("button")], ["quota-summary", new Node()]]);
  const localStorage = new Storage({ ...local, "cli-proxy-auth": JSON.stringify({ state: { managementKey: "page-key" } }) }, localFail);
  const sessionStorage = new Storage(session); let call = 0;
  const context = {
    document: { getElementById: id => nodes.get(id), createElement: tag => new Node(tag), documentElement: new Node("html"), body: new Node("body") },
    window: {}, location: { host: "test.local" }, navigator: { userAgent: "quota-test" }, localStorage, sessionStorage,
    fetch: (url, options) => { const body = JSON.parse(options.body || "{}"); if (!body.key_id) return response({ cards: [{ key_id: "account-1", label: "Account one", email: "one@test" }] }); const result = outcomes[call++] ?? { ok: true }; return result.ok ? response({ usage, label: "Account one" }) : response({ error: "private server detail" }); },
    MutationObserver: class { observe() {} }, getComputedStyle: () => ({ getPropertyValue: () => "" }),
    TextEncoder, TextDecoder, atob: globalThis.atob, btoa: globalThis.btoa, Promise, Date, Math, JSON, console,
  };
  context.window = context; vm.runInNewContext(script, context);
  return { nodes, localStorage, sessionStorage };
}

async function ready(page) { for (let i = 0; i < 100; i++) { if (page.nodes.get("app").querySelectorAll("button").length) return; await turn(); } assert.fail("page did not render"); }

test("persists a safe quota cache and restores it in a new VM context", async () => {
  const first = boot(); await ready(first); first.nodes.get("app").querySelectorAll("button")[0].onclick();
  for (let i = 0; i < 20 && !JSON.parse(first.localStorage.data[key] || "{}")["account-1"]; i++) await turn();
  assert.ok(first.localStorage.data[key]);
  const saved = JSON.parse(first.localStorage.data[key]); assert.deepEqual(saved["account-1"].usage, usage); assert.doesNotMatch(first.localStorage.data[key], /managementKey|page-key/);
  const second = boot({ local: first.localStorage.data }); await ready(second);
  assert.doesNotMatch(second.nodes.get("app").textContent, /Not refreshed/); assert.match(second.nodes.get("app").textContent, /Last refreshed/);
});

test("migrates a valid session cache once and survives blocked localStorage", async () => {
  const cached = { "account-1": { usage, label: "Old", fetched_at: new Date().toISOString() } };
  const page = boot({ session: { [key]: JSON.stringify(cached) }, localFail: true }); await ready(page);
  assert.doesNotMatch(page.nodes.get("app").textContent, /Not refreshed/); assert.ok(page.sessionStorage.data[key]);
});

test("renders stale cache, updates on success, and keeps it on failure", async () => {
  const old = { "account-1": { usage, label: "Old", fetched_at: new Date(Date.now() - 8 * 864e5).toISOString() } };
  const page = boot({ local: { [key]: JSON.stringify(old) }, outcomes: [{ ok: false }] }); await ready(page); await turn();
  assert.match(page.nodes.get("app").textContent, /Stale/); page.nodes.get("app").querySelectorAll("button")[0].onclick(); await turn(); await turn();
  assert.match(page.nodes.get("app").textContent, /Quota refresh failed/); assert.equal(JSON.parse(page.localStorage.data[key])["account-1"].fetched_at, old["account-1"].fetched_at);
});
