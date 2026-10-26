package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

type completionItem struct{ value, description string }

func isCompletionCommand(command string) bool { return strings.HasPrefix(command, "completion ") }

func boolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// The shells pass the words before the cursor, including an empty last word
// after a space. The wire format is value<TAB>description, never shell code.
func completeWords(fs *flag.FlagSet, words []string) []completionItem {
	if len(words) == 0 {
		words = []string{""}
	}
	// PowerShell 5.1 drops empty native arguments; its adapter encodes only
	// the current word so a trailing space survives the subprocess boundary.
	current := strings.TrimPrefix(words[len(words)-1], "--current=")
	var command []string
	values := map[string]string{}
	pending := ""
	for _, word := range words[:len(words)-1] {
		if pending != "" {
			values[pending], pending = word, ""
			continue
		}
		if strings.HasPrefix(word, "-") {
			name, value, equal := strings.Cut(strings.TrimLeft(word, "-"), "=")
			f := fs.Lookup(name)
			if f == nil {
				return nil
			}
			if equal {
				values[name] = value
			} else if !boolFlag(f) {
				pending = name
			}
			continue
		}
		command = append(command, word)
	}
	prefix := ""
	if pending == "" && strings.HasPrefix(current, "-") {
		name, value, equal := strings.Cut(strings.TrimLeft(current, "-"), "=")
		if equal {
			pending, prefix, current = name, strings.SplitN(current, "=", 2)[0]+"=", value
		}
	}
	var items []completionItem
	cmd := strings.Join(command, " ")
	if pending != "" {
		items = completeValue(pending, current, values)
	} else {
		// `help` takes the same command tree, without command flags.
		help := len(command) > 0 && command[0] == "help"
		if help {
			cmd = strings.Join(command[1:], " ")
		}
		for _, group := range helpGroups {
			for _, row := range group.rows {
				rest := row.command
				if cmd != "" {
					if !strings.HasPrefix(rest, cmd+" ") {
						continue
					}
					rest = strings.TrimPrefix(rest, cmd+" ")
				}
				if rest != "" {
					items = append(items, completionItem{strings.Fields(rest)[0], row.text})
				}
			}
		}
		if !help {
			if cmd == "" {
				items = append(items, completionItem{"help", "Show command help"})
			}
			allowed := acceptedFlags(cmd)
			if cmd == "" || !knownCommand(cmd) {
				allowed += commonFlags // context flags can precede the command
			}
			for _, name := range strings.Fields(allowed) {
				f := fs.Lookup(name)
				if f != nil {
					label := "--" + name
					if len(name) == 1 {
						label = "-" + name
					}
					items = append(items, completionItem{label, f.Usage})
					if name == "help" {
						items = append(items, completionItem{"-h", f.Usage})
					}
				}
			}
		}
	}
	slices.SortFunc(items, func(a, b completionItem) int { return strings.Compare(a.value, b.value) })
	out := []completionItem{}
	for _, item := range items {
		if strings.HasPrefix(item.value, current) && !strings.ContainsFunc(item.value, unicode.IsControl) && (len(out) == 0 || out[len(out)-1].value != prefix+item.value) {
			item.value = prefix + item.value
			item.description = strings.Join(strings.Fields(item.description), " ")
			out = append(out, item)
		}
	}
	return out
}

func completeValue(name, current string, _ map[string]string) []completionItem {
	var items []completionItem
	for _, value := range strings.Fields(map[string]string{
		"as": "operator agent browser", "progress": "waiting working finished cancelled",
		"runtime-state": "unknown working idle done stopped blocked",
		"page-kind":     "tasks agents dependencies assignments references history graph_history",
		"shell":         "bash zsh fish powershell",
	}[name]) {
		items = append(items, completionItem{value, ""})
	}
	switch flagValues[name] {
	case "FILE", "DIR":
		items = completePaths(current, flagValues[name] == "DIR")
	}
	return items
}

func completePaths(current string, dirsOnly bool) []completionItem {
	dir, base := filepath.Split(current)
	search := dir
	if strings.HasPrefix(search, "~/") || strings.HasPrefix(search, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		search = filepath.Join(home, search[2:])
	}
	if search == "" {
		search = "."
	}
	entries, err := os.ReadDir(search)
	if err != nil {
		return nil
	}
	items := []completionItem{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) || strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(search, entry.Name()))
			isDir = err == nil && info.IsDir()
		}
		if dirsOnly && !isDir {
			continue
		}
		value := dir + entry.Name()
		if isDir {
			value += string(os.PathSeparator)
		}
		items = append(items, completionItem{value, ""})
	}
	return items
}

func writeCompletions(fs *flag.FlagSet, words []string, stdout io.Writer) {
	for _, item := range completeWords(fs, words) {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", item.value, item.description)
	}
}

