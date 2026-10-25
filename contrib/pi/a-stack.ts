// a-stack hooks for Pi, version 1. `a-stack hook install --pi` writes this
// file to <agent dir>/extensions/a-stack.ts, and `hook uninstall --pi`
// removes it with the last hooks. Pi 0.87 or later.
//
// It reads the hook list that `hook install --pi` writes next to it,
// <agent dir>/a-stack-hooks.json, in the Claude Code settings shape, and runs
// each `a-stack hook` command with the same JSON on stdin that Claude Code
// sends. It maps the output back to Pi:
//   SessionStart context   -> a hidden message in the model's context
//   additionalContext      -> added to the tool result or to the prompt's run
//   {"decision":"block"}   -> one more model request, at agent_before_settle
// Without hooks in the list it does nothing. A hook that fails or takes too
// long gives no output, so it never stops Pi.
import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

type Hook = { command: string; timeout?: number };

function agentDir(): string {
  return process.env.PI_CODING_AGENT_DIR || join(homedir(), ".pi", "agent");
}

// hooks lists the commands of one event. A missing or broken file means none.
function hooks(event: string): Hook[] {
  try {
    const doc = JSON.parse(readFileSync(join(agentDir(), "a-stack-hooks.json"), "utf8"));
    const groups = doc?.hooks?.[event];
    if (!Array.isArray(groups)) return [];
    return groups.flatMap((g: any) => (Array.isArray(g?.hooks) ? g.hooks : [])).filter((h: any) => typeof h?.command === "string");
  } catch {
    return [];
  }
}

// run runs every hook of an event and returns their stdout.
async function run(event: string, ctx: ExtensionContext, extra: Record<string, unknown> = {}): Promise<string[]> {
  const input = JSON.stringify({ hook_event_name: event, session_id: ctx.sessionManager.getSessionId(), cwd: ctx.cwd, ...extra });
  return Promise.all(
    hooks(event).map(
      (h) =>
        new Promise<string>((resolve) => {
          const child = execFile("/bin/sh", ["-c", h.command], { timeout: (h.timeout ?? 5) * 1000, maxBuffer: 1 << 20 }, (err, stdout) =>
            resolve(err ? "" : stdout),
          );
          child.stdin?.on("error", () => {});
          child.stdin?.end(input);
        }),
    ),
  );
}

function parse(out: string): any {
  try {
    return JSON.parse(out);
  } catch {
    return undefined;
  }
}

// context collects hookSpecificOutput.additionalContext; plain text counts as
// context too, as in Claude Code.
function context(outputs: string[]): string {
  return outputs
    .map((out) => (out.trimStart().startsWith("{") ? parse(out)?.hookSpecificOutput?.additionalContext ?? "" : out.trim()))
    .filter(Boolean)
    .join("\n");
}

function blockReason(outputs: string[]): string {
  for (const out of outputs) {
    const doc = parse(out);
    if (doc?.decision === "block" && typeof doc.reason === "string") return doc.reason;
  }
  return "";
}

export default function (pi: ExtensionAPI) {
  let stopHookActive = false; // true right after the Stop guard made Pi go on

  const remind = async (source: string, ctx: ExtensionContext) => {
    const text = context(await run("SessionStart", ctx, { source }));
    if (text) pi.sendMessage({ customType: "a-stack", content: text, display: false }, { deliverAs: ctx.isIdle() ? "nextTurn" : "steer" });
  };

  // SessionStart: start, reload, /new, /resume, /fork, and after compaction.
  pi.on("session_start", async (event, ctx) => {
    stopHookActive = false;
    await remind(event.reason === "new" ? "clear" : event.reason, ctx);
  });
  pi.on("session_compact", async (_event, ctx) => remind("compact", ctx));

  // UserPromptSubmit: records working, and adds the mail nudge to the run.
  pi.on("before_agent_start", async (event, ctx) => {
    if (event.messageType !== "user") return; // a continuation, or another extension's message
    stopHookActive = false;
    const note = context(await run("UserPromptSubmit", ctx, { prompt: event.prompt }));
    if (note) return { message: { customType: "a-stack", content: note, display: false } };
  });

  // PostToolUse: records working, and adds the mail nudge to the tool result.
  pi.on("tool_result", async (event, ctx) => {
    if (event.parentToolCallId) return; // nested calls of a codemode script
    const note = context(await run("PostToolUse", ctx, { tool_name: event.toolName }));
    if (note) return { content: [...event.content, { type: "text", text: note }], structuredContent: event.structuredContent };
  });

  // PermissionRequest: an extension dialog waits for the user (Pi asks for no
  // permissions itself).
  pi.on("ui_prompt_start", async (_event, ctx) => {
    await run("PermissionRequest", ctx);
  });

  // Stop: the Stop guard may ask for one more model request; otherwise the
  // hook records idle. event.context.canContinue is false here, because the
  // last message is the assistant's; the custom message makes it go on.
  pi.on("agent_before_settle", async (event, ctx) => {
    if (event.continue || event.outcome !== "completed") return;
    const reason = blockReason(await run("Stop", ctx, { stop_hook_active: stopHookActive }));
    if (!reason || stopHookActive) {
      stopHookActive = false;
      return;
    }
    stopHookActive = true;
    return { entries: [{ type: "custom_message", customType: "a-stack", content: reason, display: true }], continue: true };
  });
}
