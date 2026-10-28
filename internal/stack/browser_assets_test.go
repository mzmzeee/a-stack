package stack

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)


var assetTokens = regexp.MustCompile("//[^\\n]*|/\\*[\\s\\S]*?\\*/|'(?:\\\\.|[^'\\\\])*'|\"(?:\\\\.|[^\"\\\\])*\"|\\x60(?:\\\\.|[^\\x60\\\\])*\\x60|[\\p{L}\\p{N}_$]+|[^\\s]")

// Keep quoted values and word boundaries intact while ignoring syntax whitespace.
func assetContains(source, fragment string) bool {
	if strings.Contains(source, fragment) {
		return true
	}
	tokens := func(s string) string {
		return "\x00" + strings.Join(assetTokens.FindAllString(s, -1), "\x00") + "\x00"
	}
	return strings.Contains(tokens(source), tokens(fragment))
}

func TestAssetContainsPreservesValues(t *testing.T) {
	for _, tt := range []struct {
		name, source, fragment string
		want bool
	}{
		{"syntax wrapping", "el(\n 'span', 'chip notice-off',\n 'Notices off'\n)", "el('span','chip notice-off','Notices off')", true},
		{"missing class space", "el('span', 'chipnotice-off', 'Notices off')", "el('span','chip notice-off','Notices off')", false},
		{"attribute wrapping", "<div\n class=\"chip notice-off\"\n hidden>", "<div class=\"chip notice-off\" hidden>", true},
		{"attribute value", "<div class=\"chipnotice-off\" hidden>", "<div class=\"chip notice-off\" hidden>", false},
		{"escaped quote", `f('a\' b')`, `f('a\'b')`, false},
		{"word boundary", "constwhere = 1", "const where=1", false},
		{"comment apostrophe", "// the tab's marker\nf('chipnotice-off')", "f('chip notice-off')", false},
		{"template value", "f(`chipnotice-off`)", "f(`chip notice-off`)", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := assetContains(tt.source, tt.fragment); got != tt.want {
				t.Fatalf("assetContains(%q, %q) = %v, want %v", tt.source, tt.fragment, got, tt.want)
			}
		})
	}
}

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
	contains := assetContains
	allowed := map[string]bool{"/v1/browser/redeem": true, "/v1/session/snapshot": true, "/v1/session/page": true, "/v1/task/show": true, "/v1/mail/read": true, "/v1/task/answer": true}
	calls := regexp.MustCompile(`(?:fetch|request)\(\s*'([^']*)'`).FindAllStringSubmatch(js, -1)
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
		if !contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	// Notice markers use the existing Agent read, with no new endpoint or write.
	for _, want := range []string{"el('span','chip notice-off','Notices off')", "g.a.notice_warning", "r.notice.hidden=!warning", "${warning.reason}: ${warning.fix}", "r.notice.setAttribute('aria-label'"} {
		if !contains(js, want) {
			t.Errorf("notice marker lacks %s", want)
		}
	}
	// Storage accesses must stay inside a try, regardless of line wrapping.
	unguarded := regexp.MustCompile(`try\s*\{[^{}]*\}\s*catch`).ReplaceAllString(js, "")
	if strings.Contains(unguarded, "localStorage") || strings.Contains(unguarded, "sessionStorage") {
		t.Error("app.js uses storage outside a simple try/catch block")
	}
	// The peek is a non-modal dialog with its own live title; the keyboard map is a modal dialog with a footer button.
	for _, want := range []string{`<div class="peek" id="peek" role="dialog" aria-labelledby="peek-title" hidden>`, `id="keys-dialog"`, `id="keys-button" aria-keyshortcuts="?"`} {
		if !contains(page, want) {
			t.Errorf("index.html lacks %s", want)
		}
	}
	if contains(page, "aria-modal") || !contains(js, "peekTitle.setAttribute('aria-live','polite')") {
		t.Error("the peek must be non-modal with a polite live title")
	}
	// "New since you looked" is kept per Session path and saved when the tab is hidden or closed.
	for _, want := range []string{"const seenKey='a-stack-seen:'+location.pathname", "addEventListener('pagehide',markSeen)", "if(document.hidden){markSeen();"} {
		if !contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	// Single-key shortcuts work only with the focus in the Graph (with its peek) or the Agents tab (WCAG 2.1.4).
	if !contains(js, "const where=panel.contains(e.target)||peekBox.contains(e.target)?'graph':agentsPanel.contains(e.target)?'agents':''") || !contains(js, "if(!where||e.target.closest('input, select, textarea')||e.target.isContentEditable){resetKeys();return;}") {
		t.Error("the key handler must stay limited to the Graph and Agents tabs")
	}
	for _, bar := range []string{"top", "session", "footer"} {
		if !contains(page, `data-bar="`+bar+`" aria-controls="`+bar+`-content" aria-expanded="true"`) {
			t.Errorf("%s bar needs a labelled disclosure button", bar)
		}
	}
	for _, want := range []string{"save('a-stack-bar-'+name", "load('a-stack-bar-'+name)", "function linkedTask(", "function toggleBars(", "e.repeat?null:()=>toggleBars", "setText(document.querySelector('.preview-label'),version ||", "getAttribute('aria-label')", "classList.toggle('lost',stale||!!notice)"} {
		if !contains(js, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
	for _, want := range []string{`id="compact-attention"`, `id="collapsed-title"`, `id="navigation-hint" role="status"`, "<kbd>h</kbd> <kbd>l</kbd>", "<kbd>H</kbd> <kbd>L</kbd>", "<kbd>t</kbd>", "<kbd>z</kbd> <kbd>z</kbd>"} {
		if !contains(page, want) {
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
