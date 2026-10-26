// Tests of the OpenCode plugin with a fake OpenCode client: node --test.
// The plugin runs from a copy next to a hook list whose hooks log their input.
import { test } from "node:test"
import assert from "node:assert/strict"
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const dir = mkdtempSync(join(tmpdir(), "a-stack-opencode-"))
mkdirSync(join(dir, "plugins"))
copyFileSync(fileURLToPath(new URL("a-stack.js", import.meta.url)), join(dir, "plugins", "a-stack.js"))
const log = join(dir, "events.log")
const command = `cat >> '${log}'; echo >> '${log}'`
const events = ["SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"]
writeFileSync(join(dir, "a-stack-hooks.json"), JSON.stringify({ hooks: Object.fromEntries(events.map((e) => [e, [{ hooks: [{ type: "command", command }] }]])) }))
const plugin = (await import(pathToFileURL(join(dir, "plugins", "a-stack.js")).href)).default

// ran returns the events whose hooks ran for a session that session.get
// answers with get.
async function ran(get) {
  writeFileSync(log, "")
  const client = { session: { get, messages: async () => ({ data: [] }), promptAsync: async () => ({}) } }
  const hooks = await plugin.server({ client, directory: dir })
  await hooks["tool.execute.after"]({ sessionID: "ses_1", tool: "bash" }, { output: "" })
  await hooks["experimental.chat.system.transform"]({ sessionID: "ses_1" }, { system: [] })
  return readFileSync(log, "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line).hook_event_name)
}

test("a top-level session gets the hooks", async () => {
  assert.deepEqual(await ran(async () => ({ data: { id: "ses_1" } })), ["PostToolUse", "SessionStart"])
})

test("a subagent session gets none", async () => {
  assert.deepEqual(await ran(async () => ({ data: { id: "ses_1", parentID: "ses_0" } })), [])
})

test("a failed session lookup counts as a subagent session", async () => {
  assert.deepEqual(await ran(async () => ({ error: { name: "NotFound" } })), [])
  assert.deepEqual(await ran(async () => Promise.reject(new Error("down"))), [])
})
