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
	cursor, cursorFile, afterTask string
	mentions                      []string
	taskLimit                     int
}

func (f *mailFlags) register(fs *flag.FlagSet) {
	fs.Func("mention", "Assignee name to mention; repeat for more", func(name string) error { f.mentions = append(f.mentions, name); return nil })
	fs.StringVar(&f.cursor, "cursor", "", "Read after this mail cursor")
	fs.StringVar(&f.cursorFile, "cursor-file", "", "Your saved mail position; each check updates it")
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

// readMailCursors reads a cursor map. A missing file is an empty map: the
// check then starts at the first message and creates the file.
func readMailCursors(path string) (map[string]string, error) {
	var reader io.Reader = os.Stdin
	if path != "-" {
		file, err := stack.Open(path)
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
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
		return nil, missingContext
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
			t.body = text
		}
		if len(t.body) > stack.MaxMailBodyBytes || !utf8.ValidString(t.body) {
			return nil, invalid("mail body must be UTF-8 and at most 64 KiB")
		}
		body = stack.MailSendRequest{Context: ref, OperationID: l.operationID, TaskID: t.task, Body: t.body, Mentions: f.mentions}
	case "mail read":
		body = stack.MailReadRequest{Context: ref, TaskID: t.task, Cursor: f.cursor, Limit: l.limit}
	case "mail check":
		return mailCheck(ctx, client, stack.MailCheckRequest{Context: ref, AfterTask: f.afterTask, TaskLimit: f.taskLimit, Limit: l.limit}, f.cursorFile, seen["cursor-file"])
	default:
		return nil, invalid("unknown mail command")
	}
	return client.Do(ctx, "POST", "/v1/"+strings.ReplaceAll(command, " ", "/"), body)
}

// scanKey keeps the Task scan continuation in a cursor file. It is never a
// Task ID, so the check takes it out of the map before it sends the map.
const scanKey = "next_task"

type mailCheckOutput struct {
	stack.MailCheckPage
	Hint string `json:"hint,omitempty"`
}

// mailCheck sends one check. With a cursor file (not -), it saves the returned
// cursors and the scan continuation back to the file and prints only the Tasks
// with new mail, so the same command run again reads on from there while more
// is true. The file
// is replaced atomically; of two checks at once the last write wins, and the
// lost one only shows its mail again.
func mailCheck(ctx context.Context, client *stack.Client, r stack.MailCheckRequest, path string, given bool) ([]byte, error) {
	save := given && path != "-"
	if given {
		var err error
		if r.Cursors, err = readMailCursors(path); err != nil {
			return nil, err
		}
	}
	if save {
		if r.AfterTask == "" {
			r.AfterTask = r.Cursors[scanKey]
		}
		delete(r.Cursors, scanKey)
	}
	data, err := client.Do(ctx, "POST", "/v1/mail/check", r)
	if err != nil || given && !save {
		return data, err
	}
	var envelope struct {
		Data stack.MailCheckPage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil, &stack.Error{Code: "storage_unavailable", Message: "unexpected mail check response", Retryable: true}
	}
	out := mailCheckOutput{MailCheckPage: envelope.Data}
	if !save {
		if out.More {
			out.Hint = "more mail is waiting; without --cursor-file each check starts again at the first message"
		}
		return json.Marshal(map[string]any{"data": out})
	}
	cursors, partial := r.Cursors, false
	out.Tasks = []stack.MailPage{}
	for _, page := range envelope.Data.Tasks {
		cursors[page.TaskID] = page.Cursor
		partial = partial || page.More
		if len(page.Messages) > 0 || page.More {
			out.Tasks = append(out.Tasks, page)
		}
	}
	// The next check goes on with the next Tasks, unless a Task has mail
	// left: then it starts again at the first Task, so that none is skipped.
	if out.NextTask != "" && !partial {
		cursors[scanKey] = out.NextTask
	}
	out.NextTask = ""
	if stack.ReplacePrivate(path, cursors) != nil {
		return nil, invalid("cannot write the cursor file; its folder must exist and be yours")
	}
	return json.Marshal(map[string]any{"data": out})
}
