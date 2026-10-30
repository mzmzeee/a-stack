package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// mailNudgeEvery is the least time between two mail checks of one harness
// session; hookFilesKept is how long an unused cursor file stays.
const mailNudgeEvery, hookFilesKept = 30 * time.Second, 7 * 24 * time.Hour

// catchUpPages bounds the mail check pages of one first check; a test
// lowers it.
var catchUpPages = 20

// hookCacheDir is the folder of one Agent's hook cursor files in the user
// cache folder. On Windows that is %LOCALAPPDATA%, where a-stack is the
// default state directory, so the hook files get a folder of their own.
func hookCacheDir(cache, agent string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(cache, "a-stack-hooks", agent)
	}
	return filepath.Join(cache, "a-stack", "hooks", agent)
}

// hookCacheFile is the path of one of the Agent's hook files for a harness
// session, the mail nudge's cursor file or the Stop guard's marker, and makes
// its folder.
func hookCacheFile(agent, session, suffix string) (string, bool) {
	cache, err := os.UserCacheDir()
	if err != nil || session == "" || !stack.ValidID(agent) {
		return "", false
	}
	dir := hookCacheDir(cache, agent)
	if os.MkdirAll(dir, 0o700) != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(session))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+suffix), true
}

// hookMailNudge peeks at new Task mail and returns a notice that names the
// Tasks where someone else wrote, or "". It keeps its own mail cursor file
// per Agent and harness session in the user cache folder, so the cursor file
// the Agent reads its mail with does not move. The first check of a harness
// session catches up normal mail silently, then scans it every 30 seconds.
// Urgent mail uses a separate body-free peek at every eligible boundary.
// Neither path re-arms Herdr notices or changes acknowledgements.
func hookMailNudge(ctx context.Context, client *stack.Client, id hookIdentity, in hookInput) (text string) {
	file, ok := hookCacheFile(id.agent, in.SessionID, ".json")
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	release, ok := tryLock(dir)
	if !ok {
		return "" // another hook run of this Agent checks right now
	}
	defer release()
	urgent := ""
	if in.Event != "SessionStart" && in.Event != "SubagentStart" {
		urgent = hookUrgentNudge(ctx, client, id, strings.TrimSuffix(file, ".json")+".urgent.json")
	}
	defer func() {
		if urgent != "" {
			text = urgent
		}
	}()
	info, err := os.Stat(file)
	first := err != nil
	if !first && (in.Event == "SessionStart" || time.Since(info.ModTime()) < mailNudgeEvery) {
		return ""
	}
	// The first check reads into a file of its own, which becomes the cursor
	// file only once it has read up to the newest message. A first check that
	// runs out of time goes on at the next hook run, and still reports nothing.
	cursor := file
	if first {
		pruneHookFiles(dir)
		cursor = strings.TrimSuffix(file, ".json") + ".first.json"
	}
	var out struct {
		Data stack.MailCheckPage `json:"data"`
	}
	produce := func(data []byte) error {
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		if first || urgent != "" {
			return nil // the generic urgent nudge also prompts a read of normal mail
		}
		titles := []string{}
		for _, page := range out.Data.Tasks {
			news := false
			for _, m := range page.Messages {
				news = news || (m.SenderID != id.agent && m.Priority != "urgent")
			}
			if !news {
				continue
			}
			data, err := client.Do(ctx, "POST", "/v1/task/show", stack.TaskReadRequest{Context: id.ref, TaskID: page.TaskID})
			var show struct {
				Data stack.TaskDetail `json:"data"`
			}
			if err != nil {
				return fmt.Errorf("nudge title lookup: %w", err)
			}
			if err := json.Unmarshal(data, &show); err != nil {
				return err
			}
			titles = append(titles, `"`+printable(truncate(show.Data.Title, 80))+`"`)
		}
		if len(titles) > 0 {
			sort.Strings(titles)
			text = "a-stack: New Task mail on " + strings.Join(titles, ", ") + "; load the a-stack skill, then run mail check."
		}
		return nil
	}
	for range catchUpPages {
		_, err := mailCheck(ctx, client, stack.MailCheckRequest{Context: id.ref, Peek: true}, cursor, true, produce)
		if e, ok := err.(*stack.Error); ok && e.Code == "invalid_input" {
			_ = os.Remove(cursor) // a cursor that no longer fits: start again
		} else if err != nil {
			now := time.Now()
			_ = os.Chtimes(file, now, now) // wait 30 seconds before trying again
		}
		if err != nil {
			return ""
		}
		if first && !out.Data.More {
			_ = stack.Rename(cursor, file)
		}
		if !first || !out.Data.More {
			break
		}
	}
	return text
}

const urgentMailNudge = "a-stack: Urgent Task mail; load the a-stack skill, then run your own mail check before continuing."

type urgentHookCursor struct {
	Identity  string                              `json:"identity"`
	Positions map[string]stack.UrgentMailPosition `json:"positions"`
	AfterTask string                              `json:"after_task,omitempty"`
}

// Called under the same lock as the normal scan, but never its throttle.
func hookUrgentNudge(ctx context.Context, client *stack.Client, id hookIdentity, file string) string {
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(client.Discovery.DatabaseID+"\n"+id.ref.WorkspaceID+"\n"+id.ref.SessionID+"\n"+client.Credential.Token)))
	cursor := urgentHookCursor{Identity: identity, Positions: map[string]stack.UrgentMailPosition{}}
	f, err := stack.Open(file)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, stack.MaxRequestBytes+1))
		f.Close()
		if readErr != nil || len(data) > stack.MaxRequestBytes || json.Unmarshal(data, &cursor) != nil || cursor.Identity != identity || cursor.Positions == nil {
			return ""
		}
	} else if !os.IsNotExist(err) {
		return ""
	}
	data, err := client.Do(ctx, "POST", "/v1/mail/urgent", stack.UrgentMailRequest{Context: id.ref, Positions: cursor.Positions, AfterTask: cursor.AfterTask})
	if err != nil {
		return ""
	}
	var out struct {
		Data stack.UrgentMailPage `json:"data"`
	}
	if json.Unmarshal(data, &out) != nil {
		return ""
	}
	for task, pos := range out.Data.Positions {
		cursor.Positions[task] = pos
	}
	cursor.AfterTask = out.Data.NextTask
	if stack.ReplacePrivate(file, cursor) != nil {
		return ""
	}
	if out.Data.Urgent {
		return urgentMailNudge
	}
	return ""
}

// pruneHookFiles removes the cursor files, Stop guard and session markers of harness
// sessions unused for hookFilesKept.
func pruneHookFiles(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && (strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".stop") || strings.HasSuffix(e.Name(), ".main") || strings.HasSuffix(e.Name(), ".away")) && time.Since(info.ModTime()) > hookFilesKept {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
