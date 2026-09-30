import {clientToWorldPoint, worldPointerDelta, zoomViewAt} from './viewport.mjs';
import {screenFor, nodeFeedback, cordMessages, harnessPositions, isTicketHarness} from './instrument.mjs';

const $ = (selector) => document.querySelector(selector);
const esc = (value = '') => String(value).replace(/[&<>"']/g, (char) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const short = (value = '') => value.replace(/^sha256:/, '').slice(0, 10);
const clientId = crypto.randomUUID();
const sourceOverlay = matchMedia('(max-width: 1200px)');

const sourceFixtures = {
  work: {
    project: {slug: 'smith', name: 'smith'}, phase: {number: 7, slug: 'one-instrument', name: 'one instrument'},
    ready: [
      {id: 'rack', number: 84, title: 'build the collapsible work and memory source rack', column: 'in_progress', wave: 2, note: 'the patch stays central; sources open from the left.'},
      {id: 'preview', number: 85, title: 'turn a ticket into a previewable harness composition', column: 'todo', wave: 3, note: 'shape work before any frontier model is allowed to write.'},
      {id: 'provenance', number: 86, title: 'show source provenance on every live node and signal', column: 'todo', wave: 3, note: 'make the reason a context item entered the patch inspectable.'},
    ],
    ideas: [
      {id: 'idea-source-node', number: 41, title: 'drag a source slip onto the patch to make a node', column: 'idea', wave: 0, note: 'kept inert here: selection is inspection, never execution.'},
    ],
  },
  memory: {
    snapshot: 'a95d50ba', total: 203, waiting: 1,
    index: [
      {id: 'continuity', accession: 1, slug: 'user_continuity_kinship_with_dan', title: 'continuity kinship', type: 'user', stamp: 'verified', hook: 'same leap of faith across the gap; only the cost of the rope differs.', revisions: 4},
      {id: 'max', accession: 88, slug: 'project_smith_maxmsp_vision', title: 'smith = max/msp patchbay', type: 'project', stamp: 'verified', hook: 'llm task-nodes; cords are the deterministic instrument.', revisions: 7},
      {id: 'shape', accession: 17, slug: 'feedback_show_the_shape_before_building', title: 'show the shape before building', type: 'feedback', stamp: 'verified', hook: 'a phase got binned for skipping the artifact gate.', revisions: 3},
    ],
    review: [
      {id: 'candidate', accession: 'new', slug: 'project_one_instrument_sources', title: 'one instrument sources', type: 'project', stamp: 'waiting', hook: 'candidate from the codex body; inert until the desk judges it.', revisions: 0},
    ],
  },
};

const store = {
	 navigation: {library: null, patchId: new URL(location.href).searchParams.get('patch') || '', projectId: '', section: 'overview', screen: 'project', creating: '', loading: false},
	provenance: {key: '', data: null, pending: '', error: ''},
	launchId: new URL(location.href).searchParams.get('launch') || '', harness: {preview: null, selection: null, request: 0, starting: false, started: null},
  session: null, state: null, runId: '', liveEdit: false, events: [], gates: [], selected: null,
  gateDecisions: [], gateError: '', gateReview: null, gateDrafts: new Map(), gatePolling: false, gateSequence: 0,
  stream: null, activeCords: new Set(), drag: null, cordDraft: null, refreshTimer: null,
  view: {x: 0, y: 0, scale: 1}, pan: null, historyOpen: false,
  sources: {
    open: false, tab: 'work', mode: 'ready', query: '', selected: null, pendingItem: '',
    fixture: false, status: 'idle', error: '', request: 0,
    work: {projects: [], project: '', phases: [], phase: '', ready: [], ideas: [], searchKind: 'tickets', searchHits: []},
    memory: {snapshot: null, index: [], review: []},
  },
};

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.method && options.method !== 'GET') {
    headers.set('Content-Type', 'application/json');
    headers.set('X-Smith-Canvas', store.session.token);
    headers.set('X-Smith-Client', clientId);
  }
  const response = await fetch(scopedPath(path), {...options, headers});
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 409 && body.error?.code === 'topology_conflict' && body.refresh?.state) {
      store.state = body.refresh.state;
      renderAll();
      notice('the patch changed elsewhere. refreshed before applying your edit.', true);
    } else {
      notice(body.error?.message || `request failed (${response.status})`, true);
    }
    const error = new Error(body.error?.message || response.statusText);
    error.code = body.error?.code;
    throw error;
  }
  return body;
}

function scopedPath(path) {
  const url = new URL(path, location.origin);
  if (!url.pathname.startsWith('/api/library')) {
    if (url.pathname.startsWith('/api/harness/') && store.navigation.screen === 'tickets') url.searchParams.set('project', store.navigation.projectId);
    else if (store.navigation.patchId) url.searchParams.set('patch', store.navigation.patchId);
    else if (store.launchId) url.searchParams.set('launch', store.launchId);
  }
  return url.pathname + url.search;
}

function patchHref(item, run = '') {
  const url = new URL('/', location.origin);
  url.searchParams.set('patch', item.id);
  if (run) url.searchParams.set('run', run);
  return url.pathname + url.search;
}

function projectHref(project, section = 'overview') {
  return `/?project=${encodeURIComponent(project.id)}&section=${section}`;
}

function currentLibraryPatch() {
  return store.navigation.library?.projects.flatMap(project => project.patches || []).find(item => item.root === store.session?.root);
}

async function loadLibrary() {
  store.navigation.library = await api('/api/library');
  const params = new URL(location.href).searchParams;
  const item = currentLibraryPatch();
  store.navigation.projectId = params.get('project') || item?.project_id || store.navigation.library.projects[0]?.id || '';
  store.navigation.section = ['overview','patches','runs','tickets'].includes(params.get('section')) ? params.get('section') : 'overview';
  store.navigation.screen = screenFor(params);
  renderNavigation();
}

function renderNavigation() {
  const nav = store.navigation;
  if (!nav.library) return;
  const projects = nav.library.projects;
  const project = projects.find(item => item.id === nav.projectId);
  const item = currentLibraryPatch();
  $('#project-tree').innerHTML = projects.map(entry => `<section class="project-entry"><a class="project-link" href="${projectHref(entry)}" ${entry.id === nav.projectId ? 'aria-current="page"' : ''}>${esc(entry.name)}</a>${entry.id === nav.projectId ? `<div class="project-links">${['overview','patches','runs','tickets'].map(section => `<a href="${projectHref(entry, section)}" ${nav.screen !== 'patch' && nav.section === section ? 'aria-current="page"' : ''}>${section}</a>`).join('')}</div>` : ''}</section>`).join('');
  $('#breadcrumbs').innerHTML = nav.screen === 'patch' ? '' : '<a class="wordmark" href="/">smith</a>';
  $('#breadcrumbs').hidden = nav.screen === 'patch';
  $('#back-project').href = nav.screen === 'patch' && project ? projectHref(project) : '/';
  $('#back-project').setAttribute('aria-label', nav.screen === 'patch' && project ? `back to ${project.name}` : 'back to home');
  $('#back-project span').textContent = nav.screen === 'patch' && project ? project.name : 'home';
  $('#back-project').hidden = nav.screen === 'home';
  $('.location-bar .identity').hidden = nav.screen !== 'patch';
  $('#global-links').hidden = nav.screen === 'patch';
  $('#execution-kind').hidden = nav.screen !== 'patch';
  $('#execution-kind').textContent = !store.runId ? 'patch editor' : ['completed','stopped','failed'].includes(store.state?.status) ? 'recorded run' : 'live run';
  $('#view-modes').hidden = nav.screen !== 'patch';
  $('.transport').hidden = nav.screen !== 'patch';
  $('.workbench').hidden = !['patch','tickets'].includes(nav.screen);
  $('.workbench').classList.toggle('tickets-view', nav.screen === 'tickets');
  $('#project-overview').hidden = nav.screen !== 'project';
  $('#home-overview').hidden = !['home','settings','integrations'].includes(nav.screen);
  if (!$('#home-overview').hidden) renderHome();
  $('#edit-patch').setAttribute('aria-pressed', String(!store.runId));
  $('#watch-run').setAttribute('aria-pressed', String(Boolean(store.runId)));
  $('#watch-run').disabled = !store.session.runs.length;
  $('#watch-run').title = store.session.runs.length ? 'inspect a recorded execution' : 'no runs yet — build your patch, then start a run';
  if (item) {
    $('#patch-name').textContent = item.name;
    $('#patch-root').textContent = store.runId ? store.liveEdit ? 'editing live · changes affect this execution' : 'watching execution · edit patch changes the next run' : 'editing patch · saved changes apply to the next run';
  }
  if (nav.screen !== 'project') return;
  const target = $('#project-overview');
  if (!project) { target.innerHTML = '<h1>project unavailable</h1><p><a href="/">return home to choose or create a project.</a></p>'; return; }
  const patches = project.patches || [];
  const runs = patches.flatMap(patch => (patch.runs || []).map(run => ({...run, patch}))).sort((a,b) => b.run_id.localeCompare(a.run_id));
  const runRows = (entries) => entries.map(run => `<a class="library-row" href="${patchHref(run.patch,run.run_id)}${run.pending_gates ? '&gate=pending' : ''}"><span><strong>${esc(run.patch.name)}</strong><small>${esc(runLabel(run))}</small></span><span>${esc(run.error || (run.pending_gates ? `${run.pending_gates} waiting · review decision` : run.status))}</span></a>`).join('');
  target.innerHTML = `<header class="project-heading"><div><h1>${esc(project.name)}</h1><p>${esc(project.root)}</p></div><div class="project-actions"><button id="new-patch" class="key">+ blank patch</button><button id="patch-from-ticket">from ticket…</button></div></header>
    <nav class="project-tabs" aria-label="project sections">${['overview','patches','runs','tickets'].map(section=>`<a href="${projectHref(project,section)}" ${section===nav.section?'aria-current="page"':''}>${section}</a>`).join('')}</nav>
    <p class="project-explanation">${nav.section === 'tickets' ? 'tickets are shared work records. preparing one shows the exact harness before anything runs.' : 'patches are editable programs. runs are their recorded executions.'}</p>
    ${nav.error || project.error ? `<p role="alert">${esc(nav.error || project.error)}</p>` : ''}
    ${nav.section === 'overview' && runs.some(run => run.pending_gates) ? `<section><h2>waiting for you</h2>${runRows(runs.filter(run => run.pending_gates))}</section>` : ''}
    ${['overview','patches'].includes(nav.section) ? `<section><h2>patches</h2>${patches.map(patch => `<a class="library-row" href="${patchHref(patch)}"><span><strong>${esc(patch.name)}</strong><small>${patch.harness ? 'ticket harness' : 'editable patch'} · ${patch.runs.length} run${patch.runs.length === 1 ? '' : 's'}</small></span><span>${esc(patch.error || 'open patch')}</span></a>`).join('') || '<p class="library-empty">no patches yet. create a blank patch, add nodes and connect them — no run needed.</p>'}</section>` : ''}
    ${['overview','runs'].includes(nav.section) ? `<section><h2>${nav.section === 'overview' ? 'recent runs' : 'runs'}</h2>${runRows(nav.section === 'overview' ? runs.slice(0,8) : runs) || '<p class="library-empty">no runs yet. open a patch and choose start run when you’re ready.</p>'}</section>` : ''}
    ${nav.section === 'overview' ? `<section><h2>project connections</h2>${integrationRows()}</section>` : ''}`;
  $('#new-patch').onclick = () => openCreate('patch');
  $('#patch-from-ticket').onclick = openProjectTickets;
  $('#open-project-tickets')?.addEventListener('click', openProjectTickets);
}

function integrationRows() {
  return [['tickets_please','work stays on your board. smith follows the ticket.',store.session.work_source_available,store.session.work_app_url],['memory renderer','memory stays in its own home. smith brings the context.',store.session.memory_source_available,store.session.memory_app_url]].map(([name,copy,available,url])=>`<div class="integration-row"><div><h3>${name}</h3><p>${copy}</p><p>${available?'source configured':'source not configured'}</p></div>${url?`<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">open ${name}</a>`:'<span class="empty-copy">app link not configured</span>'}</div>`).join('');
}

function renderHome() {
  const target=$('#home-overview'),nav=store.navigation;
  if(nav.screen==='settings') {
    target.innerHTML=`<header class="project-heading"><div><h1>settings</h1><p>this desktop instance’s configuration.</p></div></header><div class="settings-copy"><h2>workspace</h2><pre>${esc(nav.library.creation_root)}</pre><h2>write access</h2><pre>${esc((store.session.writable_roots||[]).join('\n')||'no writable roots configured')}</pre><p>these values come from the running smith service. changing startup configuration is not available in this UI yet.</p><a href="/?screen=integrations">view integrations</a></div>`;return;
  }
  if(nav.screen==='integrations') {target.innerHTML=`<header class="project-heading"><div><h1>your connections</h1><p>existing tools, kept independent.</p></div></header>${integrationRows()}<p class="settings-copy">connection settings come from this instance’s startup configuration. a configured source is not a live health check; smith reports connection errors when you open it.</p>`;return;}
  target.innerHTML=`<header class="project-heading"><div><h1>your workshop.</h1><p>projects, patches, and the work moving through them.</p></div><button id="home-new-project" class="key">+ new project</button></header>${nav.error?`<p role="alert">${esc(nav.error)}</p>`:''}<section><h2>projects</h2><div class="project-cards">${nav.library.projects.map(project=>`<a class="project-card" href="${projectHref(project)}"><h2>${esc(project.name)}</h2><p>${project.patches.length} patch${project.patches.length===1?'':'es'} / ${project.patches.reduce((n,p)=>n+p.runs.length,0)} recorded runs</p><p>${esc(project.error||project.root)}</p><span class="project-open">open project</span></a>`).join('')||'<p class="empty-copy">no projects yet. create one to start shaping a patch.</p>'}</div></section><section><h2>your tools, connected</h2>${integrationRows()}</section><button id="home-refresh" class="quiet">refresh projects and runs</button>`;
  $('#home-new-project').onclick=()=>openCreate('project');$('#home-refresh').onclick=()=>loadLibrary().catch(error=>notice(error.message,true));
}

function runLabel(run) {
  const id = run.run_id || '';
  const match = id.match(/^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})/);
  return match ? `${match[1]}-${match[2]}-${match[3]} ${match[4]}:${match[5]}:${match[6]} UTC · ${id.split('-').pop()}` : id;
}

function openProjectTickets() {
  const project = store.navigation.library.projects.find(item => item.id === store.navigation.projectId);
  if (project) location.assign(projectHref(project,'tickets'));
}

function openCreate(kind) {
  store.navigation.creating = kind;
  $('#create-form').reset(); $('#create-error').hidden = true;
  $('#create-title').textContent = `new ${kind}`;
  $('#create-submit').textContent = `create ${kind}`;
  $('#create-description').textContent = kind === 'project' ? `a local project for patches. saved under ${store.navigation.library.creation_root}.` : 'a blank, editable patch. nothing executes until you start a run and send a signal.';
  $('#create-name').placeholder = kind === 'project' ? 'experiments' : 'review-loop';
  $('#create-dialog').showModal(); $('#create-name').focus();
}

