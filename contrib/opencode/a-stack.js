// a-stack hooks for OpenCode, version 1. `a-stack hook install --opencode`
// writes this file to <config dir>/plugins/a-stack.js, and
// `hook uninstall --opencode` removes it with the last hooks. OpenCode 1.19
// or later, on Linux and macOS.
//
// It reads the hook list that `hook install --opencode` writes next to its
// folder, <config dir>/a-stack-hooks.json, in the Claude Code settings shape,
// and runs each `a-stack hook` command with the same JSON on stdin that Claude
// Code sends. It maps the output back to OpenCode:
//   SessionStart context   -> the session's system prompt, again after compaction
//   additionalContext      -> added to the tool output, or hidden in the prompt
//   {"decision":"block"}   -> one new turn when the session goes idle
// Subagent (child) sessions get none of this. Without hooks in the list it
// does nothing. A hook that fails or takes too long gives no output, so it
// never stops OpenCode.
import { execFile } from "node:child_process"
import { randomBytes } from "node:crypto"
import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

const list = fileURLToPath(new URL("../a-stack-hooks.json", import.meta.url))

// hooks lists the commands of one event. A missing or broken file means none.
function hooks(event) {
  try {
    const groups = JSON.parse(readFileSync(list, "utf8"))?.hooks?.[event]
    if (!Array.isArray(groups)) return []
    return groups.flatMap((g) => (Array.isArray(g?.hooks) ? g.hooks : [])).filter((h) => typeof h?.command === "string")
  } catch {
    return []
  }
}

// run runs every hook of an event and returns their stdout.
function run(event, input) {
  const stdin = JSON.stringify({ hook_event_name: event, ...input })
  return Promise.all(
    hooks(event).map(
      (h) =>
        new Promise((resolve) => {
          const child = execFile("/bin/sh", ["-c", h.command], { timeout: (h.timeout ?? 5) * 1000, maxBuffer: 1 << 20 }, (err, stdout) =>
            resolve(err ? "" : String(stdout)),
          )
          child.stdin?.on("error", () => {})
          child.stdin?.end(stdin)
        }),
    ),
  )
}

function parse(out) {
  try {
    return JSON.parse(out)
  } catch {
    return undefined
  }
}

// context collects hookSpecificOutput.additionalContext; plain text counts as
// context too, as in Claude Code.
function context(outputs) {
  return outputs
    .map((out) => (out.trimStart().startsWith("{") ? (parse(out)?.hookSpecificOutput?.additionalContext ?? "") : out.trim()))
    .filter(Boolean)
    .join("\n")
}

function blockReason(outputs) {
  for (const out of outputs) {
    const doc = parse(out)
    if (doc?.decision === "block" && typeof doc.reason === "string") return doc.reason
  }
  return ""
}

// partID makes a part ID in OpenCode's format: "prt_", 12 hex digits of the
// time, 14 random characters.
let counter = 0
function partID() {
  const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
  const time = (BigInt(Date.now()) * 4096n + BigInt(++counter % 4096)).toString(16).padStart(12, "0").slice(-12)
  return "prt_" + time + Array.from(randomBytes(14), (b) => chars[b % 62]).join("")
}

