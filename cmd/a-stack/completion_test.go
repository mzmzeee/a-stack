package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func completionOutput(t *testing.T, words ...string) string {
	t.Helper()
	return helpOutput(t, append([]string{"__complete"}, words...)...)
}

func completionValues(t *testing.T, words ...string) []string {
	t.Helper()

	var values []string
	for _, line := range strings.Split(strings.TrimSpace(completionOutput(t, words...)), "\n") {
		if line != "" {
			value, _, _ := strings.Cut(line, "\t")
			values = append(values, value)
		}
	}

	return values
}

// Use the public machine help, not a second command list. Every documented
// command must be reachable and every flag shown in its help must complete.
func TestCompletionTracksHelp(t *testing.T) {
	var help struct {
		Data struct{ Commands []string } `json:"data"`
	}
	if json.Unmarshal([]byte(helpOutput(t, "--help", "--json")), &help) != nil {
		t.Fatal("help JSON")
	}

	for _, command := range help.Data.Commands {
		words := strings.Fields(command)
		for i, word := range words {
			if !slices.Contains(completionValues(t, append(slices.Clone(words[:i]), "")...), word) {
				t.Errorf("cannot complete %s at %s", command, word)
			}
		}

		flags := completionValues(t, append(slices.Clone(words), "--")...)
		for _, line := range strings.Split(helpOutput(t, append([]string{"help"}, words...)...), "\n") {
			if strings.HasPrefix(line, "  --") {
				name := strings.Fields(line)[0]
				if !slices.Contains(flags, name) {
					t.Errorf("%s help has %s, completion does not", command, name)
				}
			}
		}
	}
}

func TestCompletionWordsValuesAndPaths(t *testing.T) {
	for _, test := range []struct {
		words []string
		want  []string
	}{
		{[]string{"upd"}, []string{"update"}},
		{[]string{"task", "attention", ""}, []string{"set", "resolve"}},
		{[]string{"workspace", "orders", "s"}, []string{"set", "show"}},
		{[]string{"task", "status", "--progress", "w"}, []string{"waiting", "working"}},
		{[]string{"task", "status", "--progress=wor"}, []string{"--progress=working"}},
		{[]string{"--as", ""}, []string{"operator", "agent", "browser"}},
		{[]string{"--credential-file", "a file.json", "task", "cr"}, []string{"create"}},
		{[]string{"hook", "install", "--"}, []string{"--claude", "--codex", "--pi", "--opencode"}},
		{[]string{"help", "task", "att"}, []string{"attention"}},
	} {
		got := completionValues(t, test.words...)
		for _, value := range test.want {
			if !slices.Contains(got, value) {
				t.Errorf("%q lacks %q: %q", test.words, value, got)
			}
		}
	}

	if got := completionValues(t, "update", "--"); slices.Contains(got, "--task") || slices.Contains(got, "--credential-file") {
		t.Fatalf("unaccepted flags: %q", got)
	}

	dir := t.TempDir()
	if os.Mkdir(filepath.Join(dir, "some dir"), 0o700) != nil || os.WriteFile(filepath.Join(dir, "some file.json"), nil, 0o600) != nil {
		t.Fatal("path fixture")
	}

	for _, name := range []string{"credential-file", "body-file", "state-dir"} {
		got := completionValues(t, "--"+name, filepath.Join(dir, "some"))
		if !slices.Contains(got, filepath.Join(dir, "some dir")+string(os.PathSeparator)) || slices.Contains(got, filepath.Join(dir, "some file.json")) != (name != "state-dir") {
			t.Fatalf("--%s paths: %q", name, got)
		}
	}

	if got := completionOutput(t, "--nonexistent", ""); got != "" {
		t.Fatalf("unknown flag: %q", got)
	}
}

func completionHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		value := home
		if strings.HasPrefix(key, "XDG_") {
			value = filepath.Join(home, key)
		}

		t.Setenv(key, value)
	}

	t.Setenv("FPATH", "")

	return home
}

func TestCompletionInstallUninstall(t *testing.T) {
	home := completionHome(t)
	for shell, relative := range map[string]string{
		"bash": "XDG_DATA_HOME/bash-completion/completions/a-stack",
		"zsh":  "XDG_DATA_HOME/zsh/site-functions/_a-stack",
		"fish": "XDG_CONFIG_HOME/fish/completions/a-stack.fish",
	} {
		t.Run(shell, func(t *testing.T) {
			t.Setenv("SHELL", "/bin/"+shell)
			out := helpOutput(t, "completion", "install")

			path := filepath.Join(home, filepath.FromSlash(relative))
			data, err := os.ReadFile(path)
			if err != nil || string(data) != completionScripts[shell] || !strings.Contains(out, path) || !strings.Contains(out, "new terminal") {
				t.Fatalf("install: %s %v", out, err)
			}

			before, _ := os.Stat(path)

			out = helpOutput(t, "completion", "install", "--shell", shell)
			after, _ := os.Stat(path)
			if !strings.Contains(out, "Already installed") || before.ModTime() != after.ModTime() {
				t.Fatalf("repeat install changed file: %s", out)
			}

			if shell == "zsh" && !strings.Contains(out, "fpath=(") {
				t.Fatalf("missing fpath instruction: %s", out)
			}

			helpOutput(t, "completion", "uninstall", "--shell", shell)

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("not removed: %v", err)
			}

			if out := helpOutput(t, "completion", "uninstall", "--shell", shell); !strings.Contains(out, "Already absent") {
				t.Fatal(out)
			}
		})
	}

	for _, action := range []string{"install", "uninstall"} {
		out := helpOutput(t, "completion", action, "--shell", "powershell")
		if !strings.Contains(out, "$PROFILE") || !strings.Contains(out, "No profile was edited") || !strings.Contains(out, "Invoke-Expression") {
			t.Fatal(out)
		}
	}

	fpath := filepath.Join(home, "my-functions")
	t.Setenv("FPATH", fpath)

	out := helpOutput(t, "completion", "install", "--shell", "zsh")
	if strings.Contains(out, "fpath=(") || !strings.Contains(out, filepath.Join(fpath, "_a-stack")) {
		t.Fatal(out)
	}

	t.Setenv("SHELL", "/bin/unknown")

	var stdout, stderr bytes.Buffer
	if run([]string{"completion", "install"}, &stdout, &stderr) != 2 || stdout.Len() != 0 {
		t.Fatalf("unknown shell: %s %s", stdout.String(), stderr.String())
	}
}