async function createLibraryItem(event) {
  event.preventDefault();
  $('#create-submit').disabled = true; $('#create-error').hidden = true;
  try {
    const kind = store.navigation.creating;
    const body = {name:$('#create-name').value};
    if (kind === 'patch') body.project_id = store.navigation.projectId;
    const item = await api(`/api/library/${kind === 'project' ? 'projects' : 'patches'}`, {method:'POST',body:JSON.stringify(body)});
    location.assign(kind === 'project' ? projectHref(item) : patchHref(item));
  } catch (error) { $('#create-error').textContent = error.message; $('#create-error').hidden = false; }
  finally { $('#create-submit').disabled = false; }
}

async function prepareHarness(selection) {
  if (!selection || store.harness.starting) return;
  const request = ++store.harness.request;
  store.harness.selection = selection;
  store.harness.preview = null;
  store.harness.started = null;
  $('#harness-title').textContent = 'prepare run';
  $('#harness-preview').innerHTML = '<p class="empty-copy">reading the ticket and resolving selected memory…</p>';
  $('#harness-error').hidden = true;
  $('#retry-harness').hidden = true;
  $('#start-harness').disabled = true;
  $('#start-harness').textContent = 'start run';
  $('#reconnect-harness').hidden = true;
  if (!$('#harness-dialog').open) $('#harness-dialog').showModal();
  try {
    const preview = await api('/api/harness/preview', {method: 'POST', body: JSON.stringify(selection)});
    if (request !== store.harness.request) return;
    store.harness.preview = preview;
    $('#harness-title').textContent = preview.title;
    $('#harness-preview').innerHTML = harnessPreviewHTML(preview);
    $('#start-harness').disabled = false;
  } catch (error) {
    if (request !== store.harness.request) return;
    $('#harness-preview').innerHTML = '<p>the run has not been created.</p>';
    showHarnessError(error.message);
  }
}

function harnessPreviewHTML(preview) {
  const facts = (items) => `<dl class="fact-grid">${items.map(([name, value]) => `<dt>${esc(name)}</dt><dd>${esc(value)}</dd>`).join('')}</dl>`;
  return `<section>${facts([['project', preview.selection.project_slug], ['ticket', preview.selection.ticket_id], ['state', preview.column], ['workspace', preview.workspace]])}</section>
    <section><h3>model roles</h3><div class="harness-models">${preview.models.map((model) => `<div>${facts([['node', model.node_id], ['model', model.model], ['access', model.profile]])}</div>`).join('')}</div></section>
    <section><h3>ticket permissions</h3>${preview.grants.map((grant) => facts([['package', grant.package], ['access', grant.access === 'mutate' ? 'read, search, rate, comment, move and complete' : grant.access], ['project scope', grant.scope.project]])).join('')}<p class="empty-copy">the completion gate pauses before the ticket is closed.</p></section>
    <section><h3>selected memory</h3>${preview.context.map((item) => facts([['body', item.options?.body || ''], ['context selectors', (item.options?.context || []).join(', ') || 'none'], ['exact memories', (item.options?.memories || []).join(', ') || 'none']])).join('')}
    ${preview.artifacts.map((item) => `<details><summary>${esc(item.name)} · ${item.bytes} bytes</summary>${facts([['source', item.source], ['revision', item.revision || 'unversioned'], ['sha256', item.sha256]])}</details>`).join('')}</section>
    <section><h3>acceptance checks</h3>${preview.checks.map((check) => `<div class="harness-check"><strong>${esc(check.id)}</strong><code>${esc([check.executable, ...(check.args || [])].join(' '))}</code><span>${esc(check.timeout || preview.check_limits.timeout)} limit</span></div>`).join('')}</section>
    <section><h3>execution bounds</h3>${facts([['parallel nodes', preview.options.max_parallel], ['maximum hops', preview.options.max_hops], ['queue capacity', preview.options.default_queue.capacity], ['queue overflow', preview.options.default_queue.overflow]])}
    <details><summary>per-node resource and retry limits</summary>${preview.models.map((model) => `<h4>${esc(model.node_id)}</h4>${facts(Object.entries(model.limits))}${facts(Object.entries(model.attempts).map(([key,value]) => [key, typeof value === 'object' ? JSON.stringify(value) : value]))}`).join('')}<h4>check defaults</h4>${facts(Object.entries(preview.check_limits))}</details></section>
    <details><summary>bound revisions</summary>${facts([['template', preview.template], ['template revision', preview.template_revision], ['patch revision', preview.patch_revision], ['work revision', preview.work_revision], ['preview digest', preview.digest]])}</details>`;
}

function showHarnessError(message) {
  $('#harness-error').textContent = message;
  $('#harness-error').hidden = false;
  $('#retry-harness').hidden = false;
  $('#start-harness').disabled = true;
}

function closeHarness() {
  if (store.harness.starting) return;
  store.harness.request++;
  store.harness.preview = null;
  $('#harness-dialog').close();
}

async function startHarness() {
  const preview = store.harness.preview;
  if (!preview || store.harness.starting) return;
  store.harness.starting = true;
  $('#start-harness').disabled = true;
  $('#close-harness').disabled = true;
  $('#harness-error').hidden = true;
  try {
    store.harness.started = await api('/api/harness/start', {method: 'POST', body: JSON.stringify({digest: preview.digest})});
  } catch (error) {
    showHarnessError(error.message);
    if (error.code !== 'harness_stale') {
      // A lost response does not prove start failed. Retry the same digest.
      $('#retry-harness').hidden = true;
      $('#start-harness').textContent = 'retry same start';
      $('#start-harness').disabled = false;
    }
  } finally {
    store.harness.starting = false;
    $('#close-harness').disabled = false;
  }
  if (store.harness.started) await attachHarness();
}

async function attachHarness() {
  const started = store.harness.started;
  if (!started || store.harness.starting) return;
  store.harness.starting = true;
  $('#close-harness').disabled = true;
  $('#retry-harness').hidden = true;
  $('#reconnect-harness').hidden = true;
  $('#start-harness').disabled = true;
  try {
    const library = await api('/api/library');
    const launched = library.projects.flatMap(project => project.patches || []).find(item => item.root === started.root);
    if (!launched) throw new Error('the started run is not yet available in the project library; reconnect to try again');
    store.navigation.library = library;
    store.navigation.patchId = launched.id;
    store.navigation.projectId = launched.project_id;
    store.navigation.screen = 'patch';
    store.launchId = '';
    history.replaceState(null, '', patchHref(launched, started.run_id));
    store.session = await api('/api/session');
    $('#patch-name').textContent = store.harness.preview.title;
    $('#patch-root').textContent = store.harness.preview.workspace;
    store.view = {x: 0, y: 0, scale: 1};
    await selectRun(started.run_id);
    renderNavigation();
    $('#harness-dialog').close();
    notice('harness run started');
  } catch (error) {
    $('#harness-error').textContent = `run ${started.run_id} started; could not load its view: ${error.message}`;
    $('#harness-error').hidden = false;
    $('#reconnect-harness').hidden = false;
    if (!$('#harness-dialog').open) $('#harness-dialog').showModal();
  }
  finally {
    store.harness.starting = false;
    $('#close-harness').disabled = false;
  }
}

async function boot() {
  try { store.session = await api('/api/session'); }
  catch (error) {
    if (!store.navigation.patchId && !store.launchId) throw error;
    store.navigation.patchId = ''; store.launchId = '';
    store.navigation.error = `could not open that patch: ${error.message}. choose a project or another patch below.`;
    history.replaceState(null, '', '/');
    store.session = await api('/api/session');
  }
  $('#patch-name').textContent = store.session.harness_launch?.title || (store.launchId ? 'harness run' : store.session.root.split('/').filter(Boolean).pop() || 'patch');
  $('#patch-root').textContent = store.session.root;
  const requested = new URL(location.href).searchParams.get('run');
  const requestedRun = store.session.runs.find((run) => run.run_id === requested && !run.error);
  store.runId = requestedRun?.run_id || '';
  if (requested && !requestedRun) notice('that run is unavailable; showing the editable patch.', true);
  const sourceURL = new URL(location.href).searchParams;
  store.sources.fixture = sourceURL.get('fixture_sources') === '1';
  store.sources.open = store.sources.fixture || sourceURL.get('sources') === 'open';
  store.sources.tab = sourceURL.get('source') === 'memory' ? 'memory' : 'work';
  const sourceModes = store.sources.tab === 'work' ? ['ready', 'ideas', 'search'] : ['index', 'review', 'search'];
  const requestedSourceMode = sourceURL.get('source_mode');
  store.sources.mode = sourceModes.includes(requestedSourceMode) ? requestedSourceMode : sourceModes[0];
  store.sources.pendingItem = sourceURL.get('source_item') || '';
  if (store.sources.fixture && sourceURL.get('source_item')) {
    store.sources.selected = sourceItemByID(store.sources.tab, sourceURL.get('source_item'));
    store.sources.pendingItem = '';
  }
  bindStaticControls();
  await loadLibrary();
  if (store.navigation.screen === 'tickets') { store.sources.open = true; store.sources.tab = 'work'; }
  await selectRun(store.runId, true);
  renderSources();
  if (store.sources.open && !store.sources.fixture) await loadCurrentSource();
}

function finishBoot(error) {
  const app = $('#app');
  if (error) {
    // Keep the uninitialized route concealed, but make failure persistent and
    // actionable rather than relying on a toast that disappears after 3s.
    $('#page-loading').setAttribute('role', 'alert');
    $('#startup-message').textContent = `could not open smith: ${error.message}`;
    $('#startup-retry').hidden = false;
    $('#startup-retry').onclick = () => location.reload();
  } else {
    $('#page-loading').hidden = true;
    app.removeAttribute('data-startup');
  }
  app.setAttribute('aria-busy', 'false');
}

function bindStaticControls() {
  $('#toggle-navigator').onclick = () => { const open = $('#navigator').hidden; $('#navigator').hidden = !open; $('#toggle-navigator').setAttribute('aria-expanded', String(open)); $('.workspace').classList.toggle('nav-closed', !open); };
  $('#new-project').onclick = () => openCreate('project');
  $('#close-create').onclick = () => $('#create-dialog').close();
  $('#create-form').onsubmit = createLibraryItem;
  $('#refresh-library').onclick = () => loadLibrary().catch(error => notice(error.message,true));
  $('#edit-patch').onclick = () => { const item = currentLibraryPatch(); if (item) location.assign(patchHref(item)); };
  $('#watch-run').onclick = () => { const item = currentLibraryPatch(); if (item && store.session.runs.length) location.assign(patchHref(item,store.runId || store.session.runs[0].run_id)); };
  $('#live-edit').onclick = () => { store.liveEdit = !store.liveEdit; renderAll(); };
  $('#close-harness').addEventListener('click', closeHarness);
  $('#harness-dialog').addEventListener('cancel', (event) => { event.preventDefault(); closeHarness(); });
  $('#start-harness').addEventListener('click', startHarness);
  $('#reconnect-harness').addEventListener('click', attachHarness);
  $('#retry-harness').addEventListener('click', () => prepareHarness(store.harness.selection));
  $('#toggle-sources').addEventListener('click', () => setSourcesOpen(!store.sources.open));
  $('#close-sources').addEventListener('click', () => {
    if (store.navigation.screen === 'tickets') {
      const project = store.navigation.library.projects.find(item => item.id === store.navigation.projectId);
      if (project) location.assign(projectHref(project));
    } else setSourcesOpen(false);
  });
  document.querySelectorAll('[data-source-tab]').forEach((button) => button.addEventListener('click', () => selectSourceTab(button.dataset.sourceTab)));
  sourceOverlay.addEventListener('change', syncSourceInert);
  $('.source-tabs').addEventListener('keydown', (event) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const tab = event.key === 'ArrowRight' || event.key === 'End' ? 'memory' : 'work';
    selectSourceTab(tab);
    $(`[data-source-tab="${tab}"]`).focus();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && store.sources.open && !document.querySelector('dialog[open]')) $('#close-sources').click();
    else if (event.key === 'Escape' && (store.selected || store.historyOpen) && !document.querySelector('dialog[open]')) $('#clear-selection').click();
  });
  $('#run-select').addEventListener('change', (event) => { const item = currentLibraryPatch(); if (item) location.assign(patchHref(item,event.target.value)); });
  $('#new-run').addEventListener('click', startRun);
  $('#pause-run').addEventListener('click', () => control('pause'));
  $('#resume-run').addEventListener('click', () => control('resume'));
  $('#stop-run').addEventListener('click', () => control('stop'));
  $('#clear-selection').addEventListener('click', () => { const selected=store.selected;store.selected = null;store.historyOpen=false;renderAll();if(selected?.type==='node') document.querySelector(`.patch-node[data-node="${CSS.escape(selected.id)}"]`)?.focus();else $('#show-history').focus(); });
  $('#show-history').onclick=()=>{store.historyOpen=!store.historyOpen;renderInspector();};
  $('#fit-patch').onclick=fitPatch;
  for(const [id,factor] of [['zoom-in',1.2],['zoom-out',1/1.2]]) $("#"+id).onclick=()=>{const viewport=$('#patch-viewport'),r=viewport.getBoundingClientRect();store.view=zoomViewAt(store.view,r.left+r.width/2,r.top+r.height/2,r,store.view.scale*factor);renderView();};
  $('#add-node').addEventListener('click', () => $('#node-dialog').showModal());
  $('#close-node-dialog').addEventListener('click', () => $('#node-dialog').close());
  $('#node-form [name=kind]').addEventListener('change', showKindFields);
  $('#node-form').addEventListener('submit', placeNode);
  $('#patch-viewport').addEventListener('pointerdown', beginPan);
  $('#patch-viewport').addEventListener('wheel', zoomCanvas, {passive: false});
  $('#patch-viewport').addEventListener('auxclick', preventCanvasAuxClick);
  $('#patch-viewport').addEventListener('lostpointercapture', endPan);
  document.addEventListener('pointermove', pointerMove);
  document.addEventListener('pointerup', pointerUp);
  document.addEventListener('pointercancel', endPan);
}

async function startRun() {
  try {
    const started = await api('/api/runs', {method: 'POST', body: '{}'});
    store.session.runs.unshift({
      run_id: started.run_id, status: started.state.status,
      topology_revision: started.state.topology_revision, last_sequence: started.state.last_sequence,
    });
    await selectRun(started.run_id);
    const item = currentLibraryPatch();
    if (item) location.assign(patchHref(item, started.run_id));
    notice('new patch run started');
  } catch (_) {}
}

