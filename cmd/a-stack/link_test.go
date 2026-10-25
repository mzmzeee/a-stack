package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// One task create links and assigns the new Task, with Agent names, and a
// create without --after in a Session with open Tasks prints a hint on stderr.
func TestCLITaskCreateLinksAssignsAndHints(t *testing.T) {
	f := newTeam(t)
	create := f.scoped("task", "create", "--title", "Review the parser", "--after", f.task, "--assign", "02-worker", "--assign", "01-orchestrator", "--operation-id", "linked-create", "--json")
	out, errout, code := f.run(f.orchFile, create...)
	var receipt struct {
		Data stack.Receipt `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &receipt) != nil || errout != "" {
		t.Fatalf("linked create: exit %d, stdout %s, stderr %s", code, out, errout)
	}
	if receipt.Data.Revision != 3 {
		t.Fatalf("revision %d, want 3", receipt.Data.Revision)
	}
	linked := receipt.Data.ResourceID
	// A retry returns the same receipt and prints no hint.
	if again, errout, code := f.run(f.orchFile, create...); code != 0 || again != out || errout != "" {
		t.Fatalf("retry: exit %d, %s %s", code, again, errout)
	}
	deps, _, _ := f.run(f.orchFile, f.scoped("task", "dependencies", "--task", linked)...)
	assignees, _, _ := f.run(f.orchFile, f.scoped("task", "assignees", "--task", linked)...)
	if !strings.Contains(deps, `"prerequisite_id":"`+f.task+`"`) || !strings.Contains(assignees, f.worker) || !strings.Contains(assignees, f.orch) {
		t.Fatalf("links: %s %s", deps, assignees)
	}

	out, errout, code = f.run(f.orchFile, f.scoped("task", "create", "--title", "Loose end", "--operation-id", "loose-create", "--json")...)
	if code != 0 || json.Unmarshal([]byte(out), &receipt) != nil || receipt.Data.Revision != 1 {
		t.Fatalf("plain create: exit %d %s %s", code, out, errout)
	}
	if strings.Count(errout, "\n") != 1 || !strings.Contains(errout, "hint: the new Task has no prerequisites, and 2 other Task(s) are open") || strings.Contains(out, "hint") {
		t.Fatalf("hint: stdout %s stderr %q", out, errout)
	}

	// --agent takes a name too; an unknown name fails before any change.
	loose := receipt.Data.ResourceID
	f.receipt(f.orchFile, f.scoped("task", "assign", "--task", loose, "--agent", "02-worker", "--expected-revision", "1", "--operation-id", "assign-by-name")...)
	_, errout, code = f.run(f.orchFile, f.scoped("task", "create", "--title", "X", "--assign", "03-nobody", "--operation-id", "nobody-create")...)
	if code != 2 || !strings.Contains(errout, "--assign 03-nobody is no Agent of this Session; see a-stack agent list") {
		t.Fatalf("unknown name: exit %d %s", code, errout)
	}
	tasks, _, _ := f.run(f.orchFile, f.scoped("task", "list")...)
	if strings.Count(tasks, `"title"`) != 3 {
		t.Fatalf("Tasks after the failed create: %s", tasks)
	}
}
