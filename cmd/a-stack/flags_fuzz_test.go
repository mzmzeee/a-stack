package main

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
)

func FuzzSplitFlags(f *testing.F) {
	for _, args := range [][]string{
		{},
		{"task", "show", "--task", "abc"},
		{"--json", "version"},
		{"--body=--json", "mail", "send"},
		{"--body", "--json"},
		{"--json=false", "--json"},
		{"--after=x", "--after=y"},
		{"--mention", "a", "--mention=b"},
		{"--limit=9223372036854775808"},
		{"update", "--version", "v0.1.7"},
		{"--version"},
		{"--"},
		{"-"},
		{"--body", "-x"},
		{"--unknown=value"},
		{"--limit"},
	} {
		f.Add(strings.Join(args, "\x00"))
	}

	f.Fuzz(func(t *testing.T, input string) {
		args := strings.Split(input, "\x00")
		parse := func(args []string) ([]string, bool, map[string]string, error) {
			fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			fs.Bool("json", false, "")
			fs.Int("limit", 0, "")

			for _, name := range []string{"body", "task", "after", "mention", "assign"} {
				fs.String(name, "", "")
			}

			fs.Var(new(versionFlag), "version", "")
			command, repeated, err := splitFlags(fs, args)
			values := map[string]string{}
			fs.Visit(func(f *flag.Flag) { values[f.Name] = f.Value.String() })

			return command, repeated, values, err
		}

		command, repeated, values, err := parse(args)
		if err != nil {
			return
		}

		// A positional prefix must not change flag parsing or consume arguments.
		prefixed, again, next, err := parse(append([]string{"fuzz-command"}, args...))
		if err != nil || again != repeated || !slices.Equal(prefixed, append([]string{"fuzz-command"}, command...)) {
			t.Fatalf("positional prefix changed parsing: %q, %v", prefixed, err)
		}

		if len(values) != len(next) {
			t.Fatal("positional prefix changed flags")
		}

		for name, value := range values {
			if next[name] != value {
				t.Fatalf("flag %s changed", name)
			}
		}
	})
}