// Shells invoke the real CLI entry point through a test-only executable shim.
func completionShim(t *testing.T) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "a-stack")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nASTACK_TEST_CHILD=1 exec "+shellQuote(exe)+" -test.run=^TestCLIProcess$ -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestCompletionShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(shell + " not installed")
			}

			path := filepath.Join(t.TempDir(), "completion")
			if err := os.WriteFile(path, []byte(helpOutput(t, "completion", shell)), 0o600); err != nil {
				t.Fatal(err)
			}

			if out, err := exec.Command(bin, "-n", path).CombinedOutput(); err != nil {
				t.Fatalf("syntax: %v %s", err, out)
			}

			if runtime.GOOS == "windows" {
				t.Skip("POSIX executable shim")
			}

			cli := completionShim(t)
			if shell == "bash" {
				script := `source "$1"
COMP_WORDS=("$2" upd); COMP_CWORD=1; _a_stack
[[ ${COMPREPLY[*]} == update ]] || exit 11
compgen -A function _a_stack | grep -qx _a_stack || exit 12
COMP_WORDS=("$2" task attention ''); COMP_CWORD=3; _a_stack
[[ " ${COMPREPLY[*]} " == *' set '* && " ${COMPREPLY[*]} " == *' resolve '* ]] || exit 13
COMP_WORDS=("$2" task status --progress = wor); COMP_CWORD=5; _a_stack
[[ ${COMPREPLY[*]} == working ]] || exit 14
`
				if out, err := exec.Command(bin, "--norc", "-c", script, "test", path, cli).CombinedOutput(); err != nil {
					t.Fatalf("bash completion: %v %s", err, out)
				}
			} else if shell == "zsh" {
				dir := filepath.Join(t.TempDir(), "some dir")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}

				script := `compdef() { :; }
compadd() {
    local nospace=false
    if [[ $1 == -S && $2 == '' ]]; then nospace=true; shift 2; fi
    shift 3
    [[ $1 == */ && $nospace == false ]] && exit 18
    print -rl -- "$@"
}
source "$1"
words=("$2" upd); CURRENT=2; _a_stack
words=("$2" --state-dir "$3"); CURRENT=3; _a_stack
`
				if out, err := exec.Command(bin, "-f", "-c", script, "test", path, cli, dir).CombinedOutput(); err != nil || string(out) != "update\n"+dir+"/\n" {
					t.Fatalf("zsh completion: %v %s", err, out)
				}
			} else if shell == "fish" {
				t.Setenv("PATH", filepath.Dir(cli)+string(os.PathListSeparator)+os.Getenv("PATH"))

				for _, line := range []string{"a-stack upd", "a-stack task status --progress wor"} {
					want := "update\t"
					if strings.Contains(line, "progress") {
						want = "working"
					}

					if out, err := exec.Command(bin, "--no-config", "-c", "source "+shellQuote(path)+"; complete -C "+shellQuote(line)).CombinedOutput(); err != nil || !strings.Contains(string(out), want) {
						t.Fatalf("fish completion: %v %s", err, out)
					}
				}

				root := t.TempDir()
				dir := filepath.Join(root, "path fixture")

				nested := filepath.Join(dir, "nested")
				if err := os.MkdirAll(nested, 0o700); err != nil {
					t.Fatal(err)
				}

				for _, flag := range []string{"--body-file", "--credential-file", "--state-dir"} {
					for _, token := range []string{
						filepath.Join(root, "path"),
						strings.ReplaceAll(dir, " ", `\ `) + "/",
						`"` + dir + `/"`, `"` + dir + `/`,
						"'" + dir + "/'", "'" + dir + "/",
					} {
						line := "a-stack " + flag + " " + token
						want := nested + "/"
						if token == filepath.Join(root, "path") {
							want = dir + "/"
						}

						if out, err := exec.Command(bin, "--no-config", "-c", "source "+shellQuote(path)+"; complete -C \"$argv[1]\"", "--", line).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != want {
							t.Errorf("fish path %q: %v %q", line, err, out)
						}
					}
				}

				for _, token := range []string{"", `"`, "'"} {
					line := "a-stack --body-file " + token

					command := exec.Command(bin, "--no-config", "-c", "source "+shellQuote(path)+"; complete -C \"$argv[1]\"", "--", line)
					command.Dir = root
					if out, err := command.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "path fixture/" {
						t.Errorf("fish empty token %q: %v %q", line, err, out)
					}
				}
			}
		})
	}
}
