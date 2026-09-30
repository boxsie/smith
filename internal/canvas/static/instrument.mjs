// Pure projections of the existing service journal. Never infer a node's
// current verdict from a human_decision copied through an upstream payload.
export function screenFor(params) {
  if (params.get('section') === 'tickets') return 'tickets';
  if (['patch','launch','run','sources','fixture_sources'].some(key => params.has(key))) return 'patch';
  if (params.has('project')) return 'project';
  return ['settings','integrations'].includes(params.get('screen')) ? params.get('screen') : 'home';
}
export function nodeEvents(events, id) {
  const invocations = new Set(events.filter(e => e.invocation?.node_id === id).map(e => e.invocation.id));
  return events.filter(e => e.node_id === id || e.invocation?.node_id === id || invocations.has(e.invocation_id));
}
export function nodeFeedback(events, id) {
  const own = nodeEvents(events,id), starts = own.filter(e => e.type === 'invocation.started');
  const current = starts.at(-1)?.invocation?.id || starts.at(-1)?.invocation_id;
  const attempt = current ? own.filter(e => (e.invocation?.id || e.invocation_id) === current) : [];
  const terminal = attempt.filter(e => /^invocation\.(completed|failed|cancelled|interrupted)$/.test(e.type)).at(-1);
  const checks = attempt.filter(e => e.type === 'checks.finished');
  let tone = 'idle', label = starts.length ? 'attempt in progress' : 'not reached';
  if (terminal) {tone = terminal.type === 'invocation.completed' ? 'recorded' : 'failed'; label = terminal.type === 'invocation.completed' ? 'attempt completed' : terminal.type.replace('invocation.','attempt ');}
  if (checks.length) {tone = checks.every(e => e.data?.passed === true) ? 'recorded' : 'failed';label = `${checks.filter(e => e.data?.passed === true).length}/${checks.length} checks passed`;}
  const observation=attempt.filter(e=>e.type==='node.observed').at(-1);
  if(observation?.reason==='runtime decision recorded'&&typeof observation.data?.output?.accepted==='boolean'){tone=observation.data.output.accepted?'recorded':'failed';label=observation.data.output.accepted?'review accepted':'changes requested';}
  if(observation?.reason==='required capability calls observed'){tone='recorded';label='proof verified';}
  if(observation?.reason==='workspace handoff sealed'){tone='recorded';label='handoff sealed';}
  if(attempt.some(e=>e.type==='gate.resolved')){tone='recorded';label='approved';}
  if (attempt.some(e => e.type === 'outlet.emitted' && e.port_id === 'revise')) {tone='failed';label='returned for changes';}
  if (terminal && terminal.type !== 'invocation.completed') {tone='failed';label=terminal.type.replace('invocation.','attempt ');}
  return {tone,label,attempts:starts.length,events:attempt};
}
export function cordMessages(events, id) {
  const messages = new Map();
  for (const event of events) {
    const envelope = event.envelope;
    if (envelope?.cord_id === id && !messages.has(envelope.id)) messages.set(envelope.id,{...envelope,sequence:event.sequence});
  }
  return [...messages.values()];
}

// This is a display layout for the known ticket harness, not patch bytecode.
// Other graphs and all authoring surfaces retain their authored positions.
export const harnessPositions={ticket_intake:[40,90],intake_proof:[280,90],research_route:[520,90],grok_challenge:[760,90],fable_plan:[1000,90],codex_work:[1240,90],seal_handoff:[1240,320],deterministic_checks:[760,320],tests_route:[520,320],review_route:[280,320],fable_review:[40,550],grok_review:[280,550],review_decision:[520,550],human_gate:[760,550],ticket_close:[1000,550],completion_proof:[1240,550]};
export function isTicketHarness(nodes) {return nodes.length===Object.keys(harnessPositions).length&&nodes.every(n=>Object.hasOwn(harnessPositions,n.id));}
