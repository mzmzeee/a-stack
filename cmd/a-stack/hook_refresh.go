package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const hookMigrationNotice = "Hook commands were upgraded. Each Agent must enroll from its own native session: run me hook enroll --claude (or --codex, --pi, --opencode), or me herdr bind. If the native ID is unavailable, add --native-session ID. Hooks stay silent until enrollment; never use the a-stack Session ID."

// Only the complete command grammar emitted by released installers is ours.
// Shell wrappers, extra flags and other binaries are deliberately left alone.
var legacyHookPattern = func() *regexp.Regexp {
	q := `'(?:[^']|'\\'')*'`
	id := `[A-Za-z0-9_-]{43}`
	return regexp.MustCompile(`^(` + q + `) hook(?: --state-dir ` + q + `)? --credential-file (` + q + `) --workspace ` + id + ` --session ` + id + ` --agent (` + id + `)(?: --runtime-id ` + q + `)?(?: --no-stop-guard)?(?: --no-mail-nudge)? --worktree (` + q + `)$`)
}()

func legacyHookCommand(command string) bool {
	if strings.ContainsAny(command, "\r\n\x00") {
		return false
	}

	match := legacyHookPattern.FindStringSubmatch(command)
	if match == nil {
		return false
	}

	exe := strings.ReplaceAll(match[1][1:len(match[1])-1], `'\''`, `'`)
	current, _ := os.Executable()

	return filepath.IsAbs(filepath.FromSlash(exe)) && (filepath.Base(exe) == "a-stack" || filepath.Base(exe) == "a-stack.exe" || filepath.FromSlash(exe) == current)
}

func refreshHookCommands(file, harness string) (bool, error) {
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false, nil
	}

	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		entry, err := os.Lstat(dir)
		if err != nil {
			return false, err
		}

		if entry.Mode()&os.ModeSymlink != 0 {
			return false, nil
		}

		if filepath.Dir(dir) == dir {
			break
		}
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}

	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return false, nil
	}

	var events map[string]json.RawMessage
	if json.Unmarshal(doc["hooks"], &events) != nil {
		return false, nil
	}

	changed := false
	for _, event := range hookEvents {
		var groups []map[string]json.RawMessage
		if json.Unmarshal(events[event.event], &groups) != nil {
			continue
		}

		eventChanged := false
		for _, group := range groups {
			var hooks []map[string]json.RawMessage
			if json.Unmarshal(group["hooks"], &hooks) != nil {
				continue
			}

			groupChanged := false
			for _, hook := range hooks {
				var kind, command string
				if json.Unmarshal(hook["type"], &kind) != nil || kind != "command" || json.Unmarshal(hook["command"], &command) != nil || !legacyHookCommand(command) {
					continue
				}

				id, _ := installedHookIdentity(command, harness)
				if err := markHookPending(id); err != nil {
					return false, err
				}

				hook["command"], _ = json.Marshal(command + " --harness " + harness)
				groupChanged = true
			}

			if groupChanged {
				group["hooks"], _ = json.Marshal(hooks)
				eventChanged = true
			}
		}

		if eventChanged {
			events[event.event], _ = json.Marshal(groups)
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	doc["hooks"], _ = json.Marshal(events)

	data, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}

	if err = writeAtomic(file, append(data, '\n'), info.Mode().Perm()); err != nil {
		return false, err
	}

	return true, nil
}