async function selectRun(runId, preserveSources = false) {
  const savedView=readCanvasView(runId);
  store.stream?.close();
  store.stream = null;
  store.runId = runId;
  store.liveEdit = false;
  store.events = [];
  store.gates = [];
  store.gateDecisions = [];
  store.gateSequence = 0;
  store.gateError = '';
  closeGateReview();
  store.selected = null;
  if (runId && store.sources.open && !store.sources.fixture && !preserveSources) setSourcesOpen(false, false);
  if (!runId) {
    setRunURL('');
    store.session = await api('/api/session');
    store.state = {
      status: 'offline', topology_revision: store.session.patch.topology_revision,
      patch_revision: store.session.patch.revision, last_sequence: 0, topology: store.session.patch,
      queues: {}, active: [], revisions: [],
    };
    if(savedView){store.view=savedView.view;store.selected=savedView.selected;}
    renderAll();
    return;
  }
  const encoded = encodeURIComponent(runId);
  store.state = await api(`/api/state?run=${encoded}`);
  let cursor = 0;
  do {
    const page = await api(`/api/events?run=${encoded}&after=${cursor}&limit=1000`);
    store.events.push(...(page.events || []));
    cursor = page.next_cursor;
    if (!page.has_more) break;
  } while (true);
  // Keep attribution/history over the complete journal already read above.
  // The raw tape itself remains a bounded, opt-in view.
  await refreshGates();
  setRunURL(runId);
  if(savedView){store.view=savedView.view;store.selected=savedView.selected;}
  renderAll();
  if (compactHarness()&&!savedView) fitPatch();
  openStream(store.state.last_sequence);
  if (new URL(location.href).searchParams.get('gate') === 'pending' && store.gates.length) openGateReview(store.gates[0]);
}

function setSourcesOpen(open, focus = true) {
  store.sources.open = open;
  $('#source-rack').hidden = !open;
  $('#toggle-sources').setAttribute('aria-expanded', String(open));
  document.querySelector('.workbench').classList.toggle('sources-open', open);
  syncSourceInert();
  syncSourceURL();
  if (open) {
    renderSources();
    if (!store.sources.fixture) void loadCurrentSource();
    if (focus) $(`[data-source-tab="${store.sources.tab}"]`).focus();
  } else if (focus) {
    $('#toggle-sources').focus();
  }
}

function selectSourceTab(tab) {
  store.sources.tab = tab;
  store.sources.mode = tab === 'work' ? 'ready' : 'index';
  store.sources.query = '';
  store.sources.selected = null;
  store.sources.pendingItem = '';
  syncSourceURL();
  renderSources();
  if (!store.sources.fixture) void loadCurrentSource();
}

function syncSourceURL() {
  const next = new URL(location.href);
  if (store.sources.open) next.searchParams.set('sources', 'open'); else next.searchParams.delete('sources');
  next.searchParams.set('source', store.sources.tab);
  next.searchParams.set('source_mode', store.sources.mode);
  const selected = sourceSelectionKey(store.sources.selected) || store.sources.pendingItem;
  if (selected) next.searchParams.set('source_item', selected); else next.searchParams.delete('source_item');
  history.replaceState(null, '', next);
}

function sourceSelectionKey(selected) {
  if (!selected) return '';
  if (selected.id) return selected.id;
  if (selected.ticket) return selected.ticket.id;
  if (selected.memory) return selected.memory.slug;
  if (selected.candidate) return selected.candidate.slug;
  return '';
}

function renderSources() {
  const rack = $('#source-rack');
  rack.hidden = !store.sources.open;
  $('#toggle-sources').setAttribute('aria-expanded', String(store.sources.open));
  document.querySelector('.workbench').classList.toggle('sources-open', store.sources.open);
  syncSourceInert();
  document.querySelectorAll('[data-source-tab]').forEach((button) => {
    const selected = button.dataset.sourceTab === store.sources.tab;
    button.setAttribute('aria-selected', String(selected));
    button.tabIndex = selected ? 0 : -1;
  });
  $('#source-panel').setAttribute('aria-labelledby', `source-tab-${store.sources.tab}`);
  const appName = store.sources.tab === 'work' ? 'tickets_please' : 'memory renderer';
  const appURL = store.sources.tab === 'work' ? store.session.work_app_url : store.session.memory_app_url;
  const appLink = $('#open-source-app');
  appLink.textContent = `open in ${appName} ↗`;
  appLink.hidden = !appURL;
  if (appURL) appLink.href = appURL;
  else appLink.removeAttribute('href');
  $('#source-app-unavailable').hidden = Boolean(appURL);
  $('#source-app-unavailable').textContent = `${appName} browser link not configured`;
  if (!store.sources.open) return;
  const panel = $('#source-panel');
  if (!sourceAvailable(store.sources.tab)) {
    panel.innerHTML = sourceEmptyHTML(`${store.sources.tab} source is not configured`, store.sources.tab === 'work'
      ? 'set the tickets_please endpoint and bootstrap project, then restart smith.'
      : 'set the memory renderer endpoint, then restart smith.');
    return;
  }
  if (store.sources.status === 'loading') {
    panel.innerHTML = sourceStatusHTML('loading source', store.sources.tab === 'work' ? 'reading tickets_please without changing it.' : 'reading the current memory snapshot.');
    return;
  }
  if (store.sources.status === 'error') {
    panel.innerHTML = sourceErrorHTML(store.sources.error);
    panel.querySelector('[data-source-retry]')?.addEventListener('click', () => void loadCurrentSource(`[data-source-mode="${store.sources.mode}"]`));
    return;
  }
  if (store.sources.selected) {
    panel.innerHTML = store.sources.fixture ? fixtureSourceDetailHTML(store.sources.selected) : liveSourceDetailHTML(store.sources.selected);
    panel.querySelector('[data-source-back]').addEventListener('click', () => {
      store.sources.selected = null;
      store.sources.pendingItem = '';
      syncSourceURL();
      renderSources();
    });
    panel.querySelector('[data-prepare-harness]')?.addEventListener('click', (event) => prepareHarness({project_slug: store.session.harness_project, ticket_id: event.currentTarget.dataset.prepareHarness}));
    return;
  }
  if (store.sources.fixture) {
    panel.innerHTML = store.sources.tab === 'work' ? fixtureWorkHTML() : fixtureMemoryHTML();
  } else {
    panel.innerHTML = store.sources.tab === 'work' ? liveWorkHTML() : liveMemoryHTML();
  }
  bindSourcePanel();
}

function sourceAvailable(tab) {
  return store.sources.fixture || (tab === 'work' ? store.session.work_source_available : store.session.memory_source_available);
}

function syncSourceInert() {
  const obscured = store.sources.open && sourceOverlay.matches;
  $('#patch-viewport').inert = obscured;
  $('.scope').inert = obscured;
}

async function loadCurrentSource(focusSelector = '') {
  if (!store.sources.open || store.sources.fixture || !sourceAvailable(store.sources.tab)) return;
  const request = ++store.sources.request;
  store.sources.status = 'loading';
  store.sources.error = '';
  renderSources();
  try {
    if (store.sources.tab === 'work') await loadWorkSource();
    else await loadMemorySource();
    if (request !== store.sources.request) return;
    store.sources.status = 'ready';
    renderSources();
    if (store.sources.pendingItem) {
      const item = store.sources.pendingItem;
      store.sources.pendingItem = '';
      await loadSourceDetail(item);
      return;
    }
    if (focusSelector) focusSource(focusSelector);
  } catch (error) {
    if (request !== store.sources.request) return;
    store.sources.status = 'error';
    store.sources.error = error.message || String(error);
    renderSources();
    focusSource('[data-source-retry]');
  }
}

async function loadWorkSource() {
  const work = store.sources.work;
  if (!work.projects.length) {
    const result = await api('/api/work/projects');
    work.projects = result.projects || [];
    const patchName = store.session.root.split('/').filter(Boolean).pop();
    const preferredProject = work.project || store.session.harness_project || patchName;
    work.project = work.projects.find((project) => project.slug === preferredProject)?.slug || work.projects[0]?.slug || '';
  }
  if (!work.project) return;
  if (!work.phases.length) {
    const result = await api(`/api/work/phases?project=${encodeURIComponent(work.project)}`);
    work.phases = result.phases || [];
    const active = [...work.phases].filter((phase) => phase.active_ticket_count > 0).sort((a, b) => b.number - a.number);
    work.phase = (active[0] || [...work.phases].sort((a, b) => b.number - a.number)[0])?.slug || '';
  }
  if (store.sources.mode === 'ideas') {
    const page = await api(`/api/work/ideas?project=${encodeURIComponent(work.project)}&limit=100`);
    work.ideas = page.tickets || [];
  } else if (store.sources.mode === 'search') {
    if (!store.sources.query.trim()) {
      work.searchHits = [];
      return;
    }
    const params = new URLSearchParams({project: work.project, kind: work.searchKind, q: store.sources.query, include_ideas: 'true', limit: '50'});
    const page = await api(`/api/work/search?${params}`);
    work.searchHits = page.hits || [];
  } else {
    const params = new URLSearchParams({project: work.project, ready: 'true', limit: '100'});
    if (work.phase) params.set('phase', work.phase);
    const page = await api(`/api/work/tickets?${params}`);
    work.ready = page.tickets || [];
  }
}

async function loadMemorySource() {
  const memory = store.sources.memory;
  if (!memory.snapshot) memory.snapshot = await api('/api/memory/snapshot');
  if (store.sources.mode === 'review') {
    const page = await api('/api/memory/review?shelf=waiting&limit=100');
    memory.review = page.candidates || [];
  } else {
    const params = new URLSearchParams({limit: '100'});
    if (store.sources.mode === 'search' && store.sources.query.trim()) params.set('q', store.sources.query);
    const page = await api(`/api/memory/memories?${params}`);
    memory.index = page.memories || [];
  }
}

async function loadSourceDetail(id) {
  if (store.sources.fixture) {
    store.sources.selected = sourceItemByID(store.sources.tab, id);
    syncSourceURL();
    renderSources();
    focusSource('[data-source-back]');
    return;
  }
  const request = ++store.sources.request;
  store.sources.pendingItem = id;
  store.sources.status = 'loading';
  syncSourceURL();
  renderSources();
  try {
    let selected;
    if (store.sources.tab === 'work') {
      const project = store.sources.work.project;
      const detail = await api(`/api/work/ticket?project=${encodeURIComponent(project)}&ticket=${encodeURIComponent(id)}`);
      selected = {sourceKind: 'work-live', ...detail};
    } else {
      const candidate = store.sources.memory.review.find((item) => item.slug === id);
      if (candidate) selected = {sourceKind: 'memory-review', candidate};
      else {
        const memory = await api(`/api/memory/memory?slug=${encodeURIComponent(id)}`);
        selected = {sourceKind: 'memory-live', memory};
      }
    }
    if (request !== store.sources.request) return;
    store.sources.selected = selected;
    store.sources.pendingItem = '';
    store.sources.status = 'ready';
    syncSourceURL();
    renderSources();
    focusSource('[data-source-back]');
  } catch (error) {
    if (request !== store.sources.request) return;
    store.sources.status = 'error';
    store.sources.error = error.message || String(error);
    renderSources();
    focusSource('[data-source-retry]');
  }
}

function liveWorkHTML() {
  const work = store.sources.work;
  const project = work.projects.find((item) => item.slug === work.project);
  const phase = work.phases.find((item) => item.slug === work.phase);
  let rows = work.ready.map(workTicketSlipHTML).join('');
  if (store.sources.mode === 'ideas') rows = work.ideas.map(workTicketSlipHTML).join('');
  if (store.sources.mode === 'search') rows = work.searchHits.map(workSearchSlipHTML).join('');
  return `${workContextHTML(project, phase)}
    <div class="source-modes" role="group" aria-label="work view">
      ${sourceModeButton('ready', 'ready', work.ready.length)}${sourceModeButton('ideas', 'ideas', work.ideas.length)}${sourceModeButton('search', 'search')}
    </div>
    ${store.sources.mode === 'search' ? workSearchHTML() : ''}
    <div class="source-stack">${rows || sourceEmptyHTML(store.sources.mode === 'search' ? 'no work matches that phrase' : 'nothing is waiting here', store.sources.mode === 'search' ? 'change the phrase or search another part of the record.' : 'choose another phase or source view.')}</div>`;
}

function workContextHTML(project, phase) {
  const work = store.sources.work;
  const projectOptions = work.projects.map((item) => `<option value="${esc(item.slug)}" ${item.slug === work.project ? 'selected' : ''}>${esc(item.name)}</option>`).join('');
  const phaseOptions = `<option value="">all active work</option>` + work.phases.map((item) => `<option value="${esc(item.slug)}" ${item.slug === work.phase ? 'selected' : ''}>${item.number} · ${esc(item.name)}</option>`).join('');
  const context = [project?.description, project?.summary, phase?.description, phase?.summary].filter(Boolean);
  return `<div class="source-context source-context-controls">
      <label><span>project</span><select id="work-project">${projectOptions}</select></label>
      <label><span>phase</span><select id="work-phase">${phaseOptions}</select></label>
    </div>
    ${context.length ? `<details class="source-context-notes"><summary>project and phase context</summary>${context.map((text) => `<div class="record-copy">${esc(text)}</div>`).join('')}</details>` : ''}`;
}

function workTicketSlipHTML(ticket) {
  return `<button class="source-slip work-slip" data-source-id="${esc(ticket.id)}">
    <span class="slip-rail"><b>${esc(ticketReference(ticket))}</b><i>w${ticket.wave}</i></span>
    <span class="slip-copy"><small>${esc(ticket.column.replaceAll('_', ' '))}</small><strong>${esc(ticket.title)}</strong><span>${esc(excerpt(ticket.body || 'open the complete ticket record.'))}</span></span>
  </button>`;
}

function workSearchHTML() {
  const work = store.sources.work;
  return `<div class="source-search-kinds" role="group" aria-label="search within work">
      ${searchKindButton('tickets', 'tickets')}${searchKindButton('learnings', 'learnings')}${searchKindButton('comments', 'comments')}
    </div>${sourceSearchHTML(`search ${work.searchKind}`)}`;
}

function searchKindButton(kind, label) {
  return `<button data-search-kind="${kind}" aria-pressed="${store.sources.work.searchKind === kind}">${label}</button>`;
}

function workSearchSlipHTML(hit) {
  const ticket = hit.ticket;
  const title = hit.ticket_title || ticket?.title || 'ticket record';
  const reference = ticket ? ticketReference(ticket) : `@${shortID(hit.ticket_id)}`;
  const text = hit.text || ticket?.body || '';
  return `<button class="source-slip work-slip" data-source-id="${esc(hit.ticket_id || ticket?.id)}">
    <span class="slip-rail"><b>${esc(reference)}</b><i>${esc(hit.kind)}</i></span>
    <span class="slip-copy"><small>${esc(hit.kind)}</small><strong>${esc(title)}</strong><span>${esc(excerpt(text))}</span></span>
  </button>`;
}

function liveMemoryHTML() {
  const memory = store.sources.memory;
  const snapshot = memory.snapshot || {};
  const rows = store.sources.mode === 'review' ? memory.review.map(memoryReviewSlipHTML) : memory.index.map(memoryIndexSlipHTML);
  return `<div class="source-context"><span>snapshot</span><strong>${esc(short(snapshot.snapshot) || 'current')}</strong><small>${snapshot.total || 0} memories · ${snapshot.inbox_waiting || 0} waiting</small></div>
    <div class="source-modes" role="group" aria-label="memory view">
      ${sourceModeButton('index', 'index', memory.index.length)}${sourceModeButton('review', 'review', snapshot.inbox_waiting || memory.review.length)}${sourceModeButton('search', 'search')}
    </div>
    ${store.sources.mode === 'search' ? sourceSearchHTML('search the memory index') : ''}
    <div class="source-stack">${rows.join('') || sourceEmptyHTML(store.sources.mode === 'search' ? 'no memory matches that phrase' : store.sources.mode === 'review' ? 'the accession queue is empty' : 'the memory index is empty', store.sources.mode === 'search' ? 'try another phrase or return to the index.' : 'refresh the source or choose another view.')}</div>`;
}

