package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var mailCommands = []string{"mail send", "mail read", "mail check"}

func isMailCommand(command string) bool {
	for _, c := range mailCommands {
		if c == command {
			return true
		}
	}
	return false
}

type mailFlags struct {
	body, cursor, cursorFile, afterTask string
	mentions                            []string
	taskLimit                           int
}

func (f *mailFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.body, "body", "", "Mail text")
	fs.Func("mention", "Assignee name to mention; repeat for more", func(name string) error { f.mentions = append(f.mentions, name); return nil })
	fs.StringVar(&f.cursor, "cursor", "", "Read after this mail cursor")
	fs.StringVar(&f.cursorFile, "cursor-file", "", "JSON map of Task IDs to cursors, or - for stdin")
	fs.StringVar(&f.afterTask, "after-task", "", "Continue the Task scan after this token")
	fs.IntVar(&f.taskLimit, "task-limit", 0, "Tasks per page: 1 to 100")
}
func mailAllowedFlags(command string) string {
	switch command {
	case "mail send":
		return "task operation-id body body-file mention"
	case "mail read":
		return "task cursor limit"
	case "mail check":
		return "cursor-file after-task task-limit limit"
	}
	return ""
}
func readMailCursors(path string) (map[string]string, error) {
	var reader io.Reader = os.Stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, invalid("cursor file is unavailable")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, stack.MaxRequestBytes+1))
	if err != nil || len(data) > stack.MaxRequestBytes || !utf8.Valid(data) {
		return nil, invalid("cursor map exceeds the request limit or is not UTF-8")
	}
	var cursors map[string]string
	if json.Unmarshal(data, &cursors) != nil || cursors == nil {
		return nil, invalid("cursor file must contain one JSON object mapping Task IDs to cursor strings")
	}
	return cursors, nil
}
func mailRequest(ctx context.Context, client *stack.Client, command string, l lifecycleFlags, t taskFlags, f mailFlags, seen map[string]bool) ([]byte, error) {
	if l.workspace == "" || l.session == "" {
		return nil, invalid("explicit --workspace and --session required")
	}
	ref := stack.Context{WorkspaceID: l.workspace, SessionID: l.session}
	var body any
	switch command {
	case "mail send":
		if seen["body-file"] {
			if seen["body"] {
				return nil, invalid("choose --body or --body-file")
			}
			text, err := bodyText(t.bodyFile)
			if err != nil {
				return nil, err
			}
			f.body = text
		}
		if len(f.body) > stack.MaxMailBodyBytes || !utf8.ValidString(f.body) {
			return nil, invalid("mail body must be UTF-8 and at most 64 KiB")
		}
		body = stack.MailSendRequest{Context: ref, OperationID: l.operationID, TaskID: t.task, Body: f.body, Mentions: f.mentions}
	case "mail read":
		body = stack.MailReadRequest{Context: ref, TaskID: t.task, Cursor: f.cursor, Limit: l.limit}
	case "mail check":
		var cursors map[string]string
		if seen["cursor-file"] {
			var err error
			cursors, err = readMailCursors(f.cursorFile)
			if err != nil {
				return nil, err
			}
		}
		body = stack.MailCheckRequest{Context: ref, Cursors: cursors, AfterTask: f.afterTask, TaskLimit: f.taskLimit, Limit: l.limit}
	default:
		return nil, invalid("unknown mail command")
	}
	return client.Do(ctx, "POST", "/v1/"+strings.ReplaceAll(command, " ", "/"), body)
}
