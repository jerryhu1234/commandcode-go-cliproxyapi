const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const { TextDecoder, TextEncoder } = require("node:util");

const html = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"), "utf8");
const source = html.match(/  function authStatus\(\) \{[\s\S]*?\n  \}/);
assert.ok(source, "authStatus() must be present in quota_page.html");
const requestSource = html.match(/  function authStatus\(\) \{[\s\S]*?\n  \}\n  function text\(/);
assert.ok(requestSource, "auth/request functions must be present in quota_page.html");
assert.match(html, /function auth\(\) \{[\s\S]*?pageAuthKey/);
assert.match(html, /Management API rejected these credentials \(401\)/);
assert.match(html, /Management API denied access \(403\)/);

function encrypt(version, value, host, userAgent) {
  const prefix = `enc::${version}::`;
  const seedText = version === "v2"
    ? `cli-proxy-api-webui::secure-storage|v2|${host}`
    : `cli-proxy-api-webui::secure-storage|${host}|${userAgent}`;
  const bytes = new TextEncoder().encode(value);
  const seed = new TextEncoder().encode(seedText);
  for (let i = 0; i < bytes.length; i++) bytes[i] ^= seed[i % seed.length];
  return prefix + Buffer.from(bytes).toString("base64");
}

function executeAuth(raw, { host = "localhost:8317", userAgent = "quota-test/1.0", storageError } = {}) {
  const context = {
    atob: globalThis.atob,
    TextDecoder,
    TextEncoder,
    location: { host },
    navigator: { userAgent },
    localStorage: {
      getItem(key) {
        assert.equal(key, "cli-proxy-auth");
        if (storageError) throw storageError;
        return raw;
      },
    },
  };
  return vm.runInNewContext(`(${source[0]})`, context)();
}

function executeRequest({ raw, responses, storageError, storageReads } = {}) {
  const counters = { reads: 0, calls: 0 };
  const context = {
    atob: globalThis.atob, TextDecoder, TextEncoder,
    location: { host: "localhost:8317" }, navigator: { userAgent: "quota-test/1.0" },
    localStorage: { getItem() { counters.reads++; if (storageReads) return storageReads[counters.reads - 1]; if (storageError) throw storageError; return raw; } },
    fetch() {
      counters.calls++;
      const item = responses[counters.calls - 1] || responses[responses.length - 1];
      return Promise.resolve({ ok: item.ok, status: item.status, json: () => Promise.resolve(item.body) });
    },
  };
  const functions = requestSource[0].replace(/\n  function text\($/, "");
  const run = `(function() { let pageAuthKey = ""; const endpoint = "/quota"; ${functions} return {authStatus, auth, request, stats: () => ({reads: counters.reads, calls: counters.calls})}; })()`;
  context.counters = counters;
  return vm.runInNewContext(run, context);
}

const state = key => JSON.stringify({ state: { managementKey: key } });

test("auth reads plaintext, v1, and v2 management keys", () => {
  const host = "example.test:9443";
  const userAgent = "quota-test/with-port";
  assert.deepEqual(executeAuth(state("plain-key"), { host, userAgent }), { key: "plain-key", status: "ok" });
  assert.deepEqual(executeAuth(encrypt("v1", state("v1-key"), host, userAgent), { host, userAgent }), { key: "v1-key", status: "ok" });
  assert.deepEqual(executeAuth(encrypt("v2", state("v2-key"), host, userAgent), { host, userAgent }), { key: "v2-key", status: "ok" });
});

test("v2 is independent of user agent and preserves Unicode", () => {
  const host = "管理.example:8443";
  const raw = encrypt("v2", state("密钥-🔐"), host, "encrypting-agent");
  assert.deepEqual(executeAuth(raw, { host, userAgent: "different-agent" }), { key: "密钥-🔐", status: "ok" });
});

test("auth safely rejects unavailable or malformed storage", () => {
  const cases = [
    [null, "missing"],
    ["", "missing"],
    ["not json", "decode-invalid"],
    ["enc::v1::%%%", "decode-invalid"],
    ["enc::v2::%%%", "decode-invalid"],
    ["enc::v2::" + Buffer.from("not json").toString("base64"), "decode-invalid"],
    ["enc::v3::" + Buffer.from(state("unknown-version")).toString("base64"), "unsupported-format"],
    [JSON.stringify({ state: { managementKey: 123 } }), "key-missing"],
    [JSON.stringify({ state: {} }), "key-missing"],
  ];
  for (const [raw, status] of cases) assert.deepEqual(executeAuth(raw), { key: "", status }, String(raw));
  assert.deepEqual(executeAuth(null, { storageError: new Error("denied") }), { key: "", status: "storage-blocked" });
});

test("v1 uses Unicode user agent and host including port", () => {
  const host = "管理.example:8443";
  const userAgent = "浏览器/🔐";
  assert.deepEqual(executeAuth(encrypt("v1", state("v1-unicode"), host, userAgent), { host, userAgent }), { key: "v1-unicode", status: "ok" });
});

test("request uses the page-local key and reads storage only during explicit auth", async () => {
  const page = executeRequest({ raw: state("vm-test-key"), responses: [{ ok: true, status: 200, body: { cards: [] } }] });
  assert.equal(page.auth(), "vm-test-key");
  const result = await page.request({});
  assert.deepEqual(result, { cards: [] });
  assert.deepEqual(page.stats(), { reads: 1, calls: 1 });
});

test("request never fetches without a confirmed key", async () => {
  const page = executeRequest({ raw: null, responses: [{ ok: true, status: 200, body: { cards: [] } }] });
  await assert.rejects(page.request({}), /credentials were not found/);
  assert.deepEqual(page.stats(), { reads: 0, calls: 0 });
});

test("401 and 403 messages omit upstream response bodies", async () => {
  for (const status of [401, 403]) {
    const page = executeRequest({ raw: state("safe-test-key"), responses: [{ ok: false, status, body: { error: "LEAKED-UPSTREAM-BODY" } }] });
    page.auth();
    await assert.rejects(page.request({}), error => {
      assert.match(error.message, new RegExp(String(status)));
      assert.doesNotMatch(error.message, /LEAKED-UPSTREAM-BODY/);
      assert.doesNotMatch(error.message, /safe-test-key/);
      return true;
    });
  }
});

test("a later explicit auth read can recover without initializing another request", () => {
  const page = executeRequest({ storageReads: [null, state("recovered-key")], responses: [{ ok: true, status: 200, body: {} }] });
  assert.equal(page.authStatus().status, "missing");
  assert.equal(page.auth(), "recovered-key");
  assert.deepEqual(page.stats(), { reads: 2, calls: 0 });
});
