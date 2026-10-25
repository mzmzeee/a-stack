package main

import (
	"encoding/json"
	"flag"
	"io"
	"strings"
)

// helpGroup is one section of the top-level help. Rows name a command, or a
// display text for the Operator shortcuts; the per-command help lists flags
// from acceptedFlags, so it follows the flag validation.
type helpGroup struct {
	title string
	rows  []helpRow
}
type helpRow struct{ command, display, text string }

var helpGroups = []helpGroup{
	{"Start here", []helpRow{
		{"", "(no command)", "Open your last Session; start the service if needed"},
		{"new", "new [NAME]", "Create a Session for this folder and open it"},
		{"", "-r, --resume", "Pick a Session in the terminal and open it"},
		{"open", "", "Open one Session in the browser by its IDs"},
	}},
	{"Agent skill", []helpRow{
		{"skill install", "", "Install the agent skill in ~/.claude and ~/.agents"},
		{"skill uninstall", "", "Remove the agent skill from ~/.claude and ~/.agents"},
	}},
	{"Service", []helpRow{
		{"serve", "", "Run the service in the foreground"},
		{"health", "", "Check the running service"},
		{"version", "", "Print the version"},
		{"update", "", "Install the latest release in place of this binary"},
		{"storage status", "", "Show database and WAL sizes"},
		{"storage backup", "", "Write a checked backup of the database"},
	}},
	{"Workspaces, Sessions and Agents", []helpRow{
		{"workspace create", "", "Register a project folder as a Workspace"},
		{"workspace list", "", "List Workspaces"},
		{"session create", "", "Create a Session in a Workspace"},
		{"session list", "", "List the Sessions of a Workspace"},
		{"session inspect", "", "Show one Session"},
		{"session close", "", "Close a Session"},
		{"session reopen", "", "Reopen a closed Session"},
		{"session appoint", "", "Appoint the Orchestrator of a Session"},
		{"session snapshot", "", "Show a Session's revisions and counts"},
		{"session page", "", "Read one page of a Session collection"},
		{"agent register", "", "Register an Agent and write its credential"},
		{"agent list", "", "List the Agents of a Session"},
		{"agent attach", "", "Bind a running instance to an Agent"},
		{"agent rebind", "", "Replace an Agent's binding or credential"},
		{"agent observe", "", "Record an Agent's runtime state"},
	}},
	{"Tasks", []helpRow{
		{"task create", "", "Create a Task"},
		{"task list", "", "List the Tasks of a Session"},
		{"task show", "", "Show one Task with its texts"},
		{"task edit", "", "Change a Task's title, description or ticket"},
		{"task assign", "", "Assign an Agent to a Task"},
		{"task unassign", "", "Remove an Agent from a Task"},
		{"task status", "", "Set a Task's progress"},
		{"task reopen", "", "Reopen a finished or cancelled Task"},
		{"task archive", "", "Archive a finished or cancelled Task"},
		{"task unarchive", "", "Undo task archive"},
		{"task report", "", "Write a Task's report"},
		{"task history", "", "List a Task's changes"},
		{"task assignees", "", "List a Task's Agents"},
		{"task references", "", "List a Task's file references"},
		{"task dependencies", "", "List a Task's dependencies"},
		{"task attention set", "", "Ask the Operator a Needs you question"},
		{"task attention resolve", "", "Resolve a Needs you question"},
		{"task reference add", "", "Add a file reference to a Task"},
		{"task reference remove", "", "Remove a file reference"},
		{"dependency add", "", "Make one Task depend on another"},
		{"dependency remove", "", "Remove a dependency"},
		{"dependency history", "", "List the changes of the Task graph"},
	}},
	{"Task mail", []helpRow{
		{"mail send", "", "Send Task mail to the other assignees"},
		{"mail read", "", "Read the mail of one Task"},
		{"mail check", "", "Check new mail on all your Tasks"},
	}},
	{"Notices", []helpRow{
		{"notifications status", "", "Show Herdr notice delivery"},
		{"notifications drain", "", "Send pending Herdr notices now"},
	}},
}

