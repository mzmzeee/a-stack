package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// hookCacheDir is the folder of one Agent's hook cursor files in the user
// cache folder. On Windows that is %LOCALAPPDATA%, where a-stack is the
// default state directory, so the hook files get a folder of their own.
func hookCacheDir(cache, agent string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(cache, "a-stack-hooks", agent)
	}
	return filepath.Join(cache, "a-stack", "hooks", agent)
}

// hookMailNudge peeks at new Task mail and returns a notice that names the
// Tasks where someone else wrote, or "". It keeps its own mail cursor file
// per Agent and harness session in the user cache folder, so the cursor file
// the Agent reads its mail with does not move. The first check of a harness
// session, at SessionStart or else at the first tool call, reads up to the
// newest message and reports nothing. Later checks run at most every 30
// seconds. The cursor moves past each message, so no message is reported
// twice, and the notice holds no sender and no text.
func hookMailNudge(ctx context.Context, client *stack.Client, id hookIdentity, in hookInput) string {
	cache, err := os.UserCacheDir()
	if err != nil || in.SessionID == "" || !stack.ValidID(id.agent) {
		return ""
	}
	dir := hookCacheDir(cache, id.agent)
	if os.MkdirAll(dir, 0o700) != nil {
		return ""
	}
	release, ok := tryLock(dir)
	if !ok {
		return "" // another hook run of this Agent checks right now
	}
	defer release()
	sum := sha256.Sum256([]byte(in.SessionID))
	file := filepath.Join(dir, hex.EncodeToString(sum[:16])+".json")
	info, err := os.Stat(file)
	first := err != nil
	if !first && (in.Event == "SessionStart" || time.Since(info.ModTime()) < mailNudgeEvery) {
		return ""
	}
	if first {
		pruneHookFiles(dir)
	}
	news := map[string]bool{}
	for range 20 {
		data, err := mailCheck(ctx, client, stack.MailCheckRequest{Context: id.ref}, file, true)
		if e, ok := err.(*stack.Error); ok && e.Code == "invalid_input" {
			_ = os.Remove(file) // a cursor that no longer fits: start again
		} else if err != nil {
			now := time.Now()
			_ = os.Chtimes(file, now, now) // wait 30 seconds before trying again
		}
		var out struct {
			Data stack.MailCheckPage `json:"data"`
		}
		if err != nil || json.Unmarshal(data, &out) != nil {
			return ""
		}
		for _, page := range out.Data.Tasks {
			for _, m := range page.Messages {
				if m.SenderID != id.agent {
					news[page.TaskID] = true
				}
			}
		}
		if !first || !out.Data.More {
			break
		}
	}
	if first || len(news) == 0 {
		return ""
	}
	titles := []string{}
	for task := range news {
		data, err := client.Do(ctx, "POST", "/v1/task/show", stack.TaskReadRequest{Context: id.ref, TaskID: task})
		var show struct {
			Data stack.TaskDetail `json:"data"`
		}
		if err == nil && json.Unmarshal(data, &show) == nil {
			titles = append(titles, `"`+printable(truncate(show.Data.Title, 80))+`"`)
		}
	}
	if len(titles) == 0 {
		return ""
	}
	sort.Strings(titles)
	return "a-stack: New Task mail on " + strings.Join(titles, ", ") + "; run mail check."
}

// pruneHookFiles removes the cursor files of harness sessions unused for
// hookFilesKept.
func pruneHookFiles(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".json") && time.Since(info.ModTime()) > hookFilesKept {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
