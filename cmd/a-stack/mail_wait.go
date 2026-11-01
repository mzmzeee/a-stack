package main

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var errMailWaitTimeout = &stack.Error{Code: "mail_wait_timeout", Message: "no matching mail before the timeout"}

func mailWait(ctx context.Context, client *stack.Client, ref stack.Context, path string, timeout time.Duration, all bool, pause func(context.Context, time.Duration) error) ([]byte, error) {
	if path == "" || path == "-" {
		return nil, invalid("mail wait requires --cursor-file FILE (not stdin)")
	}

	if timeout <= 0 {
		return nil, invalid("--timeout must be a positive duration")
	}

	if client.Credential.Kind != stack.AgentCapability {
		return nil, &stack.Error{Code: "forbidden", Message: "mail wait requires an Agent credential"}
	}

	cursors, err := readMailCursors(path)
	if err != nil {
		return nil, err
	}

	if len(cursors) > 0 && cursors["identity"] != mailCursorIdentity(client, ref) {
		return nil, invalid("cursor file belongs to another database, Workspace, Session or Agent credential, or is unbound; use a new cursor file to replay retained mail")
	}

	delete(cursors, "identity")
	delete(cursors, scanKey)
	delete(cursors, "partial")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request := stack.MailCheckRequest{Context: ref, Cursors: cursors, Peek: true}

	delay := 5 * time.Second
	partial := false
	for {
		data, more, err := mailWaitPage(ctx, client, &request, all)
		if ctx.Err() != nil {
			return nil, errMailWaitTimeout
		}

		if err == nil && data != nil {
			return data, nil
		}

		if err == nil {
			partial = partial || more
			// Finish a retained-mail sweep before sleeping, visiting other Tasks
			// before returning to a busy Task's next page.
			if request.AfterTask != "" {
				continue
			}

			if partial {
				partial = false
				continue
			}
		}

		wait := delay
		if err != nil {
			e, ok := err.(*stack.Error)
			if !ok || e.Code != "rate_limited" {
				return nil, err
			}

			// Clamp before multiplying an untrusted retry duration to avoid overflow.
			if remaining, ok := ctx.Deadline(); ok && e.RetryAfter > int64(time.Until(remaining)/time.Second) {
				wait = time.Until(remaining)
			} else if e.RetryAfter > 0 {
				wait = max(wait, time.Duration(e.RetryAfter)*time.Second)
			}
		}

		if err := pause(ctx, wait); err != nil {
			return nil, errMailWaitTimeout
		}

		delay = min(delay*2, 30*time.Second)
	}
}

func mailWaitPage(ctx context.Context, client *stack.Client, request *stack.MailCheckRequest, all bool) ([]byte, bool, error) {
	data, err := client.Do(ctx, "POST", "/v1/session/inspect", request.Context)
	if err != nil {
		return nil, false, err
	}

	var session struct {
		Data stack.Session `json:"data"`
	}
	if json.Unmarshal(data, &session) != nil || session.Data.YourAgentID == "" {
		return nil, false, &stack.Error{Code: "storage_unavailable", Message: "unexpected Session response"}
	}

	if session.Data.Closed {
		return nil, false, &stack.Error{Code: "session_closed", Message: "Session is closed"}
	}

	data, err = client.Do(ctx, "POST", "/v1/mail/check", request)
	if err != nil {
		return nil, false, err
	}

	var envelope struct {
		Data stack.MailCheckPage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil, false, &stack.Error{Code: "storage_unavailable", Message: "unexpected mail check response"}
	}

	type taskCount struct {
		TaskID string `json:"task_id"`
		Count  int    `json:"count"`
	}

	tasks := []taskCount{}

	count := 0
	partial := false
	for _, page := range envelope.Data.Tasks {
		partial = partial || page.More
		n := 0
		for _, message := range page.Messages {
			if mailWaitMatches(message, session.Data.YourAgentID, all) {
				n++
			}
		}

		if n > 0 {
			tasks = append(tasks, taskCount{page.TaskID, n})
			count += n
		}

		request.Cursors[page.TaskID] = page.Cursor
	}

	request.AfterTask = envelope.Data.NextTask

	if count == 0 {
		return nil, partial, nil
	}

	data, err = json.Marshal(map[string]any{"data": map[string]any{"count": count, "tasks": tasks}})

	return data, partial, err
}

func mailWaitMatches(message stack.MailMessage, agent string, all bool) bool {
	if message.SenderID == agent {
		return false
	}

	if all || message.Priority == "urgent" || message.SenderKind == "operator" || slices.Contains(message.MentionIDs, agent) {
		return true
	}

	for _, ack := range message.Acks {
		if ack.RecipientID == agent && ack.AckedAt == "" && ack.WithdrawnAt == "" {
			return true
		}
	}

	return false
}

func pauseMailWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