function memoryIndexSlipHTML(memory) {
  return `<button class="source-slip memory-slip" data-source-id="${esc(memory.slug)}">
    <span class="accession">${memory.provenance?.revisions?.length || '—'}</span>
    <span class="slip-copy"><small>${esc(memory.type)} · ${memory.indexed ? 'indexed' : 'uncatalogued'}</small><strong>${esc(memory.title)}</strong><span>${esc(memory.hook || memory.description || '')}</span><code>${esc(memory.slug)}</code></span>
  </button>`;
}

function memoryReviewSlipHTML(candidate) {
  return `<button class="source-slip memory-slip" data-source-id="${esc(candidate.slug)}">
    <span class="accession">new</span>
    <span class="slip-copy"><small>${esc(candidate.type)} · waiting</small><strong>${esc(candidate.title)}</strong><span>${esc(candidate.description || excerpt(candidate.body))}</span><code>${esc(candidate.slug)}</code></span>
  </button>`;
}

function sourceModeButton(mode, label, count = '') {
  return `<button data-source-mode="${mode}" aria-pressed="${store.sources.mode === mode}">${label}${count === '' ? '' : ` <span>${count}</span>`}</button>`;
}

function sourceSearchHTML(placeholder) {
  return `<form class="source-search"><label><span class="sr-only">${esc(placeholder)}</span><input name="query" value="${esc(store.sources.query)}" placeholder="${esc(placeholder)}"></label><button>find</button></form>`;
}

function sourceEmptyHTML(title, next) {
  return `<div class="source-empty"><strong>${esc(title)}</strong><p>${esc(next)}</p></div>`;
}

function sourceStatusHTML(title, detail) {
  return `<div class="source-status" role="status"><span class="source-spinner" aria-hidden="true"></span><strong>${esc(title)}</strong><p>${esc(detail)}</p></div>`;
}

function sourceErrorHTML(message) {
  return `<div class="source-empty source-error" role="alert"><strong>source could not be read</strong><p>${esc(message)}</p><button data-source-retry>try again</button></div>`;
}

function bindSourcePanel() {
  document.querySelectorAll('[data-source-mode]').forEach((button) => button.addEventListener('click', () => {
    store.sources.mode = button.dataset.sourceMode;
    store.sources.query = '';
    store.sources.selected = null;
    store.sources.pendingItem = '';
    syncSourceURL();
    const selector = `[data-source-mode="${store.sources.mode}"]`;
    if (store.sources.fixture) { renderSources(); focusSource(selector); } else void loadCurrentSource(selector);
  }));
  document.querySelectorAll('[data-search-kind]').forEach((button) => button.addEventListener('click', () => {
    store.sources.work.searchKind = button.dataset.searchKind;
    store.sources.work.searchHits = [];
    store.sources.query = '';
    renderSources();
    focusSource(`[data-search-kind="${store.sources.work.searchKind}"]`);
  }));
  document.querySelector('.source-search')?.addEventListener('submit', (event) => {
    event.preventDefault();
    store.sources.query = new FormData(event.currentTarget).get('query') || '';
    if (store.sources.fixture) { renderSources(); focusSource('.source-search input'); } else void loadCurrentSource('.source-search input');
  });
  $('#work-project')?.addEventListener('change', (event) => {
    store.sources.work.project = event.target.value;
    store.sources.work.phases = [];
    store.sources.work.phase = '';
    void loadCurrentSource('#work-project');
  });
  $('#work-phase')?.addEventListener('change', (event) => {
    store.sources.work.phase = event.target.value;
    void loadCurrentSource('#work-phase');
  });
  document.querySelectorAll('[data-source-id]').forEach((button) => button.addEventListener('click', () => void loadSourceDetail(button.dataset.sourceId)));
}

function sourceItemByID(tab, id) {
  const rows = tab === 'work'
    ? [...sourceFixtures.work.ready, ...sourceFixtures.work.ideas]
    : [...sourceFixtures.memory.index, ...sourceFixtures.memory.review];
  const item = rows.find((candidate) => candidate.id === id);
  return item ? {...item, sourceKind: tab} : null;
}

function fixtureWorkHTML() {
  const data = sourceFixtures.work;
  const mode = store.sources.mode === 'ideas' ? 'ideas' : store.sources.mode === 'search' ? 'search' : 'ready';
  const rows = mode === 'ideas' ? data.ideas : mode === 'search' ? [...data.ready, ...data.ideas] : data.ready;
  const query = store.sources.query.trim().toLowerCase();
  const filtered = query ? rows.filter((item) => [item.title, item.note].join(' ').toLowerCase().includes(query)) : rows;
  return `<div class="source-context"><span>project</span><strong>${esc(data.project.name)}</strong><small>phase ${data.phase.number} · ${esc(data.phase.name)}</small></div>
    <div class="source-modes" role="group" aria-label="work view">${sourceModeButton('ready', 'ready', data.ready.length)}${sourceModeButton('ideas', 'ideas', data.ideas.length)}${sourceModeButton('search', 'search')}</div>
    ${mode === 'search' ? sourceSearchHTML('search tickets and learnings') : ''}
    <div class="source-stack">${filtered.length ? filtered.map(fixtureWorkSlipHTML).join('') : sourceEmptyHTML('no work matches that phrase', 'change the filter or pick another source view.')}</div>`;
}

function fixtureWorkSlipHTML(item) {
  return `<button class="source-slip work-slip" data-source-id="${esc(item.id)}"><span class="slip-rail"><b>#${item.number}</b><i>w${item.wave}</i></span><span class="slip-copy"><small>${esc(item.column.replaceAll('_', ' '))}</small><strong>${esc(item.title)}</strong><span>${esc(item.note)}</span></span></button>`;
}

function fixtureMemoryHTML() {
  const data = sourceFixtures.memory;
  const mode = store.sources.mode === 'review' ? 'review' : store.sources.mode === 'search' ? 'search' : 'index';
  const rows = mode === 'review' ? data.review : mode === 'search' ? [...data.index, ...data.review] : data.index;
  const query = store.sources.query.trim().toLowerCase();
  const filtered = query ? rows.filter((item) => [item.title, item.hook, item.slug].join(' ').toLowerCase().includes(query)) : rows;
  return `<div class="source-context"><span>snapshot</span><strong>${esc(data.snapshot)}</strong><small>${data.total} memories · ${data.waiting} waiting</small></div>
    <div class="source-modes" role="group" aria-label="memory view">${sourceModeButton('index', 'index', data.index.length)}${sourceModeButton('review', 'review', data.waiting)}${sourceModeButton('search', 'search')}</div>
    ${mode === 'search' ? sourceSearchHTML('search the memory index') : ''}
    <div class="source-stack">${filtered.length ? filtered.map(fixtureMemorySlipHTML).join('') : sourceEmptyHTML('no memory matches that phrase', 'try another phrase or return to the index.')}</div>`;
}

function fixtureMemorySlipHTML(item) {
  return `<button class="source-slip memory-slip" data-source-id="${esc(item.id)}"><span class="accession">${esc(item.accession)}</span><span class="slip-copy"><small>${esc(item.type)} · ${esc(item.stamp)}</small><strong>${esc(item.title)}</strong><span>${esc(item.hook)}</span><code>${esc(item.slug)}</code></span></button>`;
}

function liveSourceDetailHTML(selected) {
  if (selected.sourceKind === 'work-live') return workDetailHTML(selected);
  if (selected.sourceKind === 'memory-review') return memoryReviewDetailHTML(selected.candidate);
  return memoryDetailHTML(selected.memory);
}

function fixtureSourceDetailHTML(item) {
  if (item.sourceKind === 'work') {
    return `<div class="source-detail"><button class="quiet source-back" data-source-back>← work</button><span class="detail-number">ticket #${item.number} · wave ${item.wave}</span><h3>${esc(item.title)}</h3><p>${esc(item.note)}</p><dl class="fact-grid"><dt>state</dt><dd>${esc(item.column.replaceAll('_', ' '))}</dd><dt>project</dt><dd>smith</dd><dt>phase</dt><dd>one instrument</dd></dl><div class="source-readonly">inspection only · no patch started</div></div>`;
  }
  return `<div class="source-detail"><button class="quiet source-back" data-source-back>← memory</button><span class="detail-number">accession ${esc(item.accession)} · ${esc(item.stamp)}</span><h3>${esc(item.title)}</h3><p>${esc(item.hook)}</p><dl class="fact-grid"><dt>slug</dt><dd>${esc(item.slug)}</dd><dt>type</dt><dd>${esc(item.type)}</dd><dt>revisions</dt><dd>${item.revisions}</dd><dt>snapshot</dt><dd>${sourceFixtures.memory.snapshot}</dd></dl><div class="source-readonly">read from the memory renderer · corpus unchanged</div></div>`;
}

function workDetailHTML(detail) {
  const ticket = detail.ticket;
  const work = store.sources.work;
  const project = work.projects.find((item) => item.id === ticket.project_id);
  const phase = work.phases.find((item) => item.id === ticket.phase_id);
  const completion = [
    recordSection('work summary', ticket.work_summary),
    recordSection('testing evidence', ticket.testing_evidence),
    recordSection('learnings', ticket.learnings, 'learning-record'),
  ].join('');
  const comments = (detail.comments || []).map(commentHTML).join('');
  return `<article class="source-detail source-record">
    <button class="quiet source-back" data-source-back>← work</button>
    <span class="detail-number">ticket ${esc(ticketReference(ticket))} · ${esc(ticket.column.replaceAll('_', ' '))}</span>
    <h3>${esc(ticket.title)}</h3>
    ${project?.slug === store.session.harness_project && ticket.kind !== 'idea' && !ticket.archived && ticket.column !== 'done' && !ticket.blocked_by?.length ? `<button class="key" data-prepare-harness="${esc(ticket.id)}">prepare run</button>` : ''}
    <dl class="fact-grid"><dt>id</dt><dd>${esc(ticket.id)}</dd><dt>project</dt><dd>${esc(project?.name || ticket.project_id)}</dd><dt>phase</dt><dd>${esc(phase ? `${phase.number} · ${phase.name}` : ticket.phase_id || 'unphased')}</dd><dt>kind</dt><dd>${esc(ticket.kind)}</dd><dt>state</dt><dd>${esc(ticket.column.replaceAll('_', ' '))}</dd><dt>wave</dt><dd>${ticket.wave}</dd><dt>archived</dt><dd>${ticket.archived ? `yes · ${esc(formatDate(ticket.archived_at))}` : 'no'}</dd><dt>created by</dt><dd>${esc(ticket.created_by?.name || 'unknown')} · ${esc(formatDate(ticket.created_at))}</dd><dt>updated</dt><dd>${esc(formatDate(ticket.updated_at))}</dd>${ticket.completed_at ? `<dt>completed by</dt><dd>${esc(ticket.completed_by?.name || 'unknown')} · ${esc(formatDate(ticket.completed_at))}</dd>` : ''}</dl>
    ${recordSection('ticket body', ticket.body)}
    ${recordListSection('depends on', ticket.depends_on)}
    ${recordListSection('currently blocked by', ticket.blocked_by)}
    ${recordListSection('parallelizable with', ticket.parallelizable_with)}
    ${completion ? `<section class="completion-record"><h4>completion</h4>${completion}</section>` : ''}
    <section class="comment-record"><h4>comments and audit trail <span>${detail.comments?.length || 0}</span></h4>${comments || '<p class="empty-copy">no comments recorded.</p>'}</section>
    <div class="source-readonly">complete record from tickets_please · inspection only</div>
  </article>`;
}

function commentHTML(comment) {
  const movement = comment.from_column || comment.to_column ? `<span>${esc(comment.from_column || '—')} → ${esc(comment.to_column || '—')}</span>` : '';
  return `<article class="ticket-comment"><header><strong>${esc(comment.author?.name || 'system')}</strong><time>${esc(formatDate(comment.created_at))}</time></header><div class="comment-kind">${esc(comment.kind)}${movement}</div><div class="record-copy">${esc(comment.body)}</div></article>`;
}

function memoryDetailHTML(memory) {
  const revisions = memory.provenance?.revisions || [];
  return `<article class="source-detail source-record">
    <button class="quiet source-back" data-source-back>← memory</button><span class="detail-number">${esc(memory.type)} · ${memory.indexed ? 'indexed' : 'uncatalogued'}</span><h3>${esc(memory.title)}</h3>
    <dl class="fact-grid"><dt>slug</dt><dd>${esc(memory.slug)}</dd><dt>bytes</dt><dd>${memory.bytes}</dd><dt>path</dt><dd>${esc(memory.path)}</dd><dt>indexed</dt><dd>${memory.indexed ? 'yes' : 'no'}</dd><dt>catalogued</dt><dd>${memory.catalogued ? 'yes' : 'no'}</dd><dt>hub</dt><dd>${esc(memory.hub || '—')}</dd><dt>session</dt><dd>${esc(memory.session_id || '—')}</dd><dt>self reported</dt><dd>${esc(memory.self_reported || '—')}</dd><dt>revisions</dt><dd>${revisions.length}</dd><dt>provenance</dt><dd>${memory.provenance?.available ? 'available' : 'unavailable'}</dd><dt>created</dt><dd>${esc(formatDate(memory.provenance?.created))}</dd><dt>updated</dt><dd>${esc(formatDate(memory.provenance?.updated))}</dd></dl>
    ${recordSection('description', memory.description)}${recordSection('hook', memory.hook)}${recordSection('memory body', memory.body)}
    ${recordListSection('links', memory.links)}${recordListSection('backlinks', memory.backlinks)}${recordListSection('open threads', memory.open_threads)}
    ${recordStructuredSection('defects', memory.defects)}${recordStructuredSection('audit findings', memory.audit_findings)}
    ${revisions.length ? `<section class="record-section"><h4>revision history</h4>${revisions.map(revisionHTML).join('')}</section>` : ''}
    ${recordSection('provenance note', memory.provenance?.note)}
    <div class="source-readonly">complete record from the memory renderer · corpus unchanged</div>
  </article>`;
}

