package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var mailCommands = []string{"mail ack", "mail send", "mail reply", "mail recipients", "mail read", "mail check"}

func isMailCommand(command string) bool {
	for _, c := range mailCommands {
		if c == command {
			return true
		}
	}
	return false
}

type mailFlags struct {
	cursor, cursorFile, afterTask, message string
	urgent                                 bool
	ackRequired                            bool
	ackFrom                                []string
	ackDue                                 string
	noMentionSender                        bool
	mentions                               []string
	taskLimit                              int
}

func (f *mailFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.urgent, "urgent", false, "Nudge enrolled hooks at the next eligible tool boundary")
	fs.BoolVar(&f.ackRequired, "ack-required", false, "Ask selected assignees for an explicit acknowledgement")
	fs.Func("ack-from", "Assignee to acknowledge; repeat for more", func(name string) error { f.ackFrom = append(f.ackFrom, name); return nil })
	fs.StringVar(&f.ackDue, "ack-due", "", "Acknowledgement deadline duration, such as 30m")
	fs.StringVar(&f.message, "message", "", "Original message ID in your Session")
	fs.BoolVar(&f.noMentionSender, "no-mention-sender", false, "Do not automatically mention the original sender")
	fs.Func("mention", "Assignee name to mention; repeat for more", func(name string) error { f.mentions = append(f.mentions, name); return nil })
	fs.StringVar(&f.cursor, "cursor", "", "Read after this mail cursor")
	fs.StringVar(&f.cursorFile, "cursor-file", "", "Your saved mail position; each check updates it")
	fs.StringVar(&f.afterTask, "after-task", "", "Continue the Task scan after this token")
	fs.IntVar(&f.taskLimit, "task-limit", 0, "Tasks per page: 1 to 100")
}
func mailAllowedFlags(command string) string {
	switch command {
	case "mail send":
		return "task operation-id body body-file mention ack-required ack-from ack-due urgent"
	case "mail reply":
		return "message task operation-id body body-file mention no-mention-sender ack-required ack-from ack-due urgent"
	case "mail ack":
		return "message operation-id"
	case "mail recipients":
		return "task"
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
	case "mail ack":
		if f.message == "" {
			return nil, invalid("explicit --message required")
		}
		body = stack.MailAckRequest{Context: ref, OperationID: l.operationID, MessageID: f.message}
	case "mail send", "mail reply":
		if command == "mail reply" && f.message == "" {
			return nil, invalid("explicit --message required")
		}
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
		send := stack.MailSendRequest{Context: ref, OperationID: l.operationID, TaskID: t.task, Body: t.body, Mentions: f.mentions, AckRequired: f.ackRequired, AckFrom: f.ackFrom, AckDue: f.ackDue}
		if f.urgent {
			send.Priority = "urgent"
		}
		body = send
		if command == "mail reply" {
			body = stack.MailReplyRequest{MailSendRequest: send, MessageID: f.message, NoMentionSender: f.noMentionSender}
		}
	case "mail recipients":
		body = stack.MailRecipientsRequest{Context: ref, TaskID: t.task}
	case "mail read":
		body = stack.MailReadRequest{Context: ref, TaskID: t.task, Cursor: f.cursor, Limit: l.limit}
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

// mailCheck calls deliver before saving a cursor file, and returns nil data
// after delivery. Without a writable file it returns the response to the caller.
// Concurrent saves are atomic and last-writer-wins; a lost save replays mail.
func mailCheck(ctx context.Context, client *stack.Client, r stack.MailCheckRequest, path string, given bool, deliver func([]byte) error) ([]byte, error) {
	save := given && path != "-"
	if given {
		var err error
		if r.Cursors, err = readMailCursors(path); err != nil {
			return nil, err
		}
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(client.Discovery.DatabaseID+"\n"+r.WorkspaceID+"\n"+r.SessionID+"\n"+client.Credential.Token)))
	partial := false
	if save {
		if len(r.Cursors) > 0 && r.Cursors["identity"] != identity {
			return nil, invalid("cursor file belongs to another database, Workspace, Session or Agent credential, or is unbound; use a new cursor file to replay retained mail")
		}
		delete(r.Cursors, "identity")
		partial = r.Cursors["partial"] == "true"
		delete(r.Cursors, "partial")
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
	cursors := r.Cursors
	out.Tasks = []stack.MailPage{}
	for _, page := range envelope.Data.Tasks {
		cursors[page.TaskID] = page.Cursor
		partial = partial || page.More
		if len(page.Messages) > 0 || page.More {
			out.Tasks = append(out.Tasks, page)
		}
	}
	// Finish the scan before revisiting partial Tasks, even under continuous mail.
	if out.NextTask != "" {
		cursors[scanKey] = out.NextTask
		if partial {
			cursors["partial"] = "true"
		}
	} else {
		out.More = out.More || partial
	}
	cursors["identity"] = identity
	out.NextTask = ""
	data, err = json.Marshal(map[string]any{"data": out})
	if err != nil {
		return nil, err
	}
	if err = deliver(data); err != nil {
		return nil, err
	}
	if stack.ReplacePrivate(path, cursors) != nil {
		return nil, invalid("cannot write the cursor file; its folder must exist and be yours")
	}
	return nil, nil
}
