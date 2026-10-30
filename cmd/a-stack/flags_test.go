package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestSplitFlagsOpaquePositionals(t *testing.T) {
	for _, prefix := range []string{"-", "--"} {
		id := prefix + strings.Repeat("A", 43-len(prefix))
		t.Run(prefix, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)

			code := fs.String("code", "", "")
			got, _, err := splitFlags(fs, []string{"session", "link", "team", id, "--code", "--short-code"})
			if err != nil || !reflect.DeepEqual(got, []string{"session", "link", "team", id}) || *code != "--short-code" {
				t.Fatalf("positional ID: %v %v", got, err)
			}

			for _, name := range []string{"task", "agent", "code"} {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				value := fs.String(name, "", "")
				if _, _, err := splitFlags(fs, []string{"--" + name, id}); err != nil || *value != id {
					t.Fatalf("--%s rejected dash value: %v", name, err)
				}
			}
		})
	}
}

func TestSplitFlagsEndOfFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	help := fs.Bool("help", false, "")

	want := []string{"session", "link", "--help", "--unknown", "-"}
	got, _, err := splitFlags(fs, []string{"session", "link", "--", "--help", "--unknown", "-"})
	if err != nil || !reflect.DeepEqual(got, want) || *help {
		t.Fatalf("end of flags: %v %v", got, err)
	}

	if _, _, err := splitFlags(fs, []string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}