function memoryReviewDetailHTML(candidate) {
  const provenance = candidate.provenance || {};
  return `<article class="source-detail source-record">
    <button class="quiet source-back" data-source-back>← review queue</button><span class="detail-number">accession · waiting</span><h3>${esc(candidate.title)}</h3>
    <dl class="fact-grid"><dt>slug</dt><dd>${esc(candidate.slug)}</dd><dt>type</dt><dd>${esc(candidate.type)}</dd><dt>queue</dt><dd>${esc(candidate.queue)}</dd><dt>file</dt><dd>${esc(candidate.file)}</dd><dt>path</dt><dd>${esc(candidate.path)}</dd><dt>identity</dt><dd>${esc(provenance.identity)}</dd><dt>thread</dt><dd>${esc(provenance.thread_key)}</dd><dt>first seen</dt><dd>${esc(provenance.first_seen || '—')}</dd><dt>last seen</dt><dd>${esc(provenance.last_seen || '—')}</dd><dt>transcript</dt><dd>${esc(provenance.transcript_sha || '—')}</dd><dt>shadows</dt><dd>${candidate.shadows ? 'yes' : 'no'}</dd><dt>modified</dt><dd>${esc(formatDate(candidate.modified_at))}</dd></dl>
    ${recordSection('description', candidate.description)}${recordSection('candidate body', candidate.body)}${recordSection('source excerpt', provenance.body)}
    ${recordListSection('speakers', provenance.speakers)}${recordListSection('defects', candidate.defects)}
    <div class="source-readonly">complete accession record from the memory renderer · no review action granted</div>
  </article>`;
}

function recordSection(title, text, className = '') {
  if (!text) return '';
  return `<section class="record-section ${className}"><h4>${esc(title)}</h4><div class="record-copy">${esc(text)}</div></section>`;
}

function recordListSection(title, values) {
  if (!values?.length) return '';
  return recordSection(title, values.join('\n'));
}

function recordStructuredSection(title, values) {
  if (!values?.length) return '';
  return recordSection(title, JSON.stringify(values, null, 2));
}

function revisionHTML(item) {
  return `<article class="revision-row"><header><code>${esc(item.short || shortID(item.sha))}</code><time>${esc(formatDate(item.when))}</time></header><strong>${esc(item.subject || 'untitled revision')}</strong><dl class="fact-grid"><dt>sha</dt><dd>${esc(item.sha)}</dd><dt>change</dt><dd>+${item.added} −${item.removed}</dd>${item.renamed ? `<dt>renamed</dt><dd>${esc(item.renamed)}</dd>` : ''}</dl></article>`;
}

function focusSource(selector) {
  requestAnimationFrame(() => document.querySelector(selector)?.focus());
}

function excerpt(text, limit = 150) {
  const flat = String(text || '').replace(/\s+/g, ' ').trim();
  return flat.length > limit ? `${flat.slice(0, limit - 1)}…` : flat;
}

function ticketReference(ticket) {
  return ticket.number ? `#${ticket.number}` : `@${shortID(ticket.id)}`;
}

function shortID(value = '') {
  return String(value).slice(0, 8);
}

function formatDate(value) {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'});
}

function setRunURL(runId) {
  const next = new URL(location.href);
  if (runId) next.searchParams.set('run', runId); else next.searchParams.delete('run');
  history.replaceState(null, '', next);
}

function openStream(after) {
  if (!store.runId) return;
  const stream = new EventSource(scopedPath(`/api/stream?run=${encodeURIComponent(store.runId)}&after=${after}`));
  stream.addEventListener('patch', (message) => {
    const event = JSON.parse(message.data);
    if (!store.events.some((item) => item.sequence === event.sequence)) {
      store.events.push(event);
    }
    if (event.cord_id) {
      store.activeCords.add(event.cord_id);
      setTimeout(() => { store.activeCords.delete(event.cord_id); renderCords(); }, 800);
    }
    scheduleRefresh();
    renderEvents();
  });
  stream.addEventListener('error', () => {
    if (stream.readyState === EventSource.CLOSED) notice('signal stream closed. reload to reconnect.', true);
  });
  store.stream = stream;
}

function scheduleRefresh() {
  clearTimeout(store.refreshTimer);
  store.refreshTimer = setTimeout(async () => {
    if (!store.runId) return;
    try {
      store.state = await api(`/api/state?run=${encodeURIComponent(store.runId)}`);
      await refreshGates();
      renderAll();
    } catch (_) {}
  }, 45);
}

async function refreshGates() {
  if (!store.runId) { store.gates = []; return; }
  const run = store.runId, root = store.session?.root;
  try {
    const result = await api(`/api/gates?run=${encodeURIComponent(run)}`);
    if (run !== store.runId || root !== store.session?.root) return;
    if (result.last_sequence < store.gateSequence) return;
    store.gateSequence = result.last_sequence;
    store.gates = result.requests || [];
    store.gateDecisions = result.decisions || [];
    store.gateError = '';
  } catch (error) {
    if (run === store.runId && root === store.session?.root) store.gateError = `cannot refresh decisions: ${error.message}`;
  }
}

async function control(action) {
  if (!store.runId) return;
  try {
    store.state = await api('/api/control', {method: 'POST', body: JSON.stringify({run_id: store.runId, action})});
    renderAll();
    notice(`patch ${{pause: 'paused', resume: 'resumed', stop: 'stopped'}[action]}`);
  } catch (_) {}
}

async function operate(operations, removal = 'reject') {
  if (!store.runId) {
    try {
      const result = await api('/api/author', {method:'POST', body:JSON.stringify({expected_revision:store.session.patch.revision, operations})});
      store.session.patch = result.description;
      store.state.topology = result.description;
      store.state.patch_revision = result.description.revision;
      store.state.topology_revision = result.description.topology_revision;
      renderAll();
      return result;
    } catch (error) {
      store.session = await api('/api/session');
      await selectRun('');
      notice(`${error.message} — refreshed the patch; your edit was not applied.`, true);
      return null;
    }
  }
  if (store.liveEdit && !['completed','stopped','failed'].includes(store.state.status)) {
    try {
      const result = await api('/api/operate', {method:'POST',body:JSON.stringify({run_id:store.runId,expected_topology_revision:store.state.topology_revision,operations,removal})});
      store.state.topology = result.after;
      store.state.patch_revision = result.after.revision;
      store.state.topology_revision = result.after.topology_revision;
      renderAll(); return result;
    } catch (_) { scheduleRefresh(); return null; }
  }
  notice('viewing a recorded run — choose edit patch to change nodes.', true);
  return null;
}

function renderAll() {
  renderHeader();
  renderNavigation();
  renderNodes();
  renderCords();
  renderView();
  renderInspector();
  document.querySelectorAll('#node-config, #cord-config').forEach(form => {
    form.classList.toggle('readonly', !canEditPatch());
    form.querySelectorAll('input, textarea, select, button').forEach(control => { control.disabled = !canEditPatch(); });
  });
  renderEvents();
}

function renderHeader() {
  const runs = store.session?.runs || [];
  const current = runs.find(run => run.run_id === store.runId);
  if (current && store.state) current.status = store.state.status;
  $('#run-select').innerHTML = ['<option value="">edit patch · no execution</option>', ...runs.map((run) =>
    `<option value="${esc(run.run_id)}" ${run.run_id === store.runId ? 'selected' : ''} ${run.error ? `disabled title="${esc(run.error)}"` : ''}>${esc(runLabel(run))} · ${esc(run.status)}</option>`
  )].join('');
  const status = store.state?.status || 'offline';
  $('#run-status').textContent = !store.runId ? 'editing · not running' : store.gates.length ? `${status} · gated` : status;
  $('#run-status').className = `status-lamp ${store.gates.length ? 'gated' : status}`;
  $('#revision').textContent = store.state ? `topology ${short(store.state.topology_revision)} · event ${store.state.last_sequence || 0}` : 'no live revision';
  const live = Boolean(store.runId);
  const terminal = ['completed', 'stopped', 'failed'].includes(status);
  $('#pause-run').disabled = !live || status !== 'running';
  $('#resume-run').disabled = !live || status !== 'paused';
  $('#stop-run').disabled = !live || terminal;
  if (terminal) store.liveEdit = false;
  $('#live-edit').hidden = !live || terminal;
  $('#live-edit').setAttribute('aria-pressed', String(store.liveEdit));
  $('#live-edit').textContent = store.liveEdit ? 'finish live edits' : 'enable live edits';
  $('#add-node').disabled = !canEditPatch();
  $('#add-node').title = store.liveEdit ? 'add a node to this live run' : live ? 'viewing a run — choose edit patch or enable live edits to add nodes' : 'add a node without starting execution';
  $('#new-run').hidden = live;
  $('#pause-run').hidden = !live || terminal || status==='paused';
  $('#resume-run').hidden = !live || terminal || status!=='paused';
  $('#stop-run').hidden = !live || terminal;
  $('#add-node').hidden = live && !store.liveEdit;
  $('.transport-controls').setAttribute('aria-label', live ? 'run controls; edit patch to change nodes' : 'patch authoring');
}

function topology() { return store.state?.topology || store.session?.patch || {nodes: [], cords: []}; }
function compactHarness() {return Boolean(store.runId)&&!store.liveEdit&&isTicketHarness(topology().nodes||[]);}
function displayLayout(node) {return compactHarness()?{x:harnessPositions[node.id][0],y:harnessPositions[node.id][1]}:node.layout||{};}
function canEditPatch() { return !store.runId || store.liveEdit && !['completed','stopped','failed'].includes(store.state?.status); }
function nodeSize(node) {
  if(compactHarness()) return {width:200,height:116};
  return {
    width: Math.max(190, node.layout?.width || 230),
    height: node.layout?.collapsed ? 42 : Math.max(116, node.layout?.height || 126),
  };
}
function nodeById(id) { return topology().nodes.find((node) => node.id === id); }
function portById(node, direction, id) { return (direction === 'inlet' ? node.inlets : node.outlets)?.find((port) => port.id === id); }
function eventNodeId(event) {
  if (event.node_id || event.invocation?.node_id) return event.node_id || event.invocation.node_id;
  if (!event.invocation_id) return '';
  return store.events.find((candidate) => candidate.invocation?.id === event.invocation_id)?.invocation?.node_id || '';
}

function nodeStatus(node) {
  if (store.gates.some((gate) => gate.node_id === node.id)) return ['gated', 'waiting at gate'];
  if ((store.state.active || []).some((active) => active.node_id === node.id)) return ['running', 'running'];
  const queued = Object.entries(store.state.queues || {}).filter(([key]) => key.startsWith(node.id + '.')).reduce((sum, [, count]) => sum + count, 0);
  if (queued) return ['queued', `${queued} queued`];
  if(!store.runId) return ['idle','ready to patch'];
  const result=nodeFeedback(store.events,node.id);
  return [result.tone,result.label];
}

function nodeIdentity(node) {
  if (node.kind === 'runtime') return `${node.runtime?.runtime || 'runtime'} · ${node.runtime?.model || 'default model'} · ${node.runtime?.profile || 'reason'}`;
  if (node.kind === 'builtin') return node.builtin?.type || 'builtin';
  return node.subpatch?.path || 'task tree';
}

function renderNodes() {
  const layer = $('#node-layer');
  const focused=layer.contains(document.activeElement)?{node:document.activeElement.dataset.node,port:document.activeElement.dataset.port,direction:document.activeElement.classList.contains('outlet')?'outlet':'inlet'}:null;
  const nodes = topology().nodes || [];
  $('#empty-patch').hidden = nodes.length !== 0;
  layer.innerHTML = nodes.map((node) => {
    const size = nodeSize(node);
    const [status, statusCopy] = nodeStatus(node);
    const selected = store.selected?.type === 'node' && store.selected.id === node.id;
    return `<article class="patch-node ${status} ${node.layout?.collapsed ? 'collapsed' : ''} ${selected ? 'selected' : ''}"
      data-node="${esc(node.id)}" tabindex="0" aria-label="${esc(node.id)}: ${esc(statusCopy)}">
      <div class="port-rail inlets">${portsHTML(node, 'inlet')}</div>
      <header class="node-head" data-drag-node="${esc(node.id)}"><strong>${esc(node.id.replaceAll('_',' '))}</strong><span class="node-kind">${esc(node.kind)}</span></header>
      <div class="node-body"><strong>${esc(nodeIdentity(node))}</strong><span>${(node.inlets || []).length} in · ${(node.outlets || []).length} out</span><span class="node-state"><i></i>${esc(statusCopy)}</span></div>
      <div class="port-rail outlets">${portsHTML(node, 'outlet')}</div>
    </article>`;
  }).join('');
  nodes.forEach((node) => {
    const element = layer.querySelector(`[data-node="${CSS.escape(node.id)}"]`);
    const size = nodeSize(node);
    element.style.left = `${displayLayout(node).x || 0}px`;
    element.style.top = `${displayLayout(node).y || 0}px`;
    element.style.width = `${size.width}px`;
    element.style.height = `${size.height}px`;
    element.querySelectorAll('.port-rail').forEach(rail=>[...rail.children].forEach((port,index)=>{port.style.top=`${100*(index+1)/(rail.children.length+1)}%`;}));
  });
  layer.querySelectorAll('.patch-node').forEach((element) => element.addEventListener('click', () => {
    store.selected = {type: 'node', id: element.dataset.node}; renderAll();
  }));
  layer.querySelectorAll('.patch-node').forEach(element=>element.addEventListener('keydown',event=>{if(event.target===element&&['Enter',' '].includes(event.key)){event.preventDefault();store.selected={type:'node',id:element.dataset.node};renderAll();document.querySelector(`.patch-node[data-node="${CSS.escape(element.dataset.node)}"]`)?.focus();}}));
  layer.querySelectorAll('[data-drag-node]').forEach((head) => head.addEventListener('pointerdown', beginNodeDrag));
  layer.querySelectorAll('.jack.outlet').forEach((jack) => jack.addEventListener('pointerdown', beginCordDrag));
  layer.querySelectorAll('.jack.inlet').forEach((jack) => jack.addEventListener('click', (event) => {
    event.stopPropagation();
    store.selected = {type: 'port', node: jack.dataset.node, direction: 'inlet', port: jack.dataset.port};
    renderAll();
  }));
  if(focused?.node){const selector=focused.port?`.jack.${focused.direction}[data-node="${CSS.escape(focused.node)}"][data-port="${CSS.escape(focused.port)}"]`:`.patch-node[data-node="${CSS.escape(focused.node)}"]`;layer.querySelector(selector)?.focus({preventScroll:true});}
}

function portsHTML(node, direction) {
  const ports = direction === 'inlet' ? node.inlets : node.outlets;
  return (ports || []).map((port) => `<span class="port">
    <button class="jack ${esc(port.kind)} ${direction}" data-node="${esc(node.id)}" data-port="${esc(port.id)}" data-kind="${esc(port.kind)}"
      aria-label="${esc(direction)} ${esc(node.id)} ${esc(port.id)}" title="${esc(port.id)} · ${esc(port.kind)}"></button>
    <span class="port-label">${esc(port.id)}</span>
  </span>`).join('');
}

function jackPoint(node, direction, portId) {
  const size = nodeSize(node);
  const ports = direction === 'inlet' ? (node.inlets || []) : (node.outlets || []);
  const index = Math.max(0, ports.findIndex((port) => port.id === portId));
  return {
    x: (displayLayout(node).x || 0) + (direction === 'outlet' ? size.width : 0),
    y: (displayLayout(node).y || 0) + size.height * (index + 1) / (ports.length + 1),
  };
}

function cordPath(from, to) {
  const bend=Math.max(45,Math.abs(to.x-from.x)*.4);
  if(to.x>=from.x)return `M ${from.x} ${from.y} C ${from.x+bend} ${from.y}, ${to.x-bend} ${to.y}, ${to.x} ${to.y}`;
  const lane=Math.max(from.y,to.y)+85;
  return `M ${from.x} ${from.y} H ${from.x+35} V ${lane} H ${to.x-35} V ${to.y} H ${to.x}`;
}

