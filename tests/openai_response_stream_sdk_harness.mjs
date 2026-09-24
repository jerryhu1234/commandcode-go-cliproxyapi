import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const args = Object.fromEntries(process.argv.slice(2).map((v, i, a) => v.startsWith("--") ? [v.slice(2), a[i + 1]] : null).filter(Boolean));
if (!args.sdk || !args.sse) throw new Error("usage: --sdk /path/to/openai/index.mjs --sse /path/to/fixture.sse");
const { default: OpenAI } = await import(pathToFileURL(args.sdk));
const sse = await readFile(args.sse, "utf8");
const server = createServer(async (req, res) => {
  for await (const _ of req) {} // drain request without logging credentials/body
  res.writeHead(200, { "content-type": "text/event-stream" });
  res.end(sse);
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const baseURL = `http://127.0.0.1:${server.address().port}/v1`;
const payload = { model: "qwen3.8-max", input: "hi", stream: true, tools: [{ type: "code_interpreter", container: { type: "auto" } }], tool_choice: "auto" };
try {
  const client = new OpenAI({ apiKey: "fake-key", baseURL });
  const stream = client.responses.stream(payload);
  const seen = [];
  for await (const event of stream) seen.push(event.type);
  const final = await Promise.race([stream.finalResponse(), new Promise((_, reject) => setTimeout(() => reject(new Error("finalResponse timeout")), 5000))]);
  const texts = final.output?.flatMap(item => item.content || []).filter(part => part.type === "output_text").map(part => part.text);
  console.log(JSON.stringify({ ok: true, seen, status: final.status, output_texts: texts, output_types: final.output?.map(item => item.type) }));
} catch (error) {
  console.log(JSON.stringify({ ok: false, error: String(error?.message || error) }));
  process.exitCode = 1;
} finally {
  await new Promise(resolve => server.close(resolve));
}
