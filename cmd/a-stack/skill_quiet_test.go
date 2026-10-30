package main

import (
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/skills"
)

func TestSkillKeepsHumanPanesQuiet(t *testing.T) {
	for _, name := range []string{"a-stack/SKILL.md", "a-stack/references/orchestrator.md"} {
		data, err := skills.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		text := string(data)
		for _, want := range []string{"--notices hook", "Progress mail carries no mention", "until it answered or 10 minutes passed"} {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
				t.Errorf("%s missing %q", name, want)
			}
		}

		if strings.Contains(text, "bind yourself too") {
			t.Errorf("%s restores unsafe Orchestrator binding advice", name)
		}
	}

	data, _ := skills.FS.ReadFile("a-stack/references/orchestrator.md")
	for _, want := range []string{"me herdr bind --notices hook --agent", "after each worker handoff and before reporting to the Operator", "report file and Task mail"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("Orchestrator missing %q", want)
		}
	}

	// v0.1.8 installed copies must remain refreshable after these edits.
	for name, hash := range map[string]string{
		"a-stack/SKILL.md":                   "53b9655db7cdd9936c14f94e246e00d6f3c1ddeda09e00a531b09638e67afb6b",
		"a-stack/references/orchestrator.md": "1028c12ff992947f722189640fe4f8454a307e41b12d3c20f125d7bc1253b007",
	} {
		found := false
		for _, known := range installedHashes[name] {
			found = found || known == hash
		}

		if !found {
			t.Errorf("released %s no longer refreshable", name)
		}
	}
}
