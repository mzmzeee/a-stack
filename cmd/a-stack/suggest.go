package main

import (
	"flag"
	"io"
	"sort"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// Errors for a wrong command line name what is wrong and, when one is close,
// what was probably meant.

// distance is the edit distance of a and b, where swapping two neighbouring
// letters counts as one edit, as in "tsak" for "task".
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}

	for j := range d[0] {
		d[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}

	return d[len(a)][len(b)]
}

// nearest returns the candidate closest to word, or "" when none is within
// 2 edits (1 for words of up to 4 letters).
func nearest(word string, candidates []string) string {
	limit := 2
	if len(word) <= 4 {
		limit = 1
	}

	best, bestDistance := "", limit+1
	for _, c := range candidates {
		if d := distance(word, c); d < bestDistance && c != word {
			best, bestDistance = c, d
		}
	}

	return best
}

// short keeps a word from the command line short enough for an error message.
func short(word string) string {
	if r := []rune(word); len(r) > 40 {
		return string(r[:40]) + "…"
	}

	return word
}

func unknownFlag(fs *flag.FlagSet, name string) error {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > 1 {
			names = append(names, f.Name)
		}
	})

	message := "unknown flag --" + short(name)
	if guess := nearest(name, names); guess != "" {
		message += " (did you mean --" + guess + "?)"
	}

	return invalid(message + "; use --help")
}

// commandNames lists every command and every group of commands, such as
// "task" and "task attention".
func commandNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, g := range helpGroups {
		for _, r := range g.rows {
			words := strings.Fields(r.command)
			for i := 1; i <= len(words); i++ {
				name := strings.Join(words[:i], " ")
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
		}
	}

	return names
}

func didYouMean(command string) string {
	if guess := nearest(command, commandNames()); guess != "" {
		return " (did you mean " + guess + "?)"
	}

	return ""
}

// unknownCommand explains words that are not a command. A group word alone,
// such as `a-stack task`, prints the commands of the group like `a-stack help
// task` and succeeds; with flags it is an error.
func unknownCommand(words []string, seen map[string]bool, stdout io.Writer) error {
	command := strings.Join(words, " ")
	if text := prefixHelp(command); text != "" {
		if len(seen) == 0 {
			_, err := io.WriteString(stdout, text)
			return err
		}

		var next []string
		for _, name := range commandNames() {
			if rest, ok := strings.CutPrefix(name, command+" "); ok && !strings.Contains(rest, " ") {
				next = append(next, rest)
			}
		}

		return invalid(command + " needs one more word: " + strings.Join(next, ", ") + "; run a-stack help " + command)
	}

	for n := len(words) - 1; n > 0; n-- {
		known := strings.Join(words[:n], " ")
		if !knownCommand(known) {
			continue
		}

		stray := short(words[n])

		message := "unexpected argument " + stray + " for " + known
		accepted := acceptedFlags(known)
		switch {
		case strings.Contains(accepted, " task ") && !seen["task"]:
			message += " (did you mean --task " + stray + "?)"
		case strings.Contains(accepted, " body "):
			message += "; quote text that has spaces, as in --body 'two words'"
		}

		return invalid(message)
	}

	return invalid("unknown command " + short(command) + didYouMean(command) + "; use --help")
}

// validateCommandFlags rejects flags that the command does not accept. It names the
// flag and the accepted flag it is closest to.
func validateCommandFlags(command string, seen map[string]bool) error {
	allowed := acceptedFlags(command)

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}

	sort.Strings(names)

	where := "a-stack " + command
	if command == "" {
		where = "a-stack without a command"
	}

	for _, name := range names {
		if strings.Contains(allowed, " "+name+" ") {
			continue
		}

		message := "--" + name + " does not apply to " + where
		if guess := nearest(name, strings.Fields(allowed)); guess != "" {
			message += "; did you mean --" + guess + "?"
		}

		return invalid(message + "; see a-stack help " + command)
	}

	return nil
}

// checkIDs rejects an ID flag whose value cannot be an ID. --agent and
// --assign also take an Agent name, which the CLI looks up in the Session
// (agentIDs); the hook commands take only an Agent ID.
func checkIDs(command string, seen map[string]bool, l lifecycleFlags, t taskFlags) error {
	type idFlag struct{ flag, value, kind, list string }

	agentKind := "an Agent ID or name"
	if isHookCommand(command) {
		agentKind = "an Agent ID"
	}

	ids := []idFlag{
		{"workspace", l.workspace, "a Workspace ID", "a-stack workspace list"},
		{"session", l.session, "a Session ID", "a-stack session list"},
		{"agent", l.agent, agentKind, "a-stack agent list"},
		{"task", t.task, "a Task ID", "a-stack task list"},
		{"from", t.from, "a Task ID", "a-stack task list"},
		{"to", t.to, "a Task ID", "a-stack task list"},
		{"reference", t.reference, "a reference ID", "a-stack task references"},
		{"question-id", t.questionID, "a question ID", "a-stack task show"},
	}
	if command == "task create" {
		for _, after := range l.afters {
			ids = append(ids, idFlag{"after", after, "a Task ID", "a-stack task list"})
		}
	}

	for _, assign := range t.assign {
		ids = append(ids, idFlag{"assign", assign, agentKind, "a-stack agent list"})
	}

	for _, id := range ids {
		if !seen[id.flag] || stack.ValidID(id.value) {
			continue
		}

		name := (id.flag == "agent" || id.flag == "assign") && looksLikeAgentName(id.value)
		if name && agentKind != "an Agent ID" {
			continue
		}

		what := short(id.value) + " is not one"
		if id.value == "" {
			what = "it is empty"
		} else if name && len(id.value) < 40 {
			what = id.value + " looks like a name"
		}

		return invalid("--" + id.flag + " takes " + id.kind + "; " + what + "; see " + id.list)
	}

	return nil
}

func looksLikeAgentName(s string) bool {
	if s == "" || len(s) > 64 || !strings.ContainsAny(s[:1], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") {
		return false
	}

	return strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") == ""
}