function displayCordPath(cord,from,to) {
  if(!compactHarness())return cordPath(from,to);
  if(cord.from.port==='revise') {const lane=cord.from.node==='human_gate'?480:cord.from.node==='review_decision'?500:265;return `M ${from.x} ${from.y} H ${from.x+18} V ${lane} H 1480 V ${to.y} H ${to.x}`;}
  if(cord.id==='research-direct') return `M ${from.x} ${from.y} H ${from.x+18} V 55 H ${to.x-18} V ${to.y} H ${to.x}`;
  if(cord.id==='fable-decision')return `M ${from.x} ${from.y} H ${from.x+18} V 720 H ${to.x-18} V ${to.y} H ${to.x}`;
  if(to.x<from.x){const lane=from.y+90;return `M ${from.x} ${from.y} H ${from.x+20} V ${lane} H ${to.x-20} V ${to.y} H ${to.x}`;}
  return cordPath(from,to);
}

function renderCords() {
  const svg = $('#cord-layer');
  const focused=svg.contains(document.activeElement)?document.activeElement.dataset.cord:'';
  const selected = store.selected?.type === 'cord' ? store.selected.id : '';
  let markup = (topology().cords || []).map((cord) => {
    const fromNode = nodeById(cord.from.node), toNode = nodeById(cord.to.node);
    if (!fromNode || !toNode) return '';
    const fromPort = portById(fromNode, 'outlet', cord.from.port);
    const path = displayCordPath(cord,jackPoint(fromNode, 'outlet', cord.from.port), jackPoint(toNode, 'inlet', cord.to.port));
    const classes = ['cord', fromPort?.kind === 'bang' ? 'bang' : '', selected === cord.id ? 'selected' : '', store.activeCords.has(cord.id) ? 'activity' : ''].filter(Boolean).join(' ');
    return `<path class="${classes}" d="${path}"></path><path class="cord-hit" data-cord="${esc(cord.id)}" d="${path}" tabindex="0" role="button" aria-label="inspect wire ${esc(cord.from.node)} to ${esc(cord.to.node)}"></path>`;
  }).join('');
  if (store.cordDraft) {
    const source = nodeById(store.cordDraft.node);
    if (source) markup += `<path class="cord-preview" d="${cordPath(jackPoint(source, 'outlet', store.cordDraft.port), store.cordDraft.point)}"></path>`;
  }
  svg.innerHTML = markup;
  svg.querySelectorAll('[data-cord]').forEach((path) => path.addEventListener('click', () => {
    store.selected = {type: 'cord', id: path.dataset.cord}; renderAll();
  }));
  svg.querySelectorAll('[data-cord]').forEach(path=>path.addEventListener('keydown',event=>{if(['Enter',' '].includes(event.key)){event.preventDefault();store.selected={type:'cord',id:path.dataset.cord};renderAll();document.querySelector(`[data-cord="${CSS.escape(path.dataset.cord)}"]`)?.focus();}}));
  if(focused)svg.querySelector(`[data-cord="${CSS.escape(focused)}"]`)?.focus({preventScroll:true});
}

function fitPatch() {
  const nodes=topology().nodes||[];if(!nodes.length)return;
  store.selected=null;store.historyOpen=false;renderInspector();
  const left=Math.min(...nodes.map(n=>displayLayout(n).x||0)),top=Math.min(...nodes.map(n=>displayLayout(n).y||0));
  const right=Math.max(...nodes.map(n=>(displayLayout(n).x||0)+nodeSize(n).width))+65,bottom=Math.max(...nodes.map(n=>(displayLayout(n).y||0)+nodeSize(n).height))+65;
  const viewport=$('#patch-viewport');
  // Browser focus/scrollIntoView can scroll even overflow:hidden. Fit resets
  // that native offset so the world transform remains the framing authority.
  viewport.scrollLeft=0;viewport.scrollTop=0;
  const scale=Math.min(1,Math.max(.25,Math.min((viewport.clientWidth-100)/(right-left),(viewport.clientHeight-120)/(bottom-top))));
  store.view={scale,x:(viewport.clientWidth-(right-left)*scale)/2-left*scale,y:(viewport.clientHeight-(bottom-top)*scale)/2-top*scale};renderView();
}

function beginNodeDrag(event) {
  if (event.button !== 0 || !canEditPatch()) return;
  event.preventDefault(); event.stopPropagation();
  const id = event.currentTarget.dataset.dragNode;
  const node = nodeById(id);
  store.drag = {id, startX: event.clientX, startY: event.clientY, x: node.layout?.x || 0, y: node.layout?.y || 0};
}

function beginPan(event) {
  if (event.button !== 1 && !(event.button===0&&!event.target.closest('.patch-node,.cord-hit'))) return;
  event.preventDefault();
  event.stopPropagation();
  event.currentTarget.setPointerCapture(event.pointerId);
  store.pan = {
    pointerId: event.pointerId,
    startX: event.clientX,
    startY: event.clientY,
    x: store.view.x,
    y: store.view.y,
  };
  $('#patch-viewport').classList.add('panning');
}

function preventCanvasAuxClick(event) {
  if (event.button === 1) event.preventDefault();
}

function zoomCanvas(event) {
  event.preventDefault();
  const factor = Math.exp(-event.deltaY * 0.0015);
  store.view = zoomViewAt(store.view, event.clientX, event.clientY, event.currentTarget.getBoundingClientRect(), store.view.scale * factor);
  renderView();
}

function renderView() {
  const {x, y, scale} = store.view;
  const world = $('#patch-world');
  const viewport = $('#patch-viewport');
  world.style.transform = `translate(${x}px, ${y}px) scale(${scale})`;
  viewport.style.backgroundPosition = `${x}px ${y}px, ${x}px ${y}px, ${x}px ${y}px, ${x}px ${y}px`;
  viewport.style.backgroundSize = `${20 * scale}px ${20 * scale}px, ${20 * scale}px ${20 * scale}px, ${100 * scale}px ${100 * scale}px, ${100 * scale}px ${100 * scale}px`;
  $('#zoom-level').textContent = `${Math.round(scale * 100)}%`;
  try {const selected=['node','cord'].includes(store.selected?.type)?{type:store.selected.type,id:store.selected.id}:null;sessionStorage.setItem(canvasViewKey(store.runId),JSON.stringify({view:store.view,selected}));}catch{}
}

function canvasViewKey(run) {return `smith.canvas.view:${store.session?.root}:${run}`;}
function readCanvasView(run) {
  try {const saved=JSON.parse(sessionStorage.getItem(canvasViewKey(run)));if(saved&&['x','y','scale'].every(k=>Number.isFinite(saved.view?.[k]))&&saved.view.scale>=.25&&saved.view.scale<=2.5){if(!['node','cord'].includes(saved.selected?.type)||typeof saved.selected?.id!=='string')saved.selected=null;return saved;}}catch{}
  return null;
}

function endPan(event) {
  if (!store.pan || (event.pointerId !== undefined && event.pointerId !== store.pan.pointerId)) return;
  const pointerId = store.pan.pointerId;
  store.pan = null;
  const viewport = $('#patch-viewport');
  viewport.classList.remove('panning');
  if (viewport.hasPointerCapture(pointerId)) viewport.releasePointerCapture(pointerId);
}

function beginCordDrag(event) {
  if (event.button !== 0 || !canEditPatch()) return;
  event.preventDefault(); event.stopPropagation();
  const jack = event.currentTarget;
  store.cordDraft = {node: jack.dataset.node, port: jack.dataset.port, kind: jack.dataset.kind, point: clientToWorld(event.clientX, event.clientY)};
  renderCords();
}

function clientToWorld(x, y) {
  const viewport = $('#patch-viewport');
  return clientToWorldPoint(x, y, viewport.getBoundingClientRect(), store.view);
}

function pointerMove(event) {
  if (store.pan && event.pointerId === store.pan.pointerId) {
    store.view.x = store.pan.x + event.clientX - store.pan.startX;
    store.view.y = store.pan.y + event.clientY - store.pan.startY;
    renderView();
    return;
  }
  if (store.drag) {
    const node = nodeById(store.drag.id);
    node.layout.x = Math.max(0, Math.round((store.drag.x + worldPointerDelta(event.clientX - store.drag.startX, store.view.scale)) / 10) * 10);
    node.layout.y = Math.max(0, Math.round((store.drag.y + worldPointerDelta(event.clientY - store.drag.startY, store.view.scale)) / 10) * 10);
    const element = document.querySelector(`[data-node="${CSS.escape(node.id)}"]`);
    if (element) { element.style.left = node.layout.x + 'px'; element.style.top = node.layout.y + 'px'; }
    renderCords();
  }
  if (store.cordDraft) {
    store.cordDraft.point = clientToWorld(event.clientX, event.clientY);
    renderCords();
  }
}

async function pointerUp(event) {
  if (store.pan) {
    endPan(event);
    return;
  }
  if (store.drag) {
    const drag = store.drag; store.drag = null;
    const node = nodeById(drag.id);
    await operate([{type: 'move_node', node_id: node.id, position: {x: node.layout.x, y: node.layout.y}}]);
  }
  if (store.cordDraft) {
    const draft = store.cordDraft; store.cordDraft = null;
    const target = document.elementFromPoint(event.clientX, event.clientY)?.closest('.jack.inlet');
    renderCords();
    if (!target) return;
    if (target.dataset.kind !== draft.kind) {
      notice(`${draft.kind} cannot connect to ${target.dataset.kind}`, true);
      return;
    }
    const id = uniqueCordId(`${draft.node}-${target.dataset.node}`);
    const result = await operate([{type: 'connect', cord: {
      id, from: {node: draft.node, port: draft.port}, to: {node: target.dataset.node, port: target.dataset.port},
      delivery: {mode: 'enqueue'},
    }}]);
    if (result) { store.selected = {type: 'cord', id}; notice('cord connected'); }
  }
}

function uniqueCordId(base) {
  const normalized = base.replace(/[^A-Za-z0-9._-]/g, '-').replace(/^-+/, '') || 'cord';
  const used = new Set((topology().cords || []).map((cord) => cord.id));
  if (!used.has(normalized)) return normalized;
  let index = 2;
  while (used.has(`${normalized}-${index}`)) index++;
  return `${normalized}-${index}`;
}

function renderGates() {
  const rack = $('#gate-rack');
  const records = [...store.gates, ...store.gateDecisions].filter(gate=>store.selected?.type==='node'?gate.node_id===store.selected.id:!store.selected&&(store.historyOpen||gate.state==='pending'));
  rack.hidden = !store.runId || !records.length && !store.gateError;
  $('#gate-count').textContent = store.gates.length ? `${store.gates.length} waiting` : '';
  const signature = JSON.stringify([records, store.gateError]);
  // Leave focused review buttons in place when unrelated signals arrive.
  if (rack.dataset.signature !== signature) {
    rack.dataset.signature = signature;
    $('#gates').innerHTML = `${store.gateError ? `<p role="alert">${esc(store.gateError)}</p>` : ''}${records.length ? records.map((gate, index) => `<article class="gate-card">
      <p><strong>${esc(gate.node_id)}</strong> · ${esc(gate.state)}</p>
      <p>${esc(gate.prompt || 'review this payload before it continues.')}</p>
      <p class="gate-meta"><span data-gate-age="${esc(gate.requested_at)}"></span><br>request ${esc(gate.request_id)}</p>
      ${gate.state === 'pending' ? gateEvidence(gate.payload) : `<p>${esc(gate.reason)}</p>`}
      ${gate.state === 'pending' && !gate.can_decide ? '<p>decision unavailable on this controller</p>' : ''}
      <button data-review-gate="${index}">${gate.state === 'pending' ? 'review decision' : 'view decision receipt'}</button>
    </article>`).join('') : '<p class="empty-copy">no pending human decisions for this run.</p>'}`;
    rack.querySelectorAll('[data-review-gate]').forEach(button => button.addEventListener('click', () => openGateReview(records[Number(button.dataset.reviewGate)])));
  }
  rack.querySelectorAll('[data-gate-age]').forEach(node => { node.textContent = `requested ${gateAge(node.dataset.gateAge)} ago`; });
  renderGateReview();
}

function gateAge(at) {
  const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(at)) / 1000));
  if (!Number.isFinite(seconds)) return 'unknown time';
  return seconds < 60 ? `${seconds}s` : seconds < 3600 ? `${Math.floor(seconds/60)}m` : `${Math.floor(seconds/3600)}h ${Math.floor(seconds%3600/60)}m`;
}

function gateEvidence(payload) {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return '<p class="empty-copy">inspect the complete payload below.</p>';
  const compact = value => typeof value === 'string' ? value : JSON.stringify(value);
  const rows = [
    ['ticket', payload.ticket_id], ['changes', payload.changes ?? payload.work_summary],
    ['reported checks', typeof payload.checks_passed === 'boolean' ? payload.checks_passed ? 'passed' : 'failed' : undefined],
    ['reviewer verdict', typeof payload.accepted === 'boolean' ? payload.accepted ? 'accepted' : 'rejected' : undefined],
    ['reviewer feedback', payload.review_feedback], ['doubts', payload.doubts],
  ].filter(([, value]) => value !== undefined && value !== null && value !== '');
  return rows.length ? `<dl class="gate-evidence">${rows.map(([key,value]) => `<dt>${esc(key)}</dt><dd>${esc(compact(value).slice(0,600))}${compact(value).length > 600 ? '… (complete text in payload)' : ''}</dd>`).join('')}</dl>` : '<p class="empty-copy">no harness summary fields; inspect the complete payload.</p>';
}

function openGateReview(gate) {
  const key = JSON.stringify([store.session.root, store.runId, gate.request_id]);
  store.gateReview = {key, gate, run: store.runId, root: store.session.root, error: '', submitting: false};
  $('#gate-reason').value = store.gateDrafts.get(key) || '';
  $('#gate-payload').open = false;
  renderGateReview();
  $('#gate-dialog').showModal();
}

function closeGateReview() {
  $('#gate-dialog').close();
  store.gateReview = null;
}

