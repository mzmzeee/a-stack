package stack

import (
	"os/exec"
	"testing"
)

func TestNowLoadingAndFilters(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	js, err := browserFiles.ReadFile("browser/app.js")
	if err != nil {
		t.Fatal(err)
	}

	source := string(js)
	loading := assetBetween(t, source, "if (!model) {", "setText(document.querySelector('.workspace-title h1')")
	stalls := assetBetween(t, source, "const stalled =", "const liveStalls =")

	matches := assetBetween(t, source, "function matches(t)", "function status(n, t)")
	script := `const assert=require('node:assert/strict');
const board={style:{}}, stalledSection={style:{},hidden:false};
const nowPanel={firstElementChild:stalledSection,querySelector:()=>board};
const graph={parentElement:{style:{}}};
const element={querySelector:()=>({})};
const $=id=>id==='now-board'?{parentElement:board}:element;
const setText=()=>{},updateSource=()=>{};
let model=null,notice='',failures=0;
function loading(){` + loading + `}
for(const failed of [false,true]) {
 notice=failed?'Failed':'';
 board.style.visibility='visible'; stalledSection.hidden=false;
 loading();
 assert.equal(board.style.visibility,'hidden','unknown Session must not show an empty board');
 assert.equal(stalledSection.hidden,true,'unknown Session must hide Stalled');
}
const state={query:'',filter:'all'},owners=()=>[];
` + matches + `
model={tasks:[{id:'one',title:'Needle',progress:'working',mail_waits:[{stalled:true}]},{id:'two',title:'Other',progress:'waiting',mail_waits:[{stalled:true}]}]};
function filtered(){` + stalls + `return stalled.map(s=>s.t.id)}
assert.deepEqual(filtered(),['one','two']);
state.query='Needle'; assert.deepEqual(filtered(),['one']);
state.query=''; state.filter='waiting'; assert.deepEqual(filtered(),['two']);
state.query='Needle'; assert.deepEqual(filtered(),[]); assert.equal(stalledSection.hidden,true);
state.filter='all'; state.query=''; assert.deepEqual(filtered(),['one','two']); assert.equal(stalledSection.hidden,false);
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
