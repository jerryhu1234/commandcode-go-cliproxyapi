const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const { TextDecoder, TextEncoder } = require("node:util");

const html = fs.readFileSync(path.join(__dirname, "..", "resources", "quota_page.html"), "utf8");
const source = html.match(/  function auth\(\) \{[\s\S]*?\n  \}/);
assert.ok(source, "auth() must be present in quota_page.html");

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

const state = key => JSON.stringify({ state: { managementKey: key } });

test("auth reads plaintext, v1, and v2 management keys", () => {
  const host = "example.test:9443";
  const userAgent = "quota-test/with-port";
  assert.equal(executeAuth(state("plain-key"), { host, userAgent }), "plain-key");
  assert.equal(executeAuth(encrypt("v1", state("v1-key"), host, userAgent), { host, userAgent }), "v1-key");
  assert.equal(executeAuth(encrypt("v2", state("v2-key"), host, userAgent), { host, userAgent }), "v2-key");
});

test("v2 is independent of user agent and preserves Unicode", () => {
  const host = "管理.example:8443";
  const raw = encrypt("v2", state("密钥-🔐"), host, "encrypting-agent");
  assert.equal(executeAuth(raw, { host, userAgent: "different-agent" }), "密钥-🔐");
});

test("auth safely rejects unavailable or malformed storage", () => {
  const invalid = [
    null,
    "",
    "not json",
    "enc::v1::%%%",
    "enc::v2::%%%",
    "enc::v2::" + Buffer.from("not json").toString("base64"),
    "enc::v3::" + Buffer.from(state("unknown-version")).toString("base64"),
    JSON.stringify({ state: { managementKey: 123 } }),
    JSON.stringify({ state: {} }),
  ];
  for (const raw of invalid) assert.equal(executeAuth(raw), "", String(raw));
  assert.equal(executeAuth(null, { storageError: new Error("denied") }), "");
});