func completionCommand(command, shell string, stdout io.Writer) error {
	action := strings.TrimPrefix(command, "completion ")
	if action != "install" && action != "uninstall" {
		_, err := io.WriteString(stdout, completionScripts[action])
		return err
	}
	if shell == "" {
		shell = strings.TrimSuffix(filepath.Base(os.Getenv("SHELL")), ".exe")
		if shell == "pwsh" {
			shell = "powershell"
		}
	}
	script, ok := completionScripts[shell]
	if !ok {
		return invalid("cannot detect a supported shell; use --shell bash, zsh, fish or powershell")
	}
	if shell == "powershell" {
		verb := "Add this line to $PROFILE (then reload the profile or open a new terminal):"
		if action == "uninstall" {
			verb = "Remove this line from $PROFILE (then open a new terminal):"
		}
		_, err := fmt.Fprintln(stdout, verb+"\na-stack completion powershell | Out-String | Invoke-Expression\nNo profile was edited.")
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	path := filepath.Join(data, "bash-completion", "completions", "a-stack")
	if shell == "fish" {
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		path = filepath.Join(config, "fish", "completions", "a-stack.fish")
	}
	if shell == "zsh" {
		dir := filepath.Join(data, "zsh", "site-functions")
		for _, entry := range filepath.SplitList(os.Getenv("FPATH")) {
			if filepath.IsAbs(entry) && within(entry, home) {
				dir = entry
				break
			}
		}
		path = filepath.Join(dir, "_a-stack")
		if action == "install" && !slices.Contains(filepath.SplitList(os.Getenv("FPATH")), dir) {
			_, _ = fmt.Fprintf(stdout, "Add this line before compinit in ~/.zshrc:\nfpath=(%s $fpath)\n", shellQuote(dir))
		}
	}
	if action == "uninstall" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			_, err = fmt.Fprintf(stdout, "Already absent: %s. No file changed.\n", path)
			return err
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "Removed %s. Open a new terminal to stop using it.\n", path)
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	verb := "Installed"
	if bytes.Equal(old, []byte(script)) {
		verb = "Already installed at"
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(script), 0644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(stdout, "%s %s. Open a new terminal to load it.\n", verb, path)
	if shell == "bash" {
		_, _ = fmt.Fprintf(stdout, "Requires bash-completion; load it now without a new terminal: source %s\n", shellQuote(path))
	} else if shell == "zsh" {
		_, _ = fmt.Fprintln(stdout, "Requires compinit in ~/.zshrc: autoload -Uz compinit; compinit")
	}
	return err
}

var completionScripts = map[string]string{
	"bash": `# a-stack completion: candidates come from the CLI's help and flag tables.
_a_stack() {
    local value description word previous='' i
    local -a words=()
    COMPREPLY=()
    # Readline treats '=' as a word break. Join it again for the CLI parser.
    for ((i=1; i<=COMP_CWORD; i++)); do
        word=${COMP_WORDS[i]}
        if [[ $word == = || $previous == = ]]; then
            words[${#words[@]}-1]+=$word
        else
            words+=("$word")
        fi
        previous=$word
    done
    while IFS=$'\t' read -r value description; do
        if [[ ${COMP_WORDS[COMP_CWORD]} != --*=* && $value == --*=* ]]; then
            value=${value#*=}
        fi
        COMPREPLY+=("$value")
    done < <(command "${COMP_WORDS[0]}" __complete "${words[@]}" 2>/dev/null)
    if [[ ${#COMPREPLY[@]} == 1 && ${COMPREPLY[0]} == */ ]]; then
        compopt -o nospace 2>/dev/null || :
    fi
    compopt -o filenames 2>/dev/null || :
}
complete -F _a_stack a-stack
`,
	"zsh": `#compdef a-stack
# Candidates come from the CLI's help and flag tables.
_a_stack() {
    local row
    local -a values descriptions
    for row in "${(@f)$(command "${words[1]}" __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}"; do
        values+=("${row%%$'\t'*}")
        descriptions+=("${row%%$'\t'*} -- ${row#*$'\t'}")
    done
    (( ${#values} )) && compadd -d descriptions -- "${values[@]}"
}
if [[ $funcstack[1] == _a-stack ]]; then
    _a_stack "$@"
else
    compdef _a_stack a-stack
fi
`,
	"fish": `# Candidates come from the CLI's help and flag tables.
function __a_stack_complete
    set -l words (commandline -opc)
    set -l current (commandline -ct)
    command $words[1] __complete $words[2..-1] "$current" 2>/dev/null
end
complete -c a-stack -f -a '(__a_stack_complete)'
`,
	"powershell": `# Candidates come from the CLI's help and flag tables.
Register-ArgumentCompleter -Native -CommandName a-stack,a-stack.exe -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $words = @()
    foreach ($element in $commandAst.CommandElements | Select-Object -Skip 1) {
        if ($element.Extent.StartOffset -ge $cursorPosition) { break }
        if ($element.Extent.EndOffset -ge $cursorPosition) { break }
        if ($element -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
            $words += $element.Value
        } else {
            $words += $element.Extent.Text
        }
    }
    $words += '--current=' + $wordToComplete.Trim("'", '"')
    $exe = $commandAst.CommandElements[0].Value
    & $exe __complete @words 2>$null | ForEach-Object {
        $parts = $_ -split "` + "`t" + `", 2
        $value = $parts[0]
        $description = $parts[1]
        if (-not $description) { $description = $value }
        $quoted = "'" + $value.Replace("'", "''") + "'"
        [System.Management.Automation.CompletionResult]::new($quoted, $value, 'ParameterValue', $description)
    }
}
`,
}