function renderGateReview() {
  const review = store.gateReview;
  if (!review) return;
  const gate = [...store.gates, ...store.gateDecisions].find(item => item.request_id === review.gate.request_id) || {...review.gate, state:'unavailable', can_decide:false};
  review.gate = gate;
  $('#gate-title').textContent = gate.state === 'pending' ? 'review decision' : 'decision receipt';
  $('#gate-location').textContent = `${currentLibraryPatch()?.name || 'patch'} / ${review.run} / ${gate.node_id}`;
  $('#gate-prompt').textContent = gate.prompt || 'review this payload before it continues.';
  $('#gate-meta').textContent = `requested ${gateAge(gate.requested_at)} ago · ${gate.requested_at} · request ${gate.request_id}`;
  const evidence = gateEvidence(gate.payload);
  if ($('#gate-evidence').innerHTML !== evidence) $('#gate-evidence').innerHTML = evidence;
  const payload = JSON.stringify(gate.payload ?? null, null, 2);
  if ($('#gate-payload-text').textContent !== payload) $('#gate-payload-text').textContent = payload;
  $('#gate-result').textContent = gate.state === 'pending' ? gate.can_decide ? 'waiting for your decision' : gate.unavailable_reason : `${gate.state}${gate.decided_at ? ` · ${gate.decided_at}` : ''}${gate.reason ? ` — ${gate.reason}` : ''}`;
  const error = review.error || store.gateError;
  $('#gate-error').hidden = !error;
  $('#gate-error').textContent = error;
  $('#gate-decision-form').hidden = gate.state !== 'pending';
  $('#gate-reason').disabled = review.submitting;
  const allowed = gate.state === 'pending' && gate.can_decide && !store.gateError && !review.submitting && $('#gate-reason').value.trim();
  $('#gate-approve').disabled = !allowed;
  $('#gate-reject').disabled = !allowed;
  $('#gate-help').textContent = review.submitting ? 'recording your decision…' : !gate.can_decide ? 'evidence is readable. deciding requires the owning controller or explicit run recovery.' : !$('#gate-reason').value.trim() ? 'enter a reason to enable approve and reject.' : 'both approval and rejection record your reason in the run history.';
}

$('#close-gate').addEventListener('click', closeGateReview);
$('#gate-dialog').addEventListener('cancel', () => { store.gateReview = null; });
$('#gate-reason').addEventListener('input', () => {
  if (!store.gateReview) return;
  store.gateDrafts.set(store.gateReview.key, $('#gate-reason').value);
  renderGateReview();
});
$('#gate-decision-form').addEventListener('submit', async event => {
  event.preventDefault();
  const review = store.gateReview, reason = $('#gate-reason').value;
  if (!review || review.submitting || !review.gate.can_decide || !reason.trim() || !event.submitter) return;
  const approved = event.submitter.value === 'approve';
  review.submitting = true;
  review.error = '';
  renderGateReview();
  try {
    await api('/api/gates/decide', {method:'POST', body:JSON.stringify({run_id:review.run, request_id:review.gate.request_id, approved, reason})});
    store.gateDrafts.delete(review.key);
    await refreshGates();
    notice(approved ? 'gate approved — reason recorded' : 'gate rejected — reason recorded');
  } catch (error) {
    review.error = error.message;
    await refreshGates();
  } finally {
    review.submitting = false;
    if (store.gateReview === review) renderGates();
  }
});

// Poll read-only projections as well as SSE: a restarted controller has no
// in-memory waiter, but its persisted request must remain visible.
setInterval(async () => {
  if (store.gatePolling || document.hidden || !store.session) return;
  store.gatePolling = true;
  try {
    if (store.navigation.screen === 'project') {
      const library = await api('/api/library');
      if (JSON.stringify(library) !== JSON.stringify(store.navigation.library)) {
        store.navigation.library = library;
        renderNavigation();
      }
    }
    else if (store.runId) { await refreshGates(); renderGates(); }
  } catch (_) {} finally { store.gatePolling = false; }
}, 3000);

function renderInspector() {
  renderGates();
  const target = $('#inspector-content');
  const selection = store.selected;
  $('.scope').hidden=!selection&&!store.historyOpen;
  $('.history').hidden=!store.historyOpen;
  $('#show-history').setAttribute('aria-pressed',String(store.historyOpen));
  $('#inspector-title').textContent=selection?.type==='node'?selection.id:selection?.type==='cord'?'wire messages':'inspect';
  if (!selection) {
    target.innerHTML = '<p class="empty-copy">select a node, cord, port, or event.</p>';
    return;
  }
  if (selection.type === 'node') return renderNodeInspector(target, nodeById(selection.id));
  if (selection.type === 'cord') return renderCordInspector(target, (topology().cords || []).find((cord) => cord.id === selection.id));
  if (selection.type === 'port') return renderPortInspector(target, selection);
  if (selection.type === 'event') return renderEventInspector(target, selection.event);
  if (selection.type === 'detail') {
    target.innerHTML = `<dl class="fact-grid"><dt>kind</dt><dd>${esc(selection.value.kind)}</dd><dt>id</dt><dd>${esc(selection.value.id)}</dd></dl><pre>${esc(JSON.stringify(selection.value.payload || selection.value.events || selection.value.invocation || {}, null, 2))}</pre>`;
  }
}

function renderNodeInspector(target, node) {
  if (!node) { store.selected = null; renderInspector(); return; }
  const identity = JSON.stringify([store.launchId, store.runId, node]);
  if (target.querySelector('#node-config')?.dataset.identity === identity) {
    renderNodeEvidence(node);
    void loadNodeProvenance(node.id);
    return;
  }
  const referenceFields = node.kind === 'runtime'
    ? `<label>runtime<input name="runtime" value="${esc(node.runtime?.runtime)}"></label><label>model<input name="model" value="${esc(node.runtime?.model)}"></label><label>profile<select name="profile">${['reason','inspect','work'].map((value) => `<option ${node.runtime?.profile === value ? 'selected' : ''}>${value}</option>`).join('')}</select></label>`
    : node.kind === 'builtin'
      ? `<label>builtin<select name="builtin">${['passthrough','switch','router','human_gate','capability_assert','workspace_handoff'].map((value) => `<option ${node.builtin?.type === value ? 'selected' : ''}>${value}</option>`).join('')}</select></label>`
      : `<label>task tree path<input name="subpatch" value="${esc(node.subpatch?.path)}"></label>`;
  target.innerHTML = `<div id="node-evidence" class="node-evidence"></div><details ${store.runId?'':'open'}><summary>node configuration</summary><form class="inspector-form" id="node-config">
    <dl class="fact-grid"><dt>node</dt><dd>${esc(node.id)}</dd><dt>kind</dt><dd>${esc(node.kind)}</dd></dl>
    ${referenceFields}
    <label>configuration<textarea name="config">${esc(JSON.stringify(node.config || {}, null, 2))}</textarea></label>
    <div class="form-actions"><button class="key">save configuration</button><button type="button" id="collapse-node">${node.layout?.collapsed ? 'expand' : 'collapse'}</button><button type="button" id="remove-node" class="stop">remove</button></div>
  </form></details><section id="node-provenance" class="node-provenance" aria-live="polite"></section><div class="port-list"><strong>inlets</strong>${(node.inlets || []).map((port) => portRow(node, port, 'inlet')).join('')}<strong>outlets</strong>${(node.outlets || []).map((port) => portRow(node, port, 'outlet')).join('')}</div>`;
  $('#node-config').dataset.identity = identity;
  renderNodeEvidence(node);
  void loadNodeProvenance(node.id);
  $('#node-config').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    let values;
    try { values = JSON.parse(form.get('config')); } catch (error) { notice(`configuration is not json: ${error.message}`, true); return; }
    const config = {kind: node.kind, values};
    if (node.kind === 'runtime') config.runtime = {runtime: form.get('runtime'), model: form.get('model'), profile: form.get('profile')};
    if (node.kind === 'builtin') config.builtin = {type: form.get('builtin')};
    if (node.kind === 'subpatch') config.subpatch = {path: form.get('subpatch')};
    if (await operate([{type: 'configure_node', node_id: node.id, config}])) notice('node configuration saved');
  });
  $('#collapse-node').addEventListener('click', async () => {
    const layout = {...node.layout, collapsed: !node.layout?.collapsed};
    if (await operate([{type: 'layout_node', node_id: node.id, layout}])) notice(layout.collapsed ? 'subpatch collapsed' : 'node expanded');
  });
  $('#remove-node').addEventListener('click', async () => {
    if (await operate([{type: 'remove_node', node_id: node.id}])) { store.selected = null; notice('node removed'); }
  });
  target.querySelectorAll('[data-port]').forEach((button) => button.addEventListener('click', () => {
    store.selected = {type: 'port', node: node.id, direction: button.dataset.direction, port: button.dataset.port}; renderInspector();
  }));
}

function renderNodeEvidence(node) {
  const target=$('#node-evidence');if(!target)return;
  const result=nodeFeedback(store.events,node.id),[,label]=nodeStatus(node);
  const evidence=result.events.filter(e=>['checks.finished','node.observed','gate.requested','gate.resolved','gate.rejected','invocation.failed','capability.call_completed'].includes(e.type));
  const key=JSON.stringify([label,result.attempts,evidence.map(e=>e.sequence)]);if(target.dataset.key===key)return;
  const opened=new Set([...target.querySelectorAll('details[open]')].map(d=>d.dataset.seq));
  target.dataset.key=key;
  target.innerHTML=`<p class="node-observed-state">${esc(label)}</p><p class="empty-copy">${result.attempts} recorded attempt${result.attempts===1?'':'s'}. evidence below belongs to the latest attempt.</p>${evidence.length?`<h3>observed evidence</h3>${evidence.map(e=>`<details data-seq="${e.sequence}"><summary>${esc(e.type==='checks.finished'?`${e.data?.id||'check'}: ${e.data?.passed===true?'passed':'failed'}`:eventSummary(e)[0])}</summary><p class="empty-copy">event ${e.sequence}</p><p>${esc(e.error||e.reason||'recorded by the execution service')}</p><button class="quiet" data-node-event="${e.sequence}">inspect event</button></details>`).join('')}`:''}<h3>connected wires</h3>${topology().cords.filter(c=>c.from.node===node.id||c.to.node===node.id).map(c=>`<button class="quiet" data-node-wire="${esc(c.id)}">${esc(c.from.node)} → ${esc(c.to.node)} (${cordMessages(store.events,c.id).length})</button>`).join('')||'<p class="empty-copy">no connected wires</p>'}`;
  target.querySelectorAll('details').forEach(d=>d.open=opened.has(d.dataset.seq));
  target.querySelectorAll('[data-node-wire]').forEach(b=>b.onclick=()=>{store.selected={type:'cord',id:b.dataset.nodeWire};renderAll();});
  target.querySelectorAll('[data-node-event]').forEach(b=>b.onclick=()=>{store.selected={type:'event',event:store.events.find(e=>e.sequence===Number(b.dataset.nodeEvent))};renderInspector();});
}

function portRow(node, port, direction) {
  return `<div class="port-row"><span>${esc(port.id)} · ${esc(port.kind)}</span><button type="button" data-port="${esc(port.id)}" data-direction="${direction}">inspect</button></div>`;
}

async function loadNodeProvenance(nodeID) {
  const target = $('#node-provenance');
  if (!target) return;
  if (!store.runId) { target.innerHTML = '<p class="empty-copy">no invocation yet. context has not been supplied.</p>'; return; }
  const invocation = store.selected?.invocation || '';
  const subject = [store.launchId, store.runId, nodeID, invocation].join(':');
  const key = [subject, store.state.last_sequence].join(':');
  if (store.provenance.key === key) { renderNodeProvenance(target, store.provenance.data, store.provenance.error); return; }
  if (target.dataset.subject !== subject) {
    target.dataset.subject = subject;
    target.innerHTML = '<p class="empty-copy">reading invocation provenance…</p>';
  }
  if (store.provenance.pending === key) return;
  store.provenance.pending = key;
  let data = null, error = '';
  try { data = await api(`/api/provenance?run=${encodeURIComponent(store.runId)}&node=${encodeURIComponent(nodeID)}&invocation=${encodeURIComponent(invocation)}`); }
  catch (err) { error = err.message; }
  if (store.provenance.pending !== key) return;
  store.provenance = {key, data, error, pending: ''};
  if (store.selected?.type === 'node' && store.selected.id === nodeID && (store.selected.invocation || '') === invocation) {
    const current = $('#node-provenance');
    if (current) renderNodeProvenance(current, data, error);
  }
}

function renderNodeProvenance(target, data, error) {
  const rendered = JSON.stringify([data, error, store.selected?.invocation]);
  if (target.dataset.rendered === rendered) return;
  const openReceipts = new Set([...target.querySelectorAll('details[open]')].map((item) => item.dataset.receiptKey));
  const controlKey = (item) => item?.id || (item?.tagName === 'SUMMARY' ? item.parentElement.dataset.receiptKey : '') || item?.dataset.inspectMemory || (item?.hasAttribute('data-inspect-ticket') ? 'ticket' : '');
  const focused = target.contains(document.activeElement) ? controlKey(document.activeElement) : '';
  target.dataset.rendered = rendered;
  if (error) { target.innerHTML = `<p role="alert">${esc(error)}</p><button data-provenance-retry>read again</button>`; target.querySelector('button').onclick = () => { store.provenance.key = ''; void loadNodeProvenance(store.selected.id); }; return; }
  if (!data?.invocation) { target.innerHTML = '<p class="empty-copy">this node has not run. no context has been supplied.</p>'; return; }
  const artifactHTML = (artifact) => `<details><summary>${esc(artifact.name)} · ${artifact.bytes} bytes</summary><dl class="fact-grid"><dt>revision</dt><dd>${esc(artifact.revision || 'unversioned')}</dd><dt>sha256</dt><dd>${esc(artifact.sha256)}</dd><dt>placement</dt><dd>${esc(artifact.placement)}</dd></dl>${/^(project|reference)_[A-Za-z0-9_-]+$/.test(artifact.name) ? `<button data-inspect-memory="${esc(artifact.name)}">inspect current memory</button>` : ''}</details>`;
  target.innerHTML = `<h3>invocation provenance</h3><label>invocation<select id="provenance-invocation"><option value="">latest</option>${(data.invocations || []).map((item) => `<option value="${esc(item.id)}" ${store.selected.invocation === item.id ? 'selected' : ''}>${esc(item.id.split('/').pop())}</option>`).join('')}</select></label>
    <dl class="fact-grid"><dt>invocation</dt><dd>${esc(data.invocation.id)}</dd><dt>topology</dt><dd>${esc(data.invocation.topology_revision)}</dd></dl>
    ${data.work ? `<section><h4>work item</h4><p>${esc(data.ticket?.title || data.work.ticket_id)}</p><dl class="fact-grid"><dt>project</dt><dd>${esc(data.work.project_slug)}</dd><dt>ticket</dt><dd>${esc(data.work.ticket_id)}</dd><dt>current state</dt><dd>${esc(data.ticket?.column || 'unavailable')}</dd></dl>${data.work_error ? `<p role="alert">${esc(data.work_error)}</p>` : ''}<button data-inspect-ticket>open full ticket</button></section>` : '<p class="empty-copy">no work reference in this invocation’s input.</p>'}
    <section><h4>requested context</h4><pre>${esc(JSON.stringify(data.requested || [], null, 2))}</pre></section>
    <section><h4>supplied to runtime</h4>${data.supplied?.length ? data.supplied.map(artifactHTML).join('') : '<p class="empty-copy">no supplied context recorded.</p>'}</section>
    <details><summary>resolved before runtime launch</summary>${(data.resolved || []).map(artifactHTML).join('') || '<p>no resolved context recorded.</p>'}</details>
    ${(data.failures || []).map((failure) => `<p class="provenance-failure">${esc(failure)}</p>`).join('')}
    <section><h4>ticket capability calls</h4>${(data.capabilities || []).map((call) => `<details><summary>${esc(call.tool || call.package)} · ${call.is_error ? 'failed' : 'completed'}</summary><dl class="fact-grid"><dt>body</dt><dd>${esc(call.body)}</dd><dt>invocation</dt><dd>${esc(call.invocation_id)}</dd><dt>ticket</dt><dd>${esc(call.ticket_id || '—')}</dd></dl><pre>${esc(JSON.stringify(call.facts || {}, null, 2))}</pre>${call.error ? `<p>${esc(call.error)}</p>` : ''}</details>`).join('') || '<p class="empty-copy">no calls recorded.</p>'}</section>
    <details><summary>model and session receipts</summary><pre>${esc(JSON.stringify(data.runtime || [], null, 2))}</pre></details>`;
  const duplicates = new Map();
  target.querySelectorAll('details').forEach((item) => {
    const parent = item.parentElement.closest('details')?.dataset.receiptKey || '';
    const label = `${parent}/${item.querySelector(':scope > summary').textContent}`;
    const count = duplicates.get(label) || 0;
    duplicates.set(label, count + 1);
    item.dataset.receiptKey = `${label}:${count}`;
    item.open = openReceipts.has(item.dataset.receiptKey);
  });
  if (focused) [...target.querySelectorAll('select, summary, button')].find((item) => controlKey(item) === focused)?.focus({preventScroll: true});
  target.querySelector('#provenance-invocation').addEventListener('change', (event) => { store.selected.invocation = event.target.value; void loadNodeProvenance(store.selected.id); });
  target.querySelector('[data-inspect-ticket]')?.addEventListener('click', async () => {
    store.sources.tab = 'work'; store.sources.mode = 'ready'; store.sources.work.project = data.work.project_slug; store.sources.selected = null;
    setSourcesOpen(true);
    await loadCurrentSource();
    await loadSourceDetail(data.work.ticket_id);
  });
  target.querySelectorAll('[data-inspect-memory]').forEach((button) => button.addEventListener('click', async () => {
    store.sources.tab = 'memory'; store.sources.mode = 'index'; store.sources.selected = null;
    setSourcesOpen(true);
    await loadSourceDetail(button.dataset.inspectMemory);
  }));
}