const helpTail = `Common flags:
  --state-dir DIR          State directory (default ~/.local/state/a-stack)
  --as KIND                Credential kind: operator, agent or browser
  --credential-file FILE   Credential file (default: operator.json)
  --json                   Machine-readable output
  -h, --help               Show help

Examples:
  a-stack new
  a-stack task list --workspace ID --session ID
  a-stack help task create

Run a-stack help <command> or a-stack <command> --help for details.
Docs: https://github.com/mzmzeee/a-stack
`

// requiredFlags marks the flags a command cannot run without. Flags that are
// required wherever a command accepts them are in alwaysRequired.
var alwaysRequired = " operation-id expected-revision "
var requiredFlags = map[string]string{
	"open": "workspace session", "storage backup": "destination",
	"workspace create": "path", "session create": "workspace name",
	"session list": "workspace", "agent register": "name new-credential-file",
	"session appoint": "agent", "agent attach": "agent", "agent rebind": "agent new-credential-file",
	"agent observe": "agent runtime-id runtime-state", "task assign": "agent", "task unassign": "agent",
	"task create": "title", "task status": "progress", "task reference add": "location",
	"task reference remove": "reference", "dependency add": "from to", "dependency remove": "from to",
	"session page": "page-kind",
}

func flagRequired(command, name string) bool {
	if strings.Contains(alwaysRequired, " "+name+" ") || strings.Contains(" "+requiredFlags[command]+" ", " "+name+" ") {
		return true
	}
	scoped := isTaskCommand(command) || isMailCommand(command) || isNotificationCommand(command) || strings.HasPrefix(command, "agent ") || strings.HasPrefix(command, "session ")
	switch name {
	case "workspace":
		return scoped && command != "session list"
	case "session":
		return scoped && command != "session list"
	case "task":
		return isTaskCommand(command) && command != "session page" || command == "mail send" || command == "mail read"
	}
	return false
}

func knownCommand(command string) bool {
	for _, g := range helpGroups {
		for _, r := range g.rows {
			if r.command == command && command != "" {
				return true
			}
		}
	}
	return false
}

// writeHelp prints help for the words before --help, or after `help`. With
// --json it prints the machine list of commands and flags, whatever the words.
func writeHelp(fs *flag.FlagSet, words []string, machine bool, stdout io.Writer) error {
	if machine {
		flags := []string{}
		fs.VisitAll(func(f *flag.Flag) {
			if len(f.Name) == 1 {
				flags = append(flags, "-"+f.Name)
			} else {
				flags = append(flags, "--"+f.Name)
			}
		})
		return json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]any{"commands": append([]string{"serve", "open", "version", "health", "storage status", "storage backup", "notifications status", "notifications drain"}, append(append(append(lifecycleCommands, taskCommands...), mailCommands...), "new", "skill install", "skill uninstall", "update")...), "flags": flags}})
	}
	command := strings.Join(words, " ")
	var text string
	switch {
	case command == "":
		text = topHelp()
	case knownCommand(command):
		text = commandHelp(fs, command)
	default:
		text = prefixHelp(command)
		if text == "" {
			return invalid("no help for " + short(command) + didYouMean(command) + "; use --help")
		}
	}
	_, err := io.WriteString(stdout, text)
	return err
}

func rowName(r helpRow) string {
	if r.display != "" {
		return r.display
	}
	return r.command
}

func topHelp() string {
	var b strings.Builder
	b.WriteString("Keep an eye on your AI coding agents.\n\nUsage: a-stack <command> [flags]\n")
	for _, g := range helpGroups {
		b.WriteString("\n" + g.title + ":\n")
		for _, r := range g.rows {
			b.WriteString(column(rowName(r), r.text))
		}
	}
	b.WriteString("\n" + helpTail)
	return b.String()
}

