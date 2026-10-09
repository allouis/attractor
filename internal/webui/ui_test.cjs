// No npm dependencies or frontend build: exercise the core shipped in both pages.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
function core(page) {
  const html = fs.readFileSync(path.join(__dirname, page), 'utf8');
  const source = html.match(/<script id="ui-core">([\s\S]*?)<\/script>/)?.[1];
  assert.ok(source, `${page} must ship its dependency-free UI core`);
  const scope = {};
  vm.runInNewContext(source.replace("/* UI_CORE */", fs.readFileSync(path.join(__dirname, "ui-core.js"), "utf8")), scope, {filename: page});
  return scope.UI;
}
for (const page of ['hub.html', 'waterfall.html']) {
  test(`${page}: live, disconnected and archived records have honest states`, () => {
    const u = core(page);
    assert.equal(u.runState({reachable:true, archived:false}, {status:'running'}).kind, 'running');
    assert.equal(u.runState({reachable:true, archived:false}, {status:'running', pending_questions:[{id:'q'}]}).kind, 'waiting');
    assert.equal(u.runState({reachable:false, archived:false}, {status:'running'}).kind, 'disconnected');
    assert.equal(u.runState({reachable:false, archived:true}, {status:'running',pending_questions:[{id:'q'}]}).kind, 'snapshot');
    assert.equal(u.runState({reachable:false, archived:true}, {status:'completed'}).kind, 'completed');
    assert.equal(u.runState({reachable:true, archived:false}, {status:'completed'}).kind, 'completed');
    assert.equal(u.runState({}, {}).kind, 'unknown');
  });
  test(`${page}: only exact named pipeline matches fold into history groups`, () => {
    const u = core(page), runs=[{run_id:'a',name:'Task',graph_name:'one'},{run_id:'b',name:'Task',graph_name:'one'},{run_id:'c',name:'Task',graph_name:'two'},{run_id:'d',goal:'Task',graph_name:'one'},{run_id:'e',goal:'Task',graph_name:'one'}];
    assert.equal(u.groupRuns(runs,false).length,5);
    const groups=u.groupRuns(runs,true);
    assert.equal(groups.length,4);
    assert.deepEqual(Array.from(groups[0].runs,x=>x.run_id),['a','b']);
    assert.equal(u.inWindow({ended_at:'2026-10-01T12:00:00Z'},7,Date.parse('2026-10-08T12:00:00Z')),true);
    assert.equal(u.inWindow({ended_at:'2026-10-01T11:59:59Z'},7,Date.parse('2026-10-08T12:00:00Z')),false);
    assert.equal(u.inWindow({},7,Date.now()),false);
  });
  test(`${page}: parallel timestamps share one compressed coordinate; crowded targets separate`, () => {
    const u=core(page), start=Date.parse('2026-10-08T10:00:00Z'), end=start+30*60000;
    const s=(node,a,b,gate=false)=>({node_id:node,visit:1,attempt:1,started_at:new Date(start+a*60000).toISOString(),ended_at:new Date(start+b*60000).toISOString(),is_gate:gate});
    const spans=[s('gate',3,15,true),s('review',3,20),s('check',15,15.01),s('work',20,30)];
    const axis=u.friendlyAxis(spans,end);
    assert.equal(axis.at(u.ms(spans[0].started_at)),axis.at(u.ms(spans[1].started_at)));
    assert.ok(axis.at(u.ms(spans[1].ended_at))>axis.at(u.ms(spans[1].started_at)));
    assert.ok(axis.at(u.ms(spans[2].ended_at))-axis.at(u.ms(spans[2].started_at))>=44);
    assert.ok(axis.at(end)>=axis.at(start+15*60000));
    const packed=u.pack([{x:20,w:44},{x:25,w:44},{x:80,w:44},{x:85,w:44}]);
    for(let i=0;i<packed.length;i++)for(let j=i+1;j<packed.length;j++)if(packed[i].slot===packed[j].slot)assert.ok(packed[j].x>=packed[i].x+packed[i].w+8);
  });
  test(`${page}: dependency ranking preserves forward skips and explicit cycle feedback`,()=>{
    const u=core(page),groups=['start','typecheck','lint','plan','gate','revise','build'].map(id=>({id}));
    const edges=[['start','typecheck'],['typecheck','lint'],['typecheck','plan'],['lint','plan'],['plan','gate'],['gate','revise'],['revise','gate'],['gate','build']].map(([from,to])=>({from,to}));
    const {ranks,back}=u.rankGraph(groups,edges);
    assert.ok(ranks.get('plan')>ranks.get('lint'));
    assert.ok(!back.has(u.edgeKey(edges[2])));
    assert.ok(back.has(u.edgeKey(edges[6])));
    for(const e of edges)if(!back.has(u.edgeKey(e)))assert.ok(ranks.get(e.to)>ranks.get(e.from));
  });
  test(`${page}: identities, missing times and ordinary labels stay precise`,()=>{
    const u=core(page),s={node_id:'work',visit:2,attempt:3,started_at:'2026-10-08T10:00:00Z',ended_at:'2026-10-08T10:00:42Z'};
    assert.equal(u.duration(s,Date.now()),42000);
    assert.equal(u.barText(s,Date.now()),'42s');
    assert.equal(u.barText({...s,is_gate:true},Date.now()),'◇ v2 a3');
    assert.notEqual(u.spanKey(s),u.spanKey({...s,attempt:2}));
    assert.equal(u.duration({started_at:'missing'},Date.now()),null);
    assert.equal(u.friendlyAxis([],Date.now()).at(Date.now()),0);
  });
}
test('NDJSON activity is text, while JSON documents are decoded',()=>{
  const u=core('waterfall.html');
  assert.equal(u.isJSON('application/x-ndjson'),false);
  assert.equal(u.isJSON('application/json; charset=utf-8'),true);
  assert.equal(u.ms('0001-01-01T00:00:00Z'),null);
  const graph={nodes:[{id:'retry'}],edges:[{from:'retry',to:'retry'}]};
  assert.equal(u.groupsFor(graph,'custom',false).edges.length,1);
});
test('milestone summaries use latest outcomes, and missing completion stays unknown',()=>{
  const u=core('hub.html'), members=['typecheck','lint'];
  const good={node_id:'typecheck',ended_at:'2026-10-08T10:00:10Z',outcome:'success'};
  assert.equal(u.milestone([good,{...good,node_id:'lint',outcome:'fail'}],members,[]).label,'Failed');
  assert.equal(u.milestone([good,{...good,node_id:'lint',outcome:'running',ended_at:null}],members,['lint']).label,'Active');
  assert.equal(u.milestone([good,{...good,node_id:'lint',outcome:'fail'},{...good,node_id:'lint',attempt:2}],members,[]).label,'Done');
  assert.equal(u.milestone([good],members,[]).label,'Visited');
  assert.equal(u.runElapsed({status:'completed',started_at:'2026-10-08T10:00:00Z',last_seen:'2026-10-08T11:00:00Z',state:{kind:'completed'}},Date.now()),null);
});
