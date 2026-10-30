package stack

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func FuzzMentionNames(f *testing.F) {
	for _, seed := range [][2]string{
		{"", ""},
		{"Ask @02-worker and @02-worker", "01-orchestrator"},
		{`Ask "@worker" ('@reviewer') <@agent>`, ""},
		{`Mail person@example.com or "person"@example.com`, ""},
		{"It’s @worker's turn. café@host \"escaped\\\"@name", ""},
		{"\xff\x00@bad", ""},
		{"@" + strings.Repeat("a", 65), ""},
		{strings.Repeat("a", MaxMailBodyBytes+1), ""},
		{"hi", "a\x00a\x00b"},
		{"@worker", "invalid.name"},
	} {
		f.Add(seed[0], seed[1])
	}

	var many []string
	for i := 0; i <= MaxMailMentions; i++ {
		many = append(many, "a"+base64.RawURLEncoding.EncodeToString([]byte{byte(i >> 8), byte(i)}))
	}

	f.Add("many", strings.Join(many, "\x00"))
	f.Fuzz(func(t *testing.T, body, mentions string) {
		var explicit []string
		if mentions != "" {
			explicit = strings.Split(mentions, "\x00")
		}

		names, err := mentionNames(body, explicit)
		if err != nil {
			return
		}

		if len(names) > MaxMailMentions || !slices.IsSorted(names) || len(slices.Compact(slices.Clone(names))) != len(names) {
			t.Fatalf("invalid mention set: %q", names)
		}

		var canonical []string
		for _, name := range names {
			if !agentName.MatchString(name) {
				t.Fatalf("invalid name %q", name)
			}

			canonical = append(canonical, "@"+name)
		}

		for _, name := range explicit {
			if !slices.Contains(names, name) {
				t.Fatalf("lost explicit mention %q", name)
			}
		}

		if len(names) != 0 {
			again, err := mentionNames(strings.Join(canonical, " "), nil)
			if err != nil || !slices.Equal(names, again) {
				t.Fatalf("mention round trip: %q, %v", again, err)
			}
		}
	})
}

func FuzzMailCursor(f *testing.F) {
	const task = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, cursor := range []string{"", "!", strings.Repeat("a", 513), encodeMailCursor(task, 0), encodeMailCursor(task, 9223372036854775807), encodeMailCursor(task, -1), encodeMailCursor("foreign", 1)} {
		f.Add(cursor)
	}

	for _, data := range []string{`null`, `{}`, `{"task_id":"` + task + `","sequence":1,"unknown":0}`, `{"task_id":"` + task + `","sequence":9223372036854775808}`, `{} {}`, "\xff"} {
		f.Add(base64.RawURLEncoding.EncodeToString([]byte(data)))
	}

	f.Fuzz(func(t *testing.T, cursor string) {
		sequence, err := decodeMailCursor(task, cursor)
		if err != nil {
			return
		}

		if sequence < 0 {
			t.Fatal("negative position accepted")
		}

		if got, err := decodeMailCursor(task, encodeMailCursor(task, sequence)); err != nil || got != sequence {
			t.Fatalf("round trip: %d, %v", got, err)
		}

		if cursor != "" {
			if _, err := decodeMailCursor("foreign", cursor); err == nil {
				t.Fatal("foreign Task accepted")
			}
		}
	})
}

func FuzzMailCheckCursor(f *testing.F) {
	const task = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, cursor := range []string{"", "!", strings.Repeat("a", 513), encodeMailCheckCursor("session", "agent", task), encodeMailCheckCursor("other", "agent", task), encodeMailCheckCursor("session", "other", task), encodeMailCheckCursor("session", "agent", "../bad")} {
		f.Add(cursor)
	}

	for _, data := range []string{`null`, `{}`, `{"session_id":"session","agent_id":"agent","task_id":"` + task + `","unknown":0}`, `{} {}`, "\xff"} {
		f.Add(base64.RawURLEncoding.EncodeToString([]byte(data)))
	}

	f.Fuzz(func(t *testing.T, cursor string) {
		position, err := decodeMailCheckCursor("session", "agent", cursor)
		if err != nil {
			return
		}

		if cursor == "" {
			if position != "" {
				t.Fatal("empty cursor advanced")
			}

			return
		}

		if !validToken(position) {
			t.Fatal("invalid Task accepted")
		}

		if got, err := decodeMailCheckCursor("session", "agent", encodeMailCheckCursor("session", "agent", position)); err != nil || got != position {
			t.Fatalf("round trip: %q, %v", got, err)
		}

		for _, context := range [][2]string{{"other", "agent"}, {"session", "other"}} {
			if _, err := decodeMailCheckCursor(context[0], context[1], cursor); err == nil {
				t.Fatal("foreign Session or caller accepted")
			}
		}
	})
}
