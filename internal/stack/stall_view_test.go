package stack

import (
	"os/exec"
	"regexp"
	"testing"
)

func TestBrowserMailWaitPlainText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	js, err := browserFiles.ReadFile("browser/app.js")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^  const report=firstLine\(t.report_preview\).*;$`).Find(js)
	if len(line) == 0 {
		t.Fatal("missing Now wait text")
	}
	script := `const assert=require('node:assert/strict');
const firstLine=text=>(text||'').split('\n')[0],plural=(n,s)=>n+' '+s;
const setText=(node,text)=>{node.textContent=text};
const c={note:{}}, t={mail_waits:[{recipient_name:'<img src=x onerror=alert(1)>',minutes:34}]};
` + string(line) + `
assert.equal(c.note.textContent,'Waiting on <img src=x onerror=alert(1)> for 34 min. No report yet.');
assert.equal(c.note.innerHTML,undefined);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
