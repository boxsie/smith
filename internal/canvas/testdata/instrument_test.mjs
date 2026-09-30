import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {screenFor,nodeFeedback,cordMessages,isTicketHarness,harnessPositions} from '../static/instrument.mjs';
const start=(id,node_id,sequence)=>({type:'invocation.started',invocation:{id,node_id},sequence});
test('home is distinct from project and deep links still select their run',()=>{
 for(const [query,screen] of [['','home'],['project=p','project'],['patch=p&run=r','patch'],['launch=l&run=r','patch'],['project=p&section=tickets','tickets'],['screen=settings','settings'],['screen=integrations','integrations']]) assert.equal(screenFor(new URLSearchParams(query)),screen);
});
test('current node never inherits earlier revise or last successful check',()=>{
 const events=[start('a','work',1),{type:'outlet.emitted',invocation_id:'a',port_id:'revise'},start('b','work',3),{type:'invocation.completed',invocation_id:'b',data:{human_decision:{approved:false}}}];
 assert.equal(nodeFeedback(events,'work').label,'attempt completed');
 const checks=[start('c','checks',5),{type:'checks.finished',invocation_id:'c',data:{passed:false}},{type:'checks.finished',invocation_id:'c',data:{passed:true}}];
 assert.equal(nodeFeedback(checks,'checks').label,'1/2 checks passed');assert.equal(nodeFeedback(checks,'checks').tone,'failed');
});
test('facts outside the old 300 event tape remain attributable',()=>{
 const events=[start('a','work',1),...Array.from({length:500},(_,i)=>({type:'runtime.emitted',sequence:i+2,invocation_id:'a'})),{type:'invocation.completed',invocation_id:'a',sequence:503}];
 assert.equal(nodeFeedback(events,'work').label,'attempt completed');assert.equal(nodeFeedback(events,'work').attempts,1);
});
test('wire repeated lifecycle records are one message; distinct envelopes retained',()=>{
 const envelope={id:'a',cord_id:'wire',payload:{sha256:'sha256:abc'}};
 assert.deepEqual(cordMessages([{sequence:1,envelope},{sequence:2,envelope},{sequence:3,envelope:{...envelope,id:'b'}}],'wire').map(m=>m.id),['a','b']);
});
test('view-only arrangement recognizes exactly the shipped harness, without mutating nodes',()=>{
 const nodes=Object.keys(harnessPositions).map(id=>({id,layout:{x:123,y:456}}));const before=JSON.stringify(nodes);
 assert(isTicketHarness(nodes));assert(!isTicketHarness(nodes.slice(1)));assert.equal(JSON.stringify(nodes),before);
});
test('observed own-node review and proof use actual journal schemas',()=>{
 assert.equal(nodeFeedback([start('r','review',1),{type:'node.observed',invocation_id:'r',reason:'runtime decision recorded',data:{output:{accepted:true}}}],'review').label,'review accepted');
 assert.equal(nodeFeedback([start('p','proof',1),{type:'node.observed',invocation_id:'p',reason:'required capability calls observed'}],'proof').label,'proof verified');
});
test('fit clears native browser scroll before applying world framing',()=>{
 const source=readFileSync(new URL('../static/app.js',import.meta.url),'utf8');
 const viewport={clientWidth:620,clientHeight:700,scrollLeft:568,scrollTop:90};
 const store={selected:{type:'node',id:'a'},historyOpen:true};
 const context=vm.createContext({store,$:()=>viewport,topology:()=>({nodes:[{id:'a'}]}),displayLayout:()=>({x:40,y:90}),nodeSize:()=>({width:200,height:116}),renderInspector(){},renderView(){}});
 vm.runInContext(source.slice(source.indexOf('function fitPatch()'),source.indexOf('function beginNodeDrag(')),context);
 context.fitPatch();
 assert.equal(viewport.scrollLeft,0);assert.equal(viewport.scrollTop,0);
 assert.equal(store.selected,null);assert.equal(store.historyOpen,false);
 assert(Number.isFinite(store.view.x)&&Number.isFinite(store.view.y));
});
test('startup reveals only on success; failures stay concealed with retry',()=>{
 const source=readFileSync(new URL('../static/app.js',import.meta.url),'utf8');
 const make=()=>{
  const nodes=new Map();let retried=false;
  const $=id=>{if(!nodes.has(id))nodes.set(id,{hidden:false,attributes:{},setAttribute(k,v){this.attributes[k]=v;},removeAttribute(k){delete this.attributes[k];}});return nodes.get(id);};
  $('#app').setAttribute('data-startup','');$('#app').setAttribute('aria-busy','true');
  const context=vm.createContext({$,location:{reload(){retried=true;}}});
  vm.runInContext(source.slice(source.indexOf('function finishBoot('),source.indexOf('function bindStaticControls(')),context);
  return {$,context,retried:()=>retried};
 };
 const ready=make();ready.context.finishBoot();
 assert.equal(ready.$('#page-loading').hidden,true);
 assert(!Object.hasOwn(ready.$('#app').attributes,'data-startup'));
 const failed=make();failed.context.finishBoot(new Error('offline'));
 assert(Object.hasOwn(failed.$('#app').attributes,'data-startup'));
 assert.equal(failed.$('#app').attributes['aria-busy'],'false');
 assert.equal(failed.$('#page-loading').attributes.role,'alert');
 assert.match(failed.$('#startup-message').textContent,/offline/);
 failed.$('#startup-retry').onclick();assert(failed.retried());
});
