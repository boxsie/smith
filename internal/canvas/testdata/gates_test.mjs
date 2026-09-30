// Fast state/handler regressions over the shipped gate UI functions. The tiny
// element shim is not a layout/browser proof; Chrome verification covers that.
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';

const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const gateSource = source.slice(source.indexOf('function renderGates()'), source.indexOf('function renderInspector()'));
function fixture() {
  const nodes = new Map();
  const $ = key => {
    if (!nodes.has(key)) nodes.set(key, {textContent:'', innerHTML:'', value:'', hidden:false, disabled:false, dataset:{}, handlers:{},
      addEventListener(type, handler) { this.handlers[type] = handler; }, querySelectorAll() { return []; },
      close() { this.open = false; }, showModal() { this.open = true; },
    });
    return nodes.get(key);
  };
  const gate = {request_id:'request-1',run_id:'run-1',node_id:'approval',state:'pending',can_decide:true,
    requested_at:'2026-09-07T10:00:00Z',prompt:'allow this?',payload:{ticket_id:'fixture',changes:['one'],accepted:true,checks_passed:false,review_feedback:'<script>bad</script>'}};
  const store = {session:{root:'/fixture'},runId:'run-1',gates:[gate],gateDecisions:[],gateError:'',gateDrafts:new Map()};
  const calls = [];
  const context = vm.createContext({$,store,Date,JSON,Map,Number,Math,document:{hidden:false},setInterval(){},
    esc:value=>String(value).replace(/[&<>"']/g,char=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char])),
    currentLibraryPatch:()=>({name:'fixture patch'}),notice(){},refreshGates:async()=>{},
    api:async(path,options)=>{calls.push(JSON.parse(options.body)); store.gates=[];store.gateDecisions=[{...gate,state: calls.at(-1).approved?'approved':'rejected',reason:calls.at(-1).reason,can_decide:false}];},
  });
  vm.runInContext(gateSource,context);
  context.openGateReview(gate);
  return {$,gate,store,context,calls};
}

test('pending decisions require non-whitespace reason; evidence is escaped and boolean false is retained',()=>{
  const {$,context} = fixture();
  assert.equal($('#gate-dialog').open,true);
  assert.equal($('#gate-approve').disabled,true);
  $('#gate-reason').value=' \n\t'; $('#gate-reason').handlers.input();
  assert.equal($('#gate-reject').disabled,true);
  $('#gate-reason').value='checked the evidence'; $('#gate-reason').handlers.input();
  assert.equal($('#gate-approve').disabled,false);
  assert.match($('#gate-evidence').innerHTML,/failed/);
  assert.match($('#gate-evidence').innerHTML,/&lt;script&gt;/);
  assert.doesNotMatch($('#gate-evidence').innerHTML,/<script>/);
  assert.match($('#gate-payload-text').textContent,/<script>bad<\/script>/);
  context.renderGates();
  assert.equal($('#gate-reason').value,'checked the evidence');
});

test('draft and full payload expansion survive refresh; close and reopen retain draft',()=>{
  const {$,context,gate} = fixture();
  $('#gate-reason').value='still reviewing'; $('#gate-reason').handlers.input();
  $('#gate-payload').open=true;
  for(let i=0;i<5;i++) context.renderGates();
  assert.equal($('#gate-reason').value,'still reviewing');
  assert.equal($('#gate-payload').open,true);
  context.closeGateReview(); context.openGateReview(gate);
  assert.equal($('#gate-reason').value,'still reviewing');
});

for(const choice of ['approve','reject']) test(`${choice} records exact reason and renders a durable receipt`,async()=>{
  const {$,store,calls} = fixture();
  $('#gate-reason').value=' reason kept verbatim '; $('#gate-reason').handlers.input();
  const event = {preventDefault(){},submitter:{value:choice}};
  const sending = $('#gate-decision-form').handlers.submit(event);
  await $('#gate-decision-form').handlers.submit(event);
  assert.equal($('#gate-approve').disabled,true);
  await sending;
  assert.equal(calls.length,1);
  assert.equal(calls[0].approved,choice==='approve');
  assert.equal(calls[0].reason,' reason kept verbatim ');
  assert.equal($('#gate-decision-form').hidden,true);
  assert.match($('#gate-result').textContent,new RegExp(choice==='approve'?'approved':'rejected'));
  assert.equal(store.gateDrafts.size,0);
});

test('stale failure remains visible with competing receipt and draft retained',async()=>{
  const {$,store,gate,context} = fixture();
  $('#gate-reason').value='my draft'; $('#gate-reason').handlers.input();
  context.api=async()=>{store.gates=[];store.gateDecisions=[{...gate,state:'approved',can_decide:false,reason:'other operator'}];throw new Error('gate is no longer pending');};
  await $('#gate-decision-form').handlers.submit({preventDefault(){},submitter:{value:'reject'}});
  assert.equal($('#gate-error').hidden,false);
  assert.match($('#gate-error').textContent,/no longer pending/);
  assert.match($('#gate-result').textContent,/other operator/);
  assert.equal($('#gate-reason').value,'my draft');
  assert.equal(store.gateDrafts.size,1);
});

test('observer, disconnected and vanished gates cannot be decided',()=>{
  const {$,store,gate,context} = fixture();
  $('#gate-reason').value='ready'; $('#gate-reason').handlers.input();
  gate.can_decide=false;gate.unavailable_reason='owning controller required'; context.renderGateReview();
  assert.equal($('#gate-approve').disabled,true);
  assert.match($('#gate-result').textContent,/owning controller/);
  gate.can_decide=true;store.gateError='offline';context.renderGateReview();
  assert.equal($('#gate-reject').disabled,true);
  store.gates=[];store.gateError='';context.renderGates();
  assert.equal($('#gate-decision-form').hidden,true);
  assert.match($('#gates').innerHTML,/no pending human decisions/);
});

test('out-of-order refresh cannot replace a receipt with an older pending snapshot',async()=>{
  const {store,gate,context} = fixture();
  vm.runInContext(source.slice(source.indexOf('async function refreshGates()'),source.indexOf('async function control(')),context);
  const responses=[];
  context.api=()=>new Promise(resolve=>responses.push(resolve));
  const old=context.refreshGates(), current=context.refreshGates();
  responses[1]({requests:[],decisions:[{...gate,state:'approved'}],last_sequence:20});await current;
  responses[0]({requests:[gate],decisions:[],last_sequence:10});await old;
  assert.equal(store.gates.length,0);
  assert.equal(store.gateDecisions[0].state,'approved');
  assert.equal(store.gateSequence,20);
});