async function AStack({ client, directory }) {
  const roots = new Map() // session -> Promise<boolean>: a session that is no subagent's
  const reminders = new Map() // session -> Promise of its SessionStart context
  const turns = new Map() // session -> agent and model of its last prompt
  const stopActive = new Set() // the Stop guard just made the session go on
  const continuing = new Set() // the session's next prompt is the Stop guard's

  const input = (sessionID, extra) => ({ session_id: sessionID, cwd: directory, ...extra })
  const isRoot = (sessionID) => {
    if (!sessionID) return Promise.resolve(false)
    if (!roots.has(sessionID)) {
      // A failed lookup, thrown or returned as an error, counts as a
      // subagent's session: the hooks skip a session rather than act for a
      // subagent. The next session.updated event sets the answer again.
      roots.set(
        sessionID,
        client.session.get({ path: { id: sessionID } }).then(
          (r) => Boolean(r?.data) && !r.data.parentID,
          () => false,
        ),
      )
    }
    return roots.get(sessionID)
  }
  // completed reports whether the session's last message is an answer that
  // ended without an error or an abort.
  const completed = (sessionID) =>
    client.session.messages({ path: { id: sessionID }, query: { limit: 1 } }).then(
      (r) => {
        const last = r?.data?.at(-1)?.info
        return last?.role === "assistant" && !last.error
      },
      () => false,
    )
  const reminder = (sessionID, source) => {
    if (!reminders.has(sessionID)) reminders.set(sessionID, run("SessionStart", input(sessionID, { source })).then(context))
    return reminders.get(sessionID)
  }

  return {
    event: async ({ event }) => {
      const p = event?.properties ?? {}
      const sessionID = p.sessionID ?? p.info?.id
      switch (event?.type) {
        case "session.created":
        case "session.updated":
          if (p.info?.id) roots.set(p.info.id, Promise.resolve(!p.info.parentID))
          return
        case "session.compacted":
          // SessionStart again before the next request, as after compaction in Claude Code.
          reminders.delete(sessionID)
          if (await isRoot(sessionID)) reminder(sessionID, "compact")
          return
        case "permission.asked":
        case "question.asked":
          // PermissionRequest: the Agent waits for the user, also for a subagent.
          await run("PermissionRequest", input(sessionID))
          return
        case "session.idle": {
          // Stop: the Stop guard may start one new turn; otherwise the hook
          // records idle. After an error or an abort it only records idle.
          if (!(await isRoot(sessionID))) return
          const active = stopActive.has(sessionID) || !(await completed(sessionID))
          const reason = blockReason(await run("Stop", input(sessionID, { stop_hook_active: active })))
          if (!reason || active) {
            stopActive.delete(sessionID)
            return
          }
          stopActive.add(sessionID)
          continuing.add(sessionID)
          const turn = turns.get(sessionID) ?? {}
          const sent = await client.session
            .promptAsync({ path: { id: sessionID }, body: { ...turn, parts: [{ type: "text", text: reason }] } })
            .catch(() => undefined)
          if (!sent || sent.error) continuing.delete(sessionID)
          return
        }
      }
    },

    // SessionStart: the reminder goes into the system prompt of each request.
    "experimental.chat.system.transform": async ({ sessionID }, output) => {
      if (!(await isRoot(sessionID))) return
      const text = await reminder(sessionID, "startup")
      if (text) output.system.push(text)
    },

    // UserPromptSubmit: records working, and adds the mail nudge to the prompt.
    "chat.message": async ({ sessionID }, output) => {
      if (!(await isRoot(sessionID))) return
      const { agent, model } = output.message ?? {}
      if (agent && model) turns.set(sessionID, { agent, model: { providerID: model.providerID, modelID: model.modelID } })
      if (continuing.delete(sessionID)) return // the Stop guard's own prompt
      stopActive.delete(sessionID)
      await reminder(sessionID, "startup") // its first mail check comes first
      const prompt = output.parts.map((part) => (part.type === "text" && !part.synthetic ? part.text : "")).join("")
      const note = context(await run("UserPromptSubmit", input(sessionID, { prompt })))
      if (note) output.parts.push({ id: partID(), sessionID, messageID: output.message.id, type: "text", text: note, synthetic: true })
    },

    // PostToolUse: records working, and adds the mail nudge to the tool output.
    "tool.execute.after": async ({ sessionID, tool }, output) => {
      if (!(await isRoot(sessionID))) return
      const note = context(await run("PostToolUse", input(sessionID, { tool_name: tool })))
      if (note && typeof output?.output === "string") output.output += "\n\n" + note
    },
  }
}

export default { id: "a-stack", server: AStack }