// prefixHelp lists the commands that start with the given words, such as
// `a-stack help task` or `a-stack task attention --help`.
func prefixHelp(prefix string) string {
	var b strings.Builder
	for _, g := range helpGroups {
		for _, r := range g.rows {
			if r.command != "" && strings.HasPrefix(r.command+" ", prefix+" ") {
				b.WriteString(column(r.command, r.text))
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Usage: a-stack " + prefix + " <command> [flags]\n\nCommands:\n" + b.String() + "\nRun a-stack help " + prefix + " <command> for its flags.\n"
}

func commandHelp(fs *flag.FlagSet, command string) string {
	text := ""
	for _, g := range helpGroups {
		for _, r := range g.rows {
			if r.command == command && r.command != "" {
				text = r.text
			}
		}
	}
	usage := "a-stack " + command
	if command == "new" {
		usage += " [NAME]"
	}
	accepted := acceptedFlags(command)
	var own, common strings.Builder
	for _, name := range strings.Fields(accepted) {
		if !strings.Contains(commonFlags, " "+name+" ") {
			own.WriteString(column(flagLabel(fs, name), flagText(fs, command, name)))
		}
	}
	for _, name := range strings.Fields(commonFlags) {
		if strings.Contains(accepted, " "+name+" ") {
			common.WriteString(column(flagLabel(fs, name), flagText(fs, command, name)))
		}
	}
	out := text + ".\n\nUsage: " + usage + " [flags]\n"
	if own.Len() > 0 {
		out += "\nFlags:\n" + own.String()
	}
	return out + "\nCommon flags:\n" + common.String()
}

// commonFlags are listed last, in this order, in each command's help.
const commonFlags = " state-dir as credential-file json help "

func flagLabel(fs *flag.FlagSet, name string) string {
	switch name {
	case "help":
		return "-h, --help"
	case "resume":
		return "-r, --resume"
	case "version":
		return "--version VERSION"
	}
	label := "--" + name
	if b, ok := fs.Lookup(name).Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return label
	}
	if value, ok := flagValues[name]; ok {
		return label + " " + value
	}
	return label + " VALUE"
}

// flagValues names the value of each flag that takes one. A test checks that
// no help shows the fallback VALUE.
var flagValues = map[string]string{
	"state-dir": "DIR", "credential-file": "FILE", "as": "KIND", "listen": "ADDR", "destination": "FILE",
	"herdr-executable": "FILE", "workspace": "ID", "session": "ID", "operation-id": "ID", "path": "DIR",
	"name": "NAME", "agent": "ID", "worktree": "DIR", "new-credential-file": "FILE", "runtime-id": "ID",
	"runtime-file": "FILE", "runtime-state": "STATE", "expected-revision": "N", "limit": "N", "after": "CURSOR",
	"task": "ID", "title": "TEXT", "description": "TEXT", "ticket": "REF", "report": "TEXT", "progress": "STATE",
	"reason": "TEXT", "summary": "TEXT", "question": "TEXT", "note": "TEXT", "location": "PATH",
	"reference-revision": "REV", "reference": "ID", "from": "ID", "to": "ID", "body-file": "FILE",
	"page-kind": "KIND", "read-revision": "N", "known-revision": "N", "body": "TEXT", "mention": "NAME",
	"cursor": "CURSOR", "cursor-file": "FILE", "after-task": "TOKEN", "task-limit": "N",
}

func flagText(fs *flag.FlagSet, command, name string) string {
	text := fs.Lookup(name).Usage
	if name == "body" && command != "mail send" {
		text = "Same as --" + textFlag(command, "")
		if command == "task status" {
			text += ", or --summary with --progress finished"
		}
	}
	if flagRequired(command, name) {
		text += " (required)"
	}
	return text
}

// column prints an aligned two-column row in at most 80 columns, wrapping
// the text below itself when it is long.
func column(name, text string) string {
	const indent, width = 27, 80
	line := "  " + name
	if len(line) >= indent-1 {
		line += "\n" + strings.Repeat(" ", indent)
	} else {
		line += strings.Repeat(" ", indent-len(line))
	}
	used := indent
	for i, word := range strings.Fields(text) {
		if i > 0 && used+1+len([]rune(word)) > width {
			line += "\n" + strings.Repeat(" ", indent)
			used = indent
		} else if i > 0 {
			line += " "
			used++
		}
		line += word
		used += len([]rune(word))
	}
	return line + "\n"
}
