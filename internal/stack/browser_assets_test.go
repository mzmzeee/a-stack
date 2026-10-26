package stack

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The view reads, and makes one write: the Operator's answer to a Needs you question. Its keys and storage stay in
// their bounds. These checks read the embedded files, so a new feature cannot quietly call another endpoint, add a
// modal peek or touch storage outside a try.
func TestBrowserAssetsStayReadOnly(t *testing.T) {
	read := func(name string) string {
		b, err := browserFiles.ReadFile("browser/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	js, page := read("app.js"), read("index.html")
	allowed := map[string]bool{"/v1/browser/redeem": true, "/v1/session/snapshot": true, "/v1/session/page": true, "/v1/task/show": true, "/v1/mail/read": true, "/v1/task/answer": true}
	calls := regexp.MustCompile(`(?:fetch|request)\('([^']*)'`).FindAllStringSubmatch(js, -1)
	if len(calls) != len(allowed) {
		t.Errorf("app.js has %d endpoint calls, want %d", len(calls), len(allowed))
	}
	for _, c := range calls {
		if !allowed[c[1]] {
			t.Errorf("app.js calls %s", c[1])
		}
	}
	// The answer: at most 4,096 bytes, a fresh operation ID per answer, the same one on a retry.
	for _, want := range []string{"over=n>4096", "answer.op ||= 'view-'", "answerInput.addEventListener('input',()=>{answer.op='';"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	// Storage can be missing or throw (private windows, blocked site data): every access sits in a try.
	for i, line := range strings.Split(js, "\n") {
		if (strings.Contains(line, "localStorage") || strings.Contains(line, "sessionStorage")) && !strings.Contains(line, "try") {
			t.Errorf("app.js:%d uses storage outside a try", i+1)
		}
	}
	// The peek is a non-modal dialog with its own live title; the keyboard map is a modal dialog with a footer button.
	for _, want := range []string{`<div class="peek" id="peek" role="dialog" aria-labelledby="peek-title" hidden>`, `id="keys-dialog"`, `id="keys-button" aria-keyshortcuts="?"`} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	if strings.Contains(page, "aria-modal") || !strings.Contains(js, "peekTitle.setAttribute('aria-live','polite')") {
		t.Error("the peek must be non-modal with a polite live title")
	}
	// "New since you looked" is kept per Session path and saved when the tab is hidden or closed.
	for _, want := range []string{"const seenKey='a-stack-seen:'+location.pathname", "addEventListener('pagehide',markSeen)", "if(document.hidden){markSeen();"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	// Single-key shortcuts work only with the focus in the Graph (with its peek) or the Agents tab (WCAG 2.1.4).
	if !strings.Contains(js, "const where=panel.contains(e.target)||peekBox.contains(e.target)?'graph':agentsPanel.contains(e.target)?'agents':''") || !strings.Contains(js, "if(!where||e.target.closest('input, select, textarea')||e.target.isContentEditable){resetKeys();return;}") {
		t.Error("the key handler must stay limited to the Graph and Agents tabs")
	}
	for _, bar := range []string{"top", "session", "footer"} {
		if !strings.Contains(page, `data-bar="`+bar+`" aria-controls="`+bar+`-content" aria-expanded="true"`) {
			t.Errorf("%s bar needs a labelled disclosure button", bar)
		}
	}
	for _, want := range []string{"save('a-stack-bar-'+name", "load('a-stack-bar-'+name)", "function linkedTask(", "function toggleBars(", "e.repeat?null:()=>toggleBars", "setText(document.querySelector('.preview-label'),version ||", "getAttribute('aria-label')", "classList.toggle('lost',stale||!!notice)"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	for _, want := range []string{`id="compact-attention"`, `id="collapsed-title"`, `id="navigation-hint" role="status"`, "<kbd>h</kbd> <kbd>l</kbd>", "<kbd>H</kbd> <kbd>L</kbd>", "<kbd>t</kbd>", "<kbd>z</kbd> <kbd>z</kbd>"} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
}

// Node is part of the development checks. This runs the actual key handler against a small DOM stand-in;
// browser QA covers layout, real focus, storage failure and accessibility with the embedded assets.
func TestBrowserNavigation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed for browser navigation tests")
	}
	if out, err := exec.Command(node, "testdata/browser-navigation.cjs").CombinedOutput(); err != nil {
		t.Fatalf("browser navigation: %v\n%s", err, out)
	}
}