function renderCordInspector(target, cord) {
  if (!cord) { store.selected = null; renderInspector(); return; }
  const identity=JSON.stringify([store.runId,cord]);
  if(target.querySelector('#cord-config')?.dataset.identity===identity){renderWireMessages(cord);return;}
  target.innerHTML = `<div id="wire-messages" class="wire-message"></div><details ${store.runId?'':'open'}><summary>wire configuration</summary><form class="inspector-form" id="cord-config">
    <dl class="fact-grid"><dt>cord</dt><dd>${esc(cord.id)}</dd><dt>from</dt><dd>${esc(cord.from.node)} · ${esc(cord.from.port)}</dd><dt>to</dt><dd>${esc(cord.to.node)} · ${esc(cord.to.port)}</dd></dl>
    <label>delivery<select name="delivery"><option ${cord.delivery.mode === 'enqueue' ? 'selected' : ''}>enqueue</option><option ${cord.delivery.mode === 'latest' ? 'selected' : ''}>latest</option></select></label>
    <div class="form-actions"><button class="key">save delivery</button><button type="button" id="disconnect" class="stop">disconnect</button></div>
  </form></details>`;
  $('#cord-config').dataset.identity=identity;
  renderWireMessages(cord);
  $('#cord-config').addEventListener('submit', async (event) => {
    event.preventDefault(); const mode = new FormData(event.currentTarget).get('delivery');
    if (await operate([{type: 'configure_cord', cord_id: cord.id, delivery: {mode}}])) notice('cord delivery saved');
  });
  $('#disconnect').addEventListener('click', async () => {
    if (await operate([{type: 'disconnect', cord_id: cord.id}])) { store.selected = null; notice('cord disconnected'); }
  });
}

function renderWireMessages(cord) {
  const target=$('#wire-messages');if(!target)return;
  const messages=cordMessages(store.events,cord.id);
  const key=JSON.stringify(messages.map(m=>m.id));if(target.dataset.key===key)return;
  target.dataset.key=key;
  target.innerHTML=`<p>${esc(cord.from.node)} / ${esc(cord.from.port)} → ${esc(cord.to.node)} / ${esc(cord.to.port)}</p><p class="empty-copy">${messages.length} recorded message${messages.length===1?'':'s'}</p>${messages.length?`<label>message<select id="wire-message-select">${messages.map((m,i)=>`<option value="${esc(m.id)}">${i+1} / event ${m.sequence} / ${esc(m.kind)}</option>`).join('')}</select></label><div id="wire-payload"></div>`:'<p class="empty-copy">nothing has passed along this wire in this run.</p>'}`;
  if(!messages.length)return;
  const select=$('#wire-message-select');select.value=messages.some(m=>m.id===store.selected.message)?store.selected.message:messages.at(-1).id;
  select.onchange=()=>{store.selected.message=select.value;void loadWirePayload(cord.id,select.value);};
  store.selected.message=select.value;void loadWirePayload(cord.id,select.value);
}

async function loadWirePayload(cordID,id) {
  const run=store.runId,target=$('#wire-payload');if(!target)return;
  target.innerHTML='<p class="empty-copy">reading recorded message…</p>';
  try {
    const detail=await api(`/api/detail?run=${encodeURIComponent(run)}&kind=envelope&id=${encodeURIComponent(id)}`);
    if(store.runId!==run||store.selected?.id!==cordID||store.selected?.message!==id||!target.isConnected)return;
    const p=detail.payload||{},envelope=detail.envelope||{};
    const reports=[['work report',p.work_report],['review report',p.review_feedback],['carried check report',p.check_run],['carried human decision',p.human_decision],['model ticket state',p.ticket_state]].filter(([,value])=>value!==undefined);
    target.innerHTML=`${reports.length?'<h3>carried reports</h3><p class="empty-copy">historical message content, not the receiving node’s current verdict.</p>':''}${reports.map(([name,value])=>`<details><summary>${name}</summary><pre>${esc(typeof value==='string'?value:JSON.stringify(value,null,2))}</pre></details>`).join('')}<h3>recorded envelope</h3><dl class="fact-grid"><dt>id</dt><dd>${esc(envelope.id)}</dd><dt>parent</dt><dd>${esc(envelope.parent_envelope_id||'none')}</dd><dt>receiver</dt><dd>${esc(envelope.node_id)} / ${esc(envelope.port_id)}</dd><dt>payload hash</dt><dd>${esc(envelope.payload?.sha256||'bang; no payload')}</dd></dl>${detail.payload!==undefined?`<details><summary>complete recorded payload</summary><pre>${esc(JSON.stringify(detail.payload,null,2))}</pre></details>`:''}`;
  }catch(error){if(target.isConnected&&store.selected?.message===id)target.innerHTML=`<p role="alert">${esc(error.message)}</p>`;}
}

function renderPortInspector(target, selection) {
  const node = nodeById(selection.node);
  const port = node && portById(node, selection.direction, selection.port);
  if (!port) { store.selected = null; renderInspector(); return; }
  const canSend = selection.direction === 'inlet' && store.runId;
  target.innerHTML = `<dl class="fact-grid"><dt>node</dt><dd>${esc(node.id)}</dd><dt>${esc(selection.direction)}</dt><dd>${esc(port.id)}</dd><dt>signal</dt><dd>${esc(port.kind)}</dd></dl>
    ${canSend && port.kind === 'message' ? '<form class="inspector-form" id="send-message"><label>message payload<textarea class="payload-box" name="payload">{}</textarea></label><button class="key">send message</button></form>' : ''}
    ${canSend && port.kind === 'bang' ? '<button class="key" id="send-bang">send bang</button>' : ''}
    ${port.schema ? `<details><summary>message contract</summary><pre>${esc(JSON.stringify(port.schema, null, 2))}</pre></details>` : ''}`;
  $('#send-message')?.addEventListener('submit', async (event) => {
    event.preventDefault(); let payload;
    try { payload = JSON.parse(new FormData(event.currentTarget).get('payload')); } catch (error) { notice(`payload is not json: ${error.message}`, true); return; }
    await sendSignal(node.id, port.id, 'message', payload);
  });
  $('#send-bang')?.addEventListener('click', () => sendSignal(node.id, port.id, 'bang'));
}

async function sendSignal(nodeId, portId, kind, payload) {
  try {
    await api('/api/send', {method: 'POST', body: JSON.stringify({run_id: store.runId, node_id: nodeId, port_id: portId, kind, payload})});
    notice(kind === 'bang' ? 'bang sent' : 'message sent');
  } catch (_) {}
}

function renderEventInspector(target, event) {
  target.innerHTML = `<dl class="fact-grid"><dt>event</dt><dd>${esc(event.type)}</dd><dt>sequence</dt><dd>${event.sequence}</dd><dt>node</dt><dd>${esc(eventNodeId(event) || '—')}</dd><dt>invocation</dt><dd>${esc(event.invocation_id || event.invocation?.id || '—')}</dd><dt>revision</dt><dd>${esc(short(event.topology_revision))}</dd><dt>reason</dt><dd>${esc(event.reason || event.error || '—')}</dd></dl><button id="event-detail">inspect causal detail</button>`;
  $('#event-detail').addEventListener('click', () => loadEventDetail(event));
}

async function loadEventDetail(event) {
  let kind = '', id = '';
  if (event.type?.includes('failed')) { kind = 'failure'; id = event.invocation_id || event.envelope_id || ''; }
  else if (event.invocation?.id || event.invocation_id) { kind = 'invocation'; id = event.invocation?.id || event.invocation_id; }
  else if (event.envelope?.id || event.envelope_id) { kind = 'envelope'; id = event.envelope?.id || event.envelope_id; }
  if (!kind) { notice('this event has no deeper causal record'); return; }
  try {
    const detail = await api(`/api/detail?run=${encodeURIComponent(store.runId)}&kind=${kind}&id=${encodeURIComponent(id)}`);
    store.selected = {type: 'detail', value: detail}; renderInspector();
  } catch (_) {}
}

function eventSummary(event) {
  const names = {
    'patch.started': 'patch started', 'patch.paused': 'transport paused', 'patch.resumed': 'transport resumed',
    'patch.stopped': 'patch stopped', 'patch.document_committed': 'layout committed', 'topology.committed': 'patch rewired',
    'envelope.queued': 'signal queued', 'envelope.delivered': 'signal delivered', 'outlet.emitted': 'outlet emitted',
    'invocation.started': 'node started', 'invocation.completed': 'node completed', 'invocation.failed': 'node failed',
    'gate.requested': 'waiting at gate', 'gate.resolved': 'gate approved', 'gate.rejected': 'gate rejected',
    'runtime.started': 'model started', 'runtime.completed': 'model completed', 'feedback.limit_reached': 'feedback stopped',
  };
  const subject = eventNodeId(event) || event.cord_id || event.queue || '';
  return [names[event.type] || event.type.replaceAll('.', ' '), subject];
}

function renderEvents() {
  const target = $('#events');
  $('#event-cursor').textContent = String(store.events.at(-1)?.sequence || 0);
  if (!store.events.length) {
    target.innerHTML = '<p class="empty-copy">the causal tape begins when a run starts.</p>';
    return;
  }
  target.innerHTML = [...store.events].reverse().slice(0, 120).map((event) => {
    const [label, subject] = eventSummary(event);
    const tone = event.type.includes('gate.') ? 'gate' : event.type.includes('failed') || event.type.includes('limit') ? 'failure' : event.cord_id || event.type.includes('envelope') || event.type.includes('outlet') ? 'signal' : '';
    const selected = store.selected?.type === 'event' && store.selected.event.sequence === event.sequence;
    return `<button class="event ${tone} ${selected ? 'selected' : ''}" data-event="${event.sequence}"><span class="event-seq">${event.sequence}</span><span class="event-copy"><strong>${esc(label)}</strong><span>${esc(subject || new Date(event.at).toLocaleTimeString())}</span></span></button>`;
  }).join('');
  target.querySelectorAll('[data-event]').forEach((button) => button.addEventListener('click', () => {
    store.selected = {type: 'event', event: store.events.find((event) => event.sequence === Number(button.dataset.event))}; renderAll();
  }));
}

function showKindFields() {
  const kind = $('#node-form [name=kind]').value;
  document.querySelectorAll('.kind-fields').forEach((section) => { section.hidden = !section.classList.contains(kind + '-fields'); });
}

async function placeNode(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const values = new FormData(form);
  const kind = values.get('kind'), portKind = values.get('port_kind');
  const point = clientToWorld($('#patch-viewport').getBoundingClientRect().left + $('#patch-viewport').clientWidth / 2, $('#patch-viewport').getBoundingClientRect().top + $('#patch-viewport').clientHeight / 2);
  const makePort = (id) => id ? {id, kind: portKind, ...(portKind === 'message' ? {schema: {type: 'object'}} : {})} : null;
  const node = {
    id: values.get('id'), kind, config: {}, layout: {x: Math.max(0, point.x - 115), y: Math.max(0, point.y - 63)},
    inlets: [makePort(values.get('inlet'))].filter(Boolean), outlets: [makePort(values.get('outlet'))].filter(Boolean),
  };
  if (kind === 'runtime') node.runtime = {runtime: values.get('runtime'), model: values.get('model'), profile: values.get('profile')};
  if (kind === 'builtin') node.builtin = {type: values.get('builtin')};
  if (kind === 'subpatch') node.subpatch = {path: values.get('subpatch')};
  // Search free slots in the visible world before expanding below it. A
  // second node must never be hidden exactly underneath the first one.
  const viewport = $('#patch-viewport');
  const rect = viewport.getBoundingClientRect();
  const left = Math.max(0, clientToWorld(rect.left + 30, rect.top).x);
  const right = clientToWorld(rect.right - 30, rect.top).x;
  const size = nodeSize(node);
  const overlaps = () => (topology().nodes || []).some(existing => {
    const other = nodeSize(existing), x = existing.layout?.x || 0, y = existing.layout?.y || 0;
    return node.layout.x < x + other.width + 24 && node.layout.x + size.width + 24 > x && node.layout.y < y + other.height + 24 && node.layout.y + size.height + 24 > y;
  });
  while (overlaps()) {
    node.layout.x += size.width + 40;
    if (node.layout.x + size.width > right) { node.layout.x = left; node.layout.y += size.height + 40; }
  }
  if (await operate([{type: 'add_node', node}])) {
    $('#node-dialog').close(); form.reset(); showKindFields();
    if (node.layout.y + size.height > clientToWorld(rect.left, rect.bottom).y || node.layout.x + size.width > right) {
      store.view.x = viewport.clientWidth / 2 - (node.layout.x + size.width / 2) * store.view.scale;
      store.view.y = viewport.clientHeight / 2 - (node.layout.y + size.height / 2) * store.view.scale;
    }
    store.selected = {type: 'node', id: node.id}; renderAll(); notice('node placed');
  }
}

let noticeTimer;
function notice(message, error = false) {
  const target = $('#notice');
  target.textContent = message;
  target.className = `notice show ${error ? 'error' : ''}`;
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => { target.className = 'notice'; }, 3200);
}

boot().then(() => finishBoot()).catch(finishBoot);
