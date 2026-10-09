"use strict";
(() => {
  const {esc,ms,fmt,spanKey,edgeKey,patch,badge,label,friendlyAxis,pack,groupsFor,chartLayout,fileURL} = UI;
  const $ = id => document.getElementById(id), params = new URLSearchParams(location.search), parts = location.pathname.split('/').filter(Boolean);
  const allowedViews = ['graph','waterfall','timing','steps','context','files'];
  let runId = parts[0] === 'ui' && parts[1] ? decodeURIComponent(parts[1]) : null;
  let doc = null, record = null, hubMode = null, hubChecked = 0, online = false, lastSuccess = null, lastSeq = -1, lastRecord = '';
  let cursor = 0, lastEvent = null, eventError = '', following = !params.has('node'), initialized = false;
  const state = {view:allowedViews.includes(params.get('view')) ? params.get('view') : 'graph',group:'node_id',selected:null,node:params.get('node'),inspector:false,graphMode:'overview',zooms:{overview:.85,all:.85},filesQuery:'',contextQuery:''};
  const eventSpans = new Map(), streams = new Map(), gateKeys = new Set(), gateObservations = [], notes = new Map(), questionErrors = new Map(), answering = new Set();
  const evidence = new Map(), gateDocuments = new Map(), artifactCache = new Map();
  let topology = null, graphSource = null, graphError = '', graphLoading = false, graphTried = 0, sourceBox = null;
  let files = null, filesLoading = false, filesAt = 0, filesError = '', context = null, contextLoading = false, contextAt = 0, contextError = '', selectedArtifact = null;
  const api = path => '/pipelines/' + encodeURIComponent(runId) + path;
  const artifactURL = path => fileURL(api(''), path);
  const base = s => s.node_id + '@v' + (s.visit || 1) + '.a' + (s.attempt || 1);
  const parse = value => { if (typeof value !== 'string') return value; try { return JSON.parse(value); } catch (_) { return value; } };
  async function request(url, options = {}) {
    const response = await fetch(url, {...options,signal:AbortSignal.timeout(15000)});
    if (!response.ok) { const error = new Error(response.status + ' ' + await response.text()); error.status = response.status; throw error; }
    if (response.status === 204) return null;
    return UI.isJSON(response.headers.get('content-type')) ? response.json() : response.text();
  }
  function canAct() { return online && doc?.status === 'running' && !record?.archived && record?.reachable !== false; }
  function endTime() {
    if (ms(doc?.ended_at) !== null) return ms(doc.ended_at);
    if (canAct()) return Date.now();
    const observed = (doc?.spans || []).map(s => ms(s.ended_at) ?? ms(s.started_at)).filter(Number.isFinite);
    return ms(record?.last_seen) ?? lastEvent ?? (hubMode === false ? ms(lastSuccess) : observed.length ? Math.max(...observed) : null);
  }
  function spans() {
    return (doc?.spans || []).map(raw => {
      const s = {...raw,visit:raw.visit || 1,attempt:raw.attempt || 1};
      s.current = canAct() && !s.ended_at && s.outcome === 'running';
      s.is_gate = s.type === 'wait.human' || s.class === 'gate' || gateKeys.has(spanKey(s)) || gateObservations.some(e => e.node_id === s.node_id && e.visit === s.visit && e.ts >= ms(s.started_at) && e.ts <= (ms(s.ended_at) ?? endTime() ?? Infinity));
      return s;
    });
  }
  function spanState(s) {
    if (s.current) return {kind:s.is_gate ? 'waiting' : 'running',label:s.is_gate ? 'Waiting for review' : 'Running'};
    const labels = {success:'Completed',partial_success:'Partial success',fail:'Failed',retry:'Retrying',skipped:'Skipped'};
    return {kind:s.outcome === 'success' ? 'completed' : s.outcome === 'partial_success' ? 'completed' : s.outcome === 'fail' ? 'failed' : s.outcome === 'retry' ? 'waiting' : 'snapshot',label:labels[s.outcome] || 'Last recorded ' + (s.outcome || 'unknown')};
  }
  function duration(s) { return UI.duration(s,endTime()); }
  function latestFor(node) { return spans().filter(s => s.node_id === node).at(-1) || null; }
  function tokens(usage) {
    if (!usage || (!usage.input_tokens && !usage.output_tokens)) return '—';
    const k = n => n >= 1000 ? (n / 1000).toFixed(1) + 'k' : String(n || 0);
    return k(usage.input_tokens) + ' in / ' + k(usage.output_tokens) + ' out';
  }
  function stamp(value) { return ms(value) === null ? 'Time unknown' : new Date(value).toLocaleString(); }
  // Basic Markdown is rendered from escaped text only. Raw HTML stays inert.
  function readable(value) {
    const text = typeof value === 'string' ? value : JSON.stringify(value,null,2);
    const inline = line => esc(line).replace(/`([^`]+)`/g,'<code>$1</code>').replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>');
    const out = []; let code = null, list = null;
    const closeList = () => { if (list) { out.push('</' + list + '>'); list = null; } };
    for (const line of String(text ?? '').replaceAll('\r','').split('\n')) {
      if (/^\s*```/.test(line)) { closeList(); if (code !== null) { out.push('<pre><code>' + esc(code.join('\n')) + '</code></pre>'); code = null; } else code = []; continue; }
      if (code !== null) { code.push(line); continue; }
      const heading = line.match(/^(#{1,3})\s+(.+)/), bullet = line.match(/^\s*[-*]\s+(.+)/), numbered = line.match(/^\s*\d+[.)]\s+(.+)/);
      if (heading) { closeList(); out.push('<h' + heading[1].length + '>' + inline(heading[2]) + '</h' + heading[1].length + '>'); }
      else if (bullet || numbered) { const kind = bullet ? 'ul' : 'ol'; if (list !== kind) { closeList(); list = kind; out.push('<' + kind + '>'); } out.push('<li>' + inline((bullet || numbered)[1]) + '</li>'); }
      else { closeList(); out.push(line.trim() ? '<p>' + inline(line) + '</p>' : ''); }
    }
    closeList(); if (code !== null) out.push('<pre><code>' + esc(code.join('\n')) + '</code></pre>');
    return out.join('');
  }
  async function hubRecord(force = false) {
    if (hubMode === false || (!force && Date.now() - hubChecked < 2000)) return;
    hubChecked = Date.now();
    try {
      const rows = await request('/runs'); hubMode = true; record = (Array.isArray(rows) ? rows : []).find(r => r.run_id === runId) || {reachable:false};
      $('hub-link').hidden = false; $('breadcrumb').hidden = false;
    } catch (error) {
      if (error.status === 404) { hubMode = false; record = null; $('hub-link').hidden = true; $('breadcrumb').hidden = true; }
      else if (hubMode) record = {...record,reachable:false};
    }
  }
  async function drainEvents() {
    try {
      const text = await request(api('/events?since=' + cursor));
      if (typeof text !== 'string') return;
      let changed = false;
      for (const line of text.split('\n')) {
        if (!line.trim()) continue;
        let ev; try { ev = JSON.parse(line); } catch (_) { continue; }
        if (ev.seq && ev.seq <= cursor) continue;
        if (ev.seq) cursor = Math.max(cursor,ev.seq);
        const at = ms(ev.ts); if (at !== null) lastEvent = Math.max(lastEvent || 0,at);
        if (ev.kind === 'stage_started' && ev.node_id) eventSpans.set(ev.node_id,{node_id:ev.node_id,visit:ev.visit || 1,attempt:ev.attempt || 1});
        if (ev.kind === 'interview_started' && ev.node_id) {
          const identity = eventSpans.get(ev.node_id);
          if (identity) gateKeys.add(spanKey(identity));
          else gateObservations.push({node_id:ev.node_id,visit:ev.visit || 1,ts:at});
          changed = true;
        }
        if (ev.kind === 'stage_progress' && ev.detail?.kind === 'assistant_delta' && ev.node_id) {
          const identity = eventSpans.get(ev.node_id) || {node_id:ev.node_id,visit:ev.visit || 1,attempt:ev.attempt || 1};
          const key = spanKey(identity), previous = streams.get(key) || '', next = previous + (ev.message || '');
          streams.set(key,next.length > 100000 ? '… [earlier activity omitted]\n' + next.slice(-98000) : next); changed = true;
        }
        if (ev.kind === 'pipeline_started' && initialized && topology && at && at > (ms(doc?.started_at) || Infinity)) { graphTried = 0; }
      }
      eventError = ''; if (changed) { renderQuestions(); renderInspector(); if (['waterfall','timing','graph'].includes(state.view)) renderView(); }
    } catch (error) { eventError = 'Activity stream unavailable: ' + error.message; }
  }
  async function tick() {
    try {
      if (!runId) { const list = await request('/pipelines'); if (Array.isArray(list) && list.length) runId = list[0].run_id; }
      if (runId) {
        const next = await request(api('')); const wasOnline = online; online = true; lastSuccess = new Date().toISOString(); doc = next;
        await hubRecord();
        if (!initialized) {
          initialized = true;
          const ss = spans(), requested = state.node ? latestFor(state.node) : null, active = ss.filter(s => s.current && s.type === 'codergen').at(-1) || ss.find(s => s.current);
          const chosen = requested || (state.node ? null : active || ss.at(-1));
          if (chosen) { state.selected = spanKey(chosen); state.node = chosen.node_id; state.inspector = !!active || !!requested; }
        }
        if (following) {
          const active = spans().filter(s => s.current && s.type === 'codergen').at(-1);
          if (active && spanKey(active) !== state.selected) { state.selected = spanKey(active); state.node = active.node_id; state.inspector = true; selectedArtifact = null; }
        }
        const signature = JSON.stringify(record);
        if (!wasOnline || next.last_seq !== lastSeq || next.status === 'running' || signature !== lastRecord) render();
        lastRecord = signature;
        lastSeq = next.last_seq;
        await drainEvents();
      } else patch($('view'),'<div class="empty">No run is available yet. Retrying…</div>');
    } catch (error) {
      online = false;
      if (doc) render(); else patch($('view'),'<div class="notice error">Run unavailable: ' + esc(error.message) + '<br>Retrying…</div>');
    }
    setTimeout(tick,500);
  }
  function render() {
    if (!doc) return;
    $('run-id').textContent = runId; $('current-run-link').href = location.pathname;
    document.title = (doc.name || doc.graph_name || runId) + ' · attractor';
    const status = UI.runState({...record,status:doc.status,reachable:online && record?.reachable !== false},doc), end = endTime(), elapsed = ms(doc.started_at) === null || end === null ? 'Unknown' : fmt(end - ms(doc.started_at));
    patch($('run-heading'),`<div class="heading runheading"><div class="labelwrap"><div class="eyebrow">${esc(doc.graph_name || 'Pipeline')}</div><h1>${esc(doc.name || doc.goal || runId)}</h1><div class="runmeta"><code>${esc(runId)}</code><span>${spans().length} executions</span><span>${elapsed}${status.kind === 'snapshot' || status.kind === 'disconnected' ? ' · last observed' : ''}</span><span>${esc(tokens(doc.usage))} tokens</span></div></div>${badge(status.kind,status.label)}</div>`);
    $('connection-label').textContent = online ? record?.archived ? 'Archived record' : record?.reachable === false ? 'Last known record' : 'Connected' : 'Connection lost';
    $('connection-dot').classList.toggle('offline',!online || record?.reachable === false && !record?.archived);
    patch($('connection-notice'),!online ? '<div class="notice attention">Connection lost. Showing last fetched data; retrying…</div>' : record?.archived && doc.status === 'running' ? '<div class="notice">Archived snapshot. “Running” is its last recorded state; no live process is implied.</div>' : record?.reachable === false && !record?.archived ? '<div class="notice">Run connection lost. Showing the last recorded state.</div>' : '');
    $('failreason').hidden = !doc.failure_reason; $('failreason').textContent = doc.failure_reason || '';
    document.querySelectorAll('[data-view]').forEach(b => b.setAttribute('aria-pressed',String(b.dataset.view === state.view)));
    renderQuestions(); renderView(); renderInspector();
  }
  const questionKey = q => JSON.stringify([q.id,q.node_id,q.asked_at]);
  function reviewSubject(q) {
    const asked = ms(q.asked_at) ?? Infinity, incoming = topology?.edges.filter(e => e.to === q.node_id).map(e => e.from) || [];
    const candidates = spans().filter(s => s.type === 'codergen' && ms(s.ended_at) !== null && ms(s.ended_at) <= asked).sort((a,b) => ms(a.ended_at) - ms(b.ended_at));
    return candidates.filter(s => incoming.includes(s.node_id)).at(-1) || candidates.at(-1) || null;
  }
  async function canonical(subject) {
    const dir = base(subject);
    try {
      const status = parse(await request(artifactURL(dir + '/status.json'))), values = status?.context_updates || {};
      const text = values.plan_markdown || values['review.summary'] || values.review?.summary || values.last_response;
      if (text && String(text).trim()) return typeof text === 'string' ? text : JSON.stringify(text,null,2);
    } catch (_) {}
    try { const text = await request(artifactURL(dir + '/response.md')); if (typeof text === 'string' && text.trim()) return text; } catch (_) {}
    return '';
  }
  function paintReview(q, text) {
    const element = $('gate-doc-' + encodeURIComponent(questionKey(q))); if (!element || !text) return;
    const cap = 60000, shown = text.length > cap ? '… [showing last ' + cap.toLocaleString() + ' of ' + text.length.toLocaleString() + ' characters]\n\n' + text.slice(-cap) : text;
    patch(element,readable(shown));
  }
  function loadReviews() {
    if (!canAct()) return;
    for (const q of doc.pending_questions || []) {
      const subject = reviewSubject(q); if (!subject) continue;
      const key = spanKey(subject), cached = gateDocuments.get(key);
      if (cached?.text) { paintReview(q,cached.text); continue; }
      const stream = streams.get(key); if (stream) paintReview(q,stream);
      if (cached?.loading || Date.now() - (cached?.at || 0) < 2500) continue;
      gateDocuments.set(key,{loading:true,at:Date.now(),text:''});
      canonical(subject).then(text => {
        gateDocuments.set(key,{loading:false,at:Date.now(),text});
        if (text && canAct() && (doc.pending_questions || []).some(current => questionKey(current) === questionKey(q))) paintReview(q,text);
      });
    }
  }
  function renderQuestions() {
    if (!doc) return;
    const questions = doc.pending_questions || [];
    if (!canAct()) {
      patch($('questions'),questions.length ? '<div class="notice">' + questions.length + ' unanswered question(s) in the stored record. Live review actions are unavailable.</div>' : ''); return;
    }
    patch($('questions'),questions.map(q => {
      const key = questionKey(q), options = q.question?.options || [], busy = answering.has(key);
      return `<article class="question" data-key="question-${esc(key)}"><div class="eyebrow">Human review · ${esc(q.node_id || 'gate')}</div><h2>${esc(q.question?.text || q.message || q.id)}</h2><details data-key="review-${esc(key)}" open><summary>Work leading into this gate</summary><div class="review-document document" id="gate-doc-${encodeURIComponent(key)}" data-preserve><p class="muted">Waiting for the agent’s recorded output…</p></div></details><label for="note-${encodeURIComponent(key)}">Optional note to the agent</label><input id="note-${encodeURIComponent(key)}" data-note="${esc(key)}" value="${esc(notes.get(key) || '')}" placeholder="Add review feedback…"><div class="questionactions">${options.map(opt => `<button data-answer="${esc(key)}" data-value="${esc(opt.key)}" ${busy ? 'disabled' : ''}>${esc(opt.label?.startsWith('[') ? opt.label : '[' + opt.key + '] ' + (opt.label || opt.key))}</button>`).join('')}${!options.length ? '<span class="muted">No choices supplied by this gate.</span>' : ''}</div><p class="errorline" role="status">${esc(questionErrors.get(key) || (busy ? 'Submitting…' : ''))}</p></article>`;
    }).join(''));
    loadReviews();
  }
  async function answer(key,value) {
    if (!canAct() || answering.has(key)) return;
    const q = (doc.pending_questions || []).find(q => questionKey(q) === key); if (!q) return;
    answering.add(key); questionErrors.delete(key); renderQuestions();
    try {
      await request(api('/questions/' + encodeURIComponent(q.id) + '/answer'),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({value,note:notes.get(key) || ''})});
      doc = await request(api('')); render();
    } catch (error) { questionErrors.set(key,'Answer failed: ' + error.message); }
    finally { answering.delete(key); renderQuestions(); }
  }
  function choose(key, moveFocus = true) {
    const s = spans().find(s => spanKey(s) === key);
    following = false; state.selected = s ? key : null; if (s) state.node = s.node_id;
    state.inspector = true; selectedArtifact = null; renderInspector(); updateGraphSelection();
    document.querySelectorAll('[data-span]').forEach(b => { b.setAttribute('aria-pressed',String(b.dataset.span === key)); b.classList.toggle('selected',b.dataset.span === key); });
    if (moveFocus && innerWidth <= 1100) { $('inspector').scrollIntoView({block:'start',behavior:'smooth'}); $('inspector').focus({preventScroll:true}); }
  }
  function chooseNode(node, moveFocus = true) { state.node = node; const s = latestFor(node); choose(s ? spanKey(s) : null,moveFocus); }
  function closeInspector() {
    following = false; state.inspector = false; selectedArtifact = null; renderInspector(); document.querySelector('[data-view][aria-pressed="true"]')?.focus();
  }
  function renderView() {
    if (!doc) return;
    if (state.view === 'graph') renderGraph();
    else if (state.view === 'waterfall' || state.view === 'timing') renderTimeline();
    else if (state.view === 'steps') renderSteps();
    else if (state.view === 'files') renderFiles();
    else renderContext();
  }
  function renderSteps() {
    const glyph = s => s.is_gate ? '◇' : s.current ? '●' : s.outcome === 'fail' ? '✗' : s.outcome === 'retry' ? '↻' : s.outcome === 'success' ? '✓' : '○';
    patch($('view'),`<h2>Recorded executions</h2><p class="note">Chronological records. Order alone does not establish a dependency.</p><div class="stepgroup">${spans().map(s => `<button class="step ${s.is_gate ? 'gate' : ''} ${s.current ? 'active' : s.outcome === 'fail' ? 'failed' : s.outcome === 'retry' ? 'retry' : ''}" data-key="step-${esc(spanKey(s))}" data-span="${esc(spanKey(s))}" aria-pressed="${spanKey(s) === state.selected}"><span class="stepicon" aria-hidden="true">${glyph(s)}</span><span class="steplabel">${esc(label(s.node_id))}<span class="visit">v${s.visit} · a${s.attempt}${s.is_gate ? ' · gate' : ''}</span></span><span class="steptime">${fmt(duration(s))}</span></button>`).join('') || '<div class="empty">No execution has been recorded yet.</div>'}</div>`);
  }
  function renderTimeline() {
    const ss = spans(), timed = state.view === 'timing', valid = ss.filter(s => ms(s.started_at) !== null);
    if (!valid.length) { patch($('view'),'<div class="empty">No timed executions yet.</div>'); return; }
    const start = Math.min(...valid.map(s => ms(s.started_at))), end = Math.max(start,endTime() ?? start), axis = friendlyAxis(valid.map(s => ms(s.ended_at) === null && !Number.isFinite(endTime()) ? {...s,ended_at:s.started_at} : s),end);
    const scale = 1200 / Math.max(1,end - start), at = t => 16 + (timed ? (t - start) * scale : axis.at(t)), width = timed ? 1290 : axis.width + 32;
    const lanes = new Map();
    for (const s of valid) { const group = s[state.group] || s.node_id; if (!lanes.has(group)) lanes.set(group,[]); lanes.get(group).push(s); }
    const maxTokens = Math.max(1,...[...lanes.values()].map(rows => rows.reduce((n,s) => n + (s.input_tokens || 0),0)));
    const data = [...lanes].map(([name,rows]) => {
      const items = pack(rows.map(s => { const x = at(ms(s.started_at)), end = ms(s.ended_at) ?? endTime() ?? ms(s.started_at), w = timed ? Math.max(1,at(end) - x) : s.is_gate ? 44 : Math.max(44,at(end) - x); return {s,x,w}; }));
      return {name,rows,items,height:Math.max(52,(Math.max(...items.map(i => i.slot)) + 1) * 52)};
    });
    const selected = spans().find(s => spanKey(s) === state.selected);
    patch($('view'),`<div class="sectiontitle"><h2>${timed ? 'Elapsed time' : 'Execution flow'}</h2><label class="muted" for="groupby">Lanes <select id="groupby">${[['node_id','Node'],['thread_id','Thread'],['class','Class'],['type','Type']].map(([key,name]) => `<option value="${key}" ${state.group === key ? 'selected' : ''}>${name}</option>`).join('')}</select></label></div><p class="note">${timed ? 'Timestamp-proportional bars, including gate waits. Subpixel spans have a 1px visibility floor.' : 'Shared compressed axis · compact gates · crowded targets stack within their lane'}</p><button id="jumpStep" ${selected ? '' : 'disabled'} style="margin-bottom:12px">Jump to selected step</button><div class="timeline" id="timeline" aria-label="${timed ? 'Accurate timing' : 'Readable waterfall'}"><div class="timelineinner" style="--lane-width:180px;--plot-width:${width}px"><div class="timelinehead"><div class="tlabel"><span>${timed ? 'Elapsed' : 'Readable spacing'}</span></div><div class="ruler">${timed ? [0,1,2,3,4].map(i => `<span style="left:${16 + i * 300}px">${fmt((end - start) * i / 4)}</span>`).join('') : '<span style="left:16px">Shared sequence axis · spacing is compressed</span>'}</div></div>${data.map(lane => `<div class="trow" style="height:${lane.height}px" data-key="lane-${esc(lane.name)}" data-lane="${esc(lane.name)}"><div class="tlabel" title="${esc(lane.name)}"><span>${esc(state.group === 'node_id' ? label(lane.name) : lane.name)}${lane.height > 52 ? `<small class="lane-stacks">${lane.height / 52} display rows</small>` : ''}<span class="tokenline" title="${lane.rows.reduce((n,s) => n + (s.input_tokens || 0),0)} input tokens"><i style="width:${100 * lane.rows.reduce((n,s) => n + (s.input_tokens || 0),0) / maxTokens}%"></i></span></span></div><div class="track" style="height:${lane.height}px">${lane.items.map(({s,x,w,slot}) => {
      const identity = spanKey(s), status = spanState(s), calls = s.tool_calls || [];
      return `<button class="bar ${s.is_gate ? 'gate' : ''} ${status.kind === 'completed' ? 'complete' : status.kind === 'failed' ? 'failed' : ''} ${timed ? 'timed' : ''} ${identity === state.selected ? 'selected' : ''}" style="left:${x}px;width:${w}px;top:${(timed ? 14 : 8) + slot * 52}px" data-key="bar-${esc(identity)}" data-span="${esc(identity)}" data-stack="${slot}" aria-pressed="${identity === state.selected}" aria-label="${esc(s.node_id + ', visit ' + s.visit + ', attempt ' + s.attempt + ', ' + status.label + ', duration ' + fmt(duration(s)))}" title="${esc(s.node_id + ' · v' + s.visit + ' a' + s.attempt + ' · ' + status.label + ' · ' + fmt(duration(s)))}">${esc(timed ? '' : state.group === 'node_id' ? UI.barText(s,endTime()) : label(s.node_id) + ' · ' + UI.barText(s,endTime()))}${calls.map(t => { const when = ms(t.at); if (when === null) return ''; const offset = Math.max(0,Math.min(w - 2,at(when) - x)); return `<span class="tooltick ${t.failed ? 'failed' : ''}" style="left:${offset}px" aria-hidden="true" title="${esc(t.title || 'Tool call')}"></span>`; }).join('')}</button>`;
    }).join('')}</div></div>`).join('')}</div></div><p class="note">${timed ? 'Select very short executions from Steps. Original timestamps and inspected durations are unchanged.' : 'Display rows separate targets; they do not imply extra threads. Parallel starts stay aligned. Gate wait durations remain in the inspector and Accurate timing.'}${state.group !== 'node_id' ? ' Missing grouping metadata falls back to each node.' : ''}</p>`);
  }
  let chartRuntime = null;
  async function loadGraph(force = false) {
    if (graphLoading || topology && !force || !force && Date.now() - graphTried < 5000) return;
    graphLoading = true; graphTried = Date.now();
    try {
      const text = await request(api('/graph')), xml = new DOMParser().parseFromString(String(text),'image/svg+xml');
      if (xml.documentElement.localName !== 'svg' || xml.querySelector('parsererror')) throw new Error('Graph response is not valid SVG');
      const nodes = [...xml.querySelectorAll('g.node')].map(n => ({id:n.querySelector('title')?.textContent})).filter(n => n.id), ids = new Set(nodes.map(n => n.id));
      if (!nodes.length) throw new Error('No graph node IDs were supplied');
      const edges = []; let omitted = 0;
      for (const group of xml.querySelectorAll('g.edge')) {
        const title = group.querySelector('title')?.textContent || '';
        const from = [...ids].sort((a,b) => b.length - a.length).find(id => title.startsWith(id + '->') && ids.has(title.slice(id.length + 2)));
        if (!from) { omitted++; continue; }
        edges.push({from,to:title.slice(from.length + 2),label:[...group.querySelectorAll('text')].map(t => t.textContent).join(' ')});
      }
      graphSource = String(text); topology = {nodes,edges,omitted}; graphError = '';
      if (doc.graph_name !== 'plan_build_review' && state.graphMode === 'overview') state.graphMode = 'all';
    } catch (error) { graphError = 'Graph unavailable: ' + error.message; }
    finally { graphLoading = false; if (state.view === 'graph') renderGraph(); loadReviews(); }
  }
  function groupState(group) {
    const ss = spans().filter(s => group.members.includes(s.node_id)), current = ss.filter(s => s.current), latest = ss.at(-1);
    const byNode = new Map(ss.map(s => [s.node_id,s])), visited = byNode.size;
    if (current.length) return {kind:'live',icon:'●',text:current.some(s => s.is_gate) ? 'Waiting for review' : 'Running',pick:current.at(-1)};
    if (!latest) return {kind:'future',icon:'○',text:'Not yet recorded',pick:null};
    const failed = [...byNode.values()].find(s => s.outcome === 'fail'), done = visited === group.members.length && [...byNode.values()].every(s => s.ended_at && ['success','partial_success','skipped'].includes(s.outcome));
    return {kind:failed ? 'failed' : done ? 'done' : 'partial',icon:failed ? '✗' : latest.is_gate ? '◇' : done ? '✓' : '◐',text:group.members.length > 1 ? visited + '/' + group.members.length + ' nodes visited' : 'Visit ' + latest.visit + ' · ' + spanState(latest).label.toLowerCase(),pick:failed || latest};
  }
  function modes() {
    return `<div class="chartmode" aria-label="Graph detail">${[['overview','Overview'],['all','All steps'],['source','Source SVG']].filter(([id]) => id !== 'overview' || doc.graph_name === 'plan_build_review').map(([id,name]) => `<button data-mode="${id}" aria-pressed="${state.graphMode === id}">${name}</button>`).join('')}</div>`;
  }
  function controls(source = false) {
    const active = spans().some(s => s.current);
    return `<div class="graphcontrols"><button data-chart="in">Zoom in</button><button data-chart="out">Zoom out</button><button data-chart="fit">${source ? 'Fit graph' : 'Fit chart'}</button><button data-chart="active">${active ? 'Focus active' : 'Focus latest'}</button><button data-chart="selected" ${state.node ? '' : 'disabled'}>Focus selected</button>${source ? '' : `<output id="zoom-value" aria-live="polite">${Math.round((state.zooms[state.graphMode] || .85) * 100)}%</output>`}</div>`;
  }
  function outline(data) {
    const query = state.graphQuery || '';
    return `<details class="filegroup" data-key="graph-search"><summary>Find a step</summary><input class="search" id="graph-search" type="search" aria-label="Search graph steps" placeholder="Search node names…" value="${esc(query)}" style="width:100%"><div class="graphoutline" id="graph-outline">${data.groups.filter(g => (g.name + ' ' + g.members.join(' ')).toLocaleLowerCase().includes(query.toLocaleLowerCase())).map(g => `<button data-find="${esc(g.id)}" data-key="find-${esc(g.id)}">${esc(g.name)} <small>· ${g.members.length} node${g.members.length === 1 ? '' : 's'}</small></button>`).join('')}</div></details>`;
  }
  function renderGraph() {
    if (!topology) {
      patch($('view'),`<h2>Pipeline dependencies</h2><div class="empty">${esc(graphError || 'Loading graph…')}<br><button data-retry-graph style="margin-top:12px">Retry graph</button> <button data-view="steps">Show recorded steps</button></div>`); loadGraph(); return;
    }
    if (state.graphMode === 'source') { renderSource(); return; }
    const data = groupsFor(topology,doc.graph_name,state.graphMode === 'overview'), layout = chartLayout(data), {pos,ranks,back,width,height} = layout, zoom = state.zooms[state.graphMode] || .85;
    chartRuntime = {data,layout}; let rail = 0;
    const paths = data.edges.map(e => {
      const a = pos.get(e.from), b = pos.get(e.to), feedback = back.has(edgeKey(e)), skip = !feedback && ranks.get(e.to) - ranks.get(e.from) > 1;
      const sx = a.x + a.w, sy = a.y + a.h / 2, tx = b.x, ty = b.y + b.h / 2; let d;
      if (!feedback && !skip) { const m = (sx + tx) / 2; d = `M${sx},${sy} C${m},${sy} ${m},${ty} ${tx},${ty}`; }
      else { const level = 16 + rail++ * 8; d = `M${sx},${sy} H${sx + 11} V${level} H${tx - 11} V${ty} H${tx}`; }
      return `<path class="${feedback ? 'return' : ''}" d="${d}" marker-end="url(#jobarrow)"><title>${esc(e.from + ' → ' + e.to + (e.labels.length ? ' · ' + e.labels.join(' / ') : ''))}</title></path>`;
    }).join('');
    patch($('view'),`<div class="chartintro"><h2>Pipeline ${data.overview ? 'overview' : 'steps'}</h2><small>${data.overview ? data.groups.length + ' groups · ' + data.nodeCount + ' underlying nodes' : data.nodeCount + ' nodes · ' + data.edgeCount + ' dependencies'}</small></div><p class="note">${data.overview ? 'Compact milestones with related nodes folded into groups. All steps shows every dependency.' : 'Every node and dependency, with fixed-size job cards.'}</p>${modes()}${controls()}${topology.omitted ? '<div class="notice">Some connections could not be decoded. Source SVG retains the original graph.</div>' : ''}<div class="jobviewport" id="jobviewport" aria-label="Job graph; scroll to explore"><div class="jobstage" id="jobstage" style="width:${width * zoom}px;height:${height * zoom}px"><div class="jobworld" id="jobworld" style="width:${width}px;height:${height}px;transform:scale(${zoom})"><svg class="jobedges" width="${width}" height="${height}" aria-hidden="true"><defs><marker id="jobarrow" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path class="arrowhead" d="M0 0 L7 3.5 L0 7 Z"/></marker></defs>${paths}</svg>${data.groups.map(g => {
      const p = pos.get(g.id), status = groupState(g), gate = g.id === 'gate' || g.id === 'ship' || g.members.length === 1 && spans().some(s => s.node_id === g.members[0] && s.is_gate), selected = g.members.includes(state.node);
      return `<button class="jobcard ${status.kind} ${gate ? 'gate' : ''} ${selected ? 'selected' : ''}" style="left:${p.x}px;top:${p.y}px" data-key="job-${esc(g.id)}" data-job="${esc(g.id)}" aria-pressed="${selected}" aria-label="${esc(g.name + ', ' + status.text + ', ' + g.members.length + ' nodes')}" title="${esc(g.name)}"><span class="jobicon" aria-hidden="true">${gate ? '◇' : status.icon}</span><span class="jobtext"><strong>${esc(g.name)}</strong><small>${esc(status.text)}</small><small>${g.members.length > 1 ? g.members.length + ' nodes · open details' : esc(g.members[0])}</small></span></button>`;
    }).join('')}</div></div></div><div class="chartlegend"><span><i></i>Dependency</span><span><i class="loopkey"></i>Return connection</span><span>Statuses describe recorded nodes, not traversed edges.</span></div>${outline(data)}`);
  }
  function chartZoom(value) {
    if (!chartRuntime || !$('jobworld')) return;
    const zoom = Math.max(.04,Math.min(1.6,value)); state.zooms[state.graphMode] = zoom;
    $('jobworld').style.transform = 'scale(' + zoom + ')'; $('jobstage').style.width = chartRuntime.layout.width * zoom + 'px'; $('jobstage').style.height = chartRuntime.layout.height * zoom + 'px'; $('zoom-value').textContent = Math.round(zoom * 100) + '%';
  }
  function focusGroups(ids) {
    if (!chartRuntime || !$('jobviewport')) return;
    const ps = ids.map(id => chartRuntime.layout.pos.get(id)).filter(Boolean); if (!ps.length) return;
    const viewport = $('jobviewport'), low = Math.min(...ps.map(p => p.x)), high = Math.max(...ps.map(p => p.x + p.w));
    if (ids.length > 1) chartZoom(Math.min(.95,(viewport.clientWidth - 40) / (high - low)));
    else if ((state.zooms[state.graphMode] || .85) < .7) chartZoom(.85);
    const top = Math.min(...ps.map(p => p.y)), bottom = Math.max(...ps.map(p => p.y + p.h)), zoom = state.zooms[state.graphMode] || .85;
    viewport.scrollTo({left:(low + high) / 2 * zoom - viewport.clientWidth / 2,top:(top + bottom) / 2 * zoom - viewport.clientHeight / 2,behavior:'smooth'});
  }
  function graphAction(action) {
    if (state.graphMode === 'source') { sourceAction(action); return; }
    if (!chartRuntime) return;
    const {data,layout} = chartRuntime, viewport = $('jobviewport'), zoom = state.zooms[state.graphMode] || .85;
    if (action === 'in') chartZoom(zoom * 1.2);
    if (action === 'out') chartZoom(zoom / 1.2);
    if (action === 'fit') { chartZoom(Math.min((viewport.clientWidth - 20) / layout.width,(viewport.clientHeight - 20) / layout.height,1)); viewport.scrollTo(0,0); }
    if (action === 'selected') focusGroups(data.groups.filter(g => g.members.includes(state.node)).map(g => g.id));
    if (action === 'active') { const active = data.groups.filter(g => spans().some(s => s.current && g.members.includes(s.node_id))); focusGroups((active.length ? active : data.groups.filter(g => g.members.includes(spans().at(-1)?.node_id))).map(g => g.id)); }
  }
  function updateGraphSelection() {
    document.querySelectorAll('.jobcard[data-job]').forEach(button => {
      const group = chartRuntime?.data.groups.find(g => g.id === button.dataset.job), selected = !!group?.members.includes(state.node);
      button.classList.toggle('selected',selected); button.setAttribute('aria-pressed',String(selected));
    });
    document.querySelectorAll('#source-graph g.node').forEach(node => {
      const s = latestFor(node.dataset.sourceNode); node.classList.toggle('selected',node.dataset.sourceNode === state.node); node.setAttribute('aria-pressed',String(node.dataset.sourceNode === state.node));
      node.classList.toggle('live',!!s?.current); node.classList.toggle('done',!!s?.ended_at && ['success','partial_success'].includes(s.outcome)); node.classList.toggle('failed',s?.outcome === 'fail');
    });
  }
  function renderSource() {
    const data = groupsFor(topology,doc.graph_name,false);
    patch($('view'),`<h2>Original graph</h2><p class="note">Original layout with selectable nodes, zoom and focus.</p>${modes()}${controls(true)}<div class="graphbox" id="source-graph" data-preserve aria-label="Pipeline graph. Drag empty space to pan."></div>${outline(data)}`);
    if (!$('source-graph').childElementCount) {
      const xml = new DOMParser().parseFromString(graphSource,'image/svg+xml');
      const tags = new Set(['svg','g','title','desc','polygon','polyline','path','ellipse','circle','rect','line','text','tspan']);
      const attrs = new Set(['id','class','viewBox','width','height','xmlns','version','transform','points','d','cx','cy','rx','ry','r','x','y','x1','x2','y1','y2','fill','stroke','stroke-width','text-anchor','font-family','font-size']);
      function clean(node) {
        if (node.nodeType === 3) return document.createTextNode(node.textContent);
        if (node.nodeType !== 1 || !tags.has(node.localName)) return null;
        const element = document.createElementNS('http://www.w3.org/2000/svg',node.localName);
        for (const attr of node.attributes) if (attrs.has(attr.name) && !/(url\s*\(|javascript:|data:)/i.test(attr.value)) element.setAttribute(attr.name,attr.value);
        for (const child of node.childNodes) { const next = clean(child); if (next) element.appendChild(next); }
        return element;
      }
      const svg = clean(xml.documentElement); $('source-graph').appendChild(svg);
      for (const node of svg.querySelectorAll('g.node')) { const id = node.querySelector('title')?.textContent; node.dataset.sourceNode = id || ''; node.setAttribute('tabindex','0'); node.setAttribute('role','button'); node.setAttribute('aria-label',id || 'Graph node'); }
      if (sourceBox) svg.setAttribute('viewBox',sourceBox.join(' '));
      let drag = null;
      $('source-graph').addEventListener('pointerdown',event => { if (event.target.closest('g.node')) return; const matrix = svg.getScreenCTM(); if (!matrix) return; drag = {x:event.clientX,y:event.clientY,box:svg.getAttribute('viewBox').split(/[ ,]+/).map(Number),matrix:matrix.inverse()}; $('source-graph').setPointerCapture(event.pointerId); });
      $('source-graph').addEventListener('pointermove',event => { if (!drag) return; const a = new DOMPoint(drag.x,drag.y).matrixTransform(drag.matrix), b = new DOMPoint(event.clientX,event.clientY).matrixTransform(drag.matrix); sourceBox = [drag.box[0] + a.x - b.x,drag.box[1] + a.y - b.y,drag.box[2],drag.box[3]]; svg.setAttribute('viewBox',sourceBox.join(' ')); });
      $('source-graph').addEventListener('pointerup',() => drag = null); $('source-graph').addEventListener('pointercancel',() => drag = null);
    }
    updateGraphSelection();
  }
  function sourceAction(action) {
    const svg = document.querySelector('#source-graph svg'); if (!svg) return;
    let box = svg.getAttribute('viewBox').split(/[ ,]+/).map(Number);
    if (action === 'fit') { const original = new DOMParser().parseFromString(graphSource,'image/svg+xml').documentElement.getAttribute('viewBox'); sourceBox = original.split(/[ ,]+/).map(Number); }
    else if (action === 'in' || action === 'out') { const factor = action === 'in' ? .75 : 1.33; sourceBox = [box[0] + box[2] * (1 - factor) / 2,box[1] + box[3] * (1 - factor) / 2,box[2] * factor,box[3] * factor]; }
    else {
      const active = spans().filter(s => s.current).map(s => s.node_id), ids = action === 'selected' ? [state.node] : active.length ? active : [spans().at(-1)?.node_id];
      const nodes = [...svg.querySelectorAll('g.node')].filter(n => ids.includes(n.dataset.sourceNode)), matrix = svg.getScreenCTM(); if (!nodes.length || !matrix) return;
      const points = nodes.flatMap(n => { const b = n.getBBox(), m = matrix.inverse().multiply(n.getScreenCTM()); return [[b.x,b.y],[b.x+b.width,b.y],[b.x,b.y+b.height],[b.x+b.width,b.y+b.height]].map(([x,y]) => new DOMPoint(x,y).matrixTransform(m)); });
      const x = Math.min(...points.map(p => p.x)), y = Math.min(...points.map(p => p.y)), w = Math.max(...points.map(p => p.x)) - x, h = Math.max(...points.map(p => p.y)) - y, width = Math.max(500,w+160), height = Math.max(380,h+200);
      sourceBox = [x+w/2-width/2,y+h/2-height/2,width,height];
    }
    if (sourceBox) svg.setAttribute('viewBox',sourceBox.join(' '));
  }
  async function artifact(path, refresh = false) {
    const old = artifactCache.get(path);
    if (old?.promise) return old.promise;
    if (old && (!refresh || Date.now() - old.at < 2000)) {
      if (old.error) throw old.error; return old.value;
    }
    const promise = request(artifactURL(path)).then(value => {
      artifactCache.set(path,{value,at:Date.now()}); return value;
    },error => { artifactCache.set(path,{error,at:Date.now()}); throw error; });
    artifactCache.set(path,{promise}); return promise;
  }
  async function loadFiles(force = false) {
    if (filesLoading || !force && Date.now() - filesAt < (canAct() ? 3000 : 60000)) return;
    filesLoading = true; filesAt = Date.now();
    try { const value = await request(api('/artifacts')); files = Array.isArray(value) ? value : []; filesError = ''; }
    catch (error) { filesError = 'Files unavailable: ' + error.message; }
    finally { filesLoading = false; if (state.view === 'files') renderFiles(); if (state.inspector) renderInspector(); }
  }
  function evidenceBlock(path,title,open = false) {
    return `<details class="evidence" data-key="evidence-${esc(path)}" data-artifact="${esc(path)}" ${open ? 'open' : ''}><summary>${esc(title)}</summary><a class="rawlink" href="${esc(artifactURL(path))}" target="_blank" rel="noopener">Open original file ↗</a><div class="document evidencebody" data-key="body-${esc(path)}" data-preserve><p class="muted">Loading file…</p></div></details>`;
  }
  function loadVisibleEvidence() {
    for (const element of document.querySelectorAll('details[data-artifact][open]')) {
      const path = element.dataset.artifact, body = element.querySelector('.evidencebody');
      if (!body) continue;
      artifact(path,canAct()).then(value => {
        if (!element.isConnected || element.dataset.artifact !== path) return;
        const raw = typeof value === 'string' ? value : JSON.stringify(value,null,2), limit = 100000;
        const shown = raw.length > limit ? raw.slice(0,limit) + '\n… [preview truncated; open original file for the full content]' : raw;
        patch(body,/\.(md|txt)$/.test(path) ? readable(shown) : '<pre>' + esc(shown) + '</pre>');
      },error => { if (element.isConnected) patch(body,'<p class="errorline">' + esc('File unavailable: ' + error.message) + '</p>'); });
    }
  }
  function renderInspector() {
    const host = $('inspector'); if (!doc || !host) return;
    host.hidden = !state.inspector; $('run-layout').classList.toggle('noinspector',!state.inspector);
    if (!state.inspector) return;
    const selected = spans().find(s => spanKey(s) === state.selected), node = state.node;
    if (!node) { patch(host,'<button class="close" data-close-inspector aria-label="Close inspector">×</button><h2>Execution details</h2><p class="muted">Select a graph node or execution.</p>'); return; }
    const siblings = spans().filter(s => s.node_id === node), group = state.view === 'graph' && state.graphMode === 'overview' ? chartRuntime?.data.groups.find(g => g.members.includes(node)) : null;
    const memberList = group?.members.length > 1 ? `<details class="filegroup" data-key="members-${esc(group.id)}" open><summary>${esc(group.name)} · ${group.members.length} nodes</summary><div class="graphoutline">${group.members.map(id => `<button data-member="${esc(id)}" aria-pressed="${id === node}">${esc(label(id))} <small>· ${latestFor(id) ? spanState(latestFor(id)).label : 'Not yet recorded'}</small></button>`).join('')}</div></details>` : '';
    const picker = siblings.length > 1 ? `<label for="execution-picker">Execution <select id="execution-picker">${siblings.map(s => `<option value="${esc(spanKey(s))}" ${spanKey(s) === state.selected ? 'selected' : ''}>Visit ${s.visit} · attempt ${s.attempt} · ${spanState(s).label}</option>`).join('')}</select></label>` : '';
    let body = '<div class="empty">This node has not recorded an execution yet.</div>';
    if (selected) {
      const s = selected, status = spanState(s), dir = base(s), stream = streams.get(spanKey(s)), cached = evidence.get(spanKey(s));
      const paths = (files || []).filter(p => p.startsWith(dir + '/'));
      const extra = paths.filter(p => p.slice(dir.length+1) !== 'status.json');
      body = `<div class="inspectmeta">${badge(status.kind,status.label)}<span>v${s.visit} · a${s.attempt} · ${fmt(duration(s))}</span></div>${picker}<dl class="facts"><dt>Node</dt><dd><code>${esc(s.node_id)}</code></dd><dt>Type</dt><dd>${esc(s.type || 'Not recorded')}</dd><dt>Model</dt><dd>${esc(s.model || 'Not recorded')}</dd><dt>Thread</dt><dd>${esc(s.thread_id || 'Not recorded')}</dd><dt>Tokens</dt><dd>${esc(tokens({input_tokens:s.input_tokens,output_tokens:s.output_tokens}))}</dd><dt>Started</dt><dd>${esc(stamp(s.started_at))}</dd><dt>Finished</dt><dd>${ms(s.ended_at) === null ? s.current ? 'In progress' : 'Not recorded' : esc(stamp(s.ended_at))}</dd></dl>${s.detail ? '<div class="notice error">' + esc(s.detail) + '</div>' : ''}<details class="evidence" data-key="output-${esc(spanKey(s))}" open><summary>Recorded output</summary><div class="document responsebody" id="selected-output" data-key="output-body-${esc(spanKey(s))}" data-preserve>${cached?.text ? readable(cached.text) : '<p class="muted">Loading recorded output…</p>'}</div></details>${stream || s.current && s.type === 'codergen' ? `<details class="evidence" data-key="activity-${esc(spanKey(s))}" open><summary>Agent activity${following ? ' · following current execution' : ''}</summary><pre class="activity" id="selected-activity" data-key="stream-${esc(spanKey(s))}" data-preserve>${esc(stream || 'Waiting for activity…')}</pre>${eventError ? '<p class="errorline">' + esc(eventError) + '</p>' : ''}</details>` : ''}<details class="filegroup" data-key="raw-${esc(spanKey(s))}"><summary>Execution record and files · ${paths.length}</summary><pre>${esc(JSON.stringify(s,null,2))}</pre>${evidenceBlock(dir + '/status.json','Status JSON')}${extra.map(p => evidenceBlock(p,p.slice(dir.length+1))).join('')}${filesError ? '<p class="errorline">' + esc(filesError) + '</p>' : ''}<button data-view="files">Browse all run files</button></details>`;
    }
    patch(host,`<button class="close" data-close-inspector aria-label="Close inspector">×</button><div class="eyebrow">Execution inspector</div><h2>${esc(label(node))}</h2>${memberList}${body}`);
    if (selected) {
      const key = spanKey(selected), element = $('selected-activity'), text = streams.get(key);
      if (element && text && element.textContent !== text) { const bottom = element.scrollHeight - element.scrollTop - element.clientHeight < 40; element.textContent = text; if (bottom) element.scrollTop = element.scrollHeight; }
      loadOutput(selected);
    }
    loadFiles(); loadVisibleEvidence();
  }
  function loadOutput(s) {
    const key = spanKey(s), old = evidence.get(key);
    if (old?.loading || old?.text && !s.current || Date.now() - (old?.at || 0) < 2500) return;
    evidence.set(key,{...old,loading:true,at:Date.now()});
    (async () => {
      let text = await canonical(s);
      if (!text && s.type === 'tool') {
        try { const value = await artifact(base(s) + '/stdout.txt',s.current); text = typeof value === 'string' ? value : JSON.stringify(value,null,2); } catch (_) {}
      }
      if (!text) text = streams.get(key) || '';
      const limit = 100000; if (text.length > limit) text = text.slice(0,limit) + '\n… [preview truncated; full output is available in Files]';
      evidence.set(key,{text,at:Date.now(),loading:false});
      if (state.selected !== key || !state.inspector) return;
      const element = $('selected-output'); if (element) patch(element,text ? readable(text) : '<p class="muted">No recorded output is available yet. Browse execution files for raw details.</p>');
    })();
  }
  function renderFiles() {
    const query = state.filesQuery.toLocaleLowerCase(), groups = new Map();
    for (const path of files || []) {
      if (query && !path.toLocaleLowerCase().includes(query)) continue;
      const dir = path.includes('/') ? path.split('/')[0] : 'Run metadata';
      if (!groups.has(dir)) groups.set(dir,[]); groups.get(dir).push(path);
    }
    patch($('view'),`<h2>Run files</h2><p class="note">Original artifacts grouped by execution. Previews load when opened.</p><div class="filetools"><input type="search" class="search" id="files-search" aria-label="Search files" placeholder="Search file paths…" value="${esc(state.filesQuery)}"><button data-refresh-files>Refresh files</button></div>${filesError ? '<div class="notice error">' + esc(filesError) + '</div>' : ''}<p class="filesummary">${files ? files.length + ' files · ' + [...groups.values()].reduce((n,g) => n+g.length,0) + ' shown' : 'Loading files…'}</p>${[...groups].map(([dir,paths]) => `<details class="filegroup" data-key="files-${esc(dir)}" ${query ? 'open' : ''}><summary>${esc(dir)} <small>· ${paths.length} files</small></summary>${paths.map(p => evidenceBlock(p,p === dir ? p : p.slice(p.indexOf('/')+1))).join('')}</details>`).join('') || (files ? '<div class="empty">No matching files.</div>' : '')}`);
    loadFiles(); loadVisibleEvidence();
  }
  async function loadContext(force = false) {
    if (contextLoading || !force && Date.now() - contextAt < (canAct() ? 3000 : 60000)) return;
    contextLoading = true; contextAt = Date.now();
    try { const checkpoint = parse(await request(artifactURL('checkpoint.json'))); context = checkpoint?.context || {}; contextError = ''; }
    catch (error) { contextError = error.status === 404 ? 'No checkpoint is available yet. Context is saved after a completed node.' : 'Context unavailable: ' + error.message; }
    finally { contextLoading = false; if (state.view === 'context') renderContext(); }
  }
  function renderContext() {
    const query = state.contextQuery.toLocaleLowerCase(), entries = Object.entries(context || {}).filter(([key,value]) => (key + ' ' + JSON.stringify(value)).toLocaleLowerCase().includes(query));
    patch($('view'),`<h2>Run context</h2><p class="note">Context from the last saved checkpoint. A running node may have newer activity.</p><div class="contexttools"><input type="search" class="search" id="context-search" aria-label="Search context" placeholder="Search keys and values…" value="${esc(state.contextQuery)}"><button data-refresh-context>Refresh context</button><a href="${esc(artifactURL('checkpoint.json'))}" target="_blank" rel="noopener">Original checkpoint ↗</a></div>${contextError ? '<div class="notice">' + esc(contextError) + '</div>' : ''}<p class="filesummary">${context ? entries.length + ' of ' + Object.keys(context).length + ' values' : contextError ? '' : 'Loading checkpoint…'}</p>${entries.map(([key,value]) => `<details class="contextrow" data-key="context-${esc(key)}"><summary><code>${esc(key)}</code><small>${typeof value === 'string' ? value.length + ' characters' : Array.isArray(value) ? value.length + ' items' : typeof value}</small></summary><div class="document">${typeof value === 'string' ? readable(value) : '<pre>' + esc(JSON.stringify(value,null,2)) + '</pre>'}</div></details>`).join('')}${context && !entries.length ? '<div class="empty">No matching context values.</div>' : ''}`);
    loadContext();
  }
  document.addEventListener('click',event => {
    const button = event.target.closest('button');
    const source = event.target.closest('g.node[data-source-node]');
    if (source) { chooseNode(source.dataset.sourceNode); return; }
    if (!button) return;
    if (button.dataset.view) {
      state.view = button.dataset.view; const url = new URL(location.href); url.searchParams.set('view',state.view); history.replaceState(null,'',url); render(); return;
    }
    if (button.hasAttribute('data-close-inspector')) { closeInspector(); return; }
    if (button.dataset.span) { choose(button.dataset.span); return; }
    if (button.dataset.member) { chooseNode(button.dataset.member); return; }
    if (button.dataset.mode) { state.graphMode = button.dataset.mode; renderGraph(); renderInspector(); return; }
    if (button.dataset.chart) { graphAction(button.dataset.chart); return; }
    if (button.dataset.job || button.dataset.find) {
      const id = button.dataset.job || button.dataset.find, data = state.graphMode === 'source' ? groupsFor(topology,doc.graph_name,false) : chartRuntime?.data;
      const group = data?.groups.find(g => g.id === id); if (!group) return;
      const pick = groupState(group).pick; chooseNode(pick?.node_id || group.members[0]);
      if (button.dataset.find) { if (state.graphMode === 'source') sourceAction('selected'); else focusGroups([group.id]); } return;
    }
    if (button.hasAttribute('data-retry-graph')) { loadGraph(true); return; }
    if (button.dataset.answer) { answer(button.dataset.answer,button.dataset.value); return; }
    if (button.hasAttribute('data-refresh-files')) { loadFiles(true); return; }
    if (button.hasAttribute('data-refresh-context')) { loadContext(true); return; }
    if (button.id === 'jumpStep') { document.querySelector('.bar[aria-pressed="true"]')?.scrollIntoView({block:'nearest',inline:'center',behavior:'smooth'}); }
  });
  document.addEventListener('input',event => {
    const input = event.target;
    if (input.dataset.note) { notes.set(input.dataset.note,input.value); return; }
    if (input.id === 'files-search') { state.filesQuery = input.value; renderFiles(); if (state.filesQuery) document.querySelectorAll('#view .filegroup').forEach(group => group.open = true); }
    if (input.id === 'context-search') { state.contextQuery = input.value; renderContext(); }
    if (input.id === 'graph-search') { state.graphQuery = input.value; renderGraph(); }
  });
  document.addEventListener('change',event => {
    if (event.target.id === 'groupby') { state.group = event.target.value; renderTimeline(); }
    if (event.target.id === 'execution-picker') choose(event.target.value,false);
  });
  document.addEventListener('toggle',event => { if (event.target.matches('details[data-artifact]') && event.target.open) loadVisibleEvidence(); },true);
  document.addEventListener('keydown',event => { const node = event.target.closest('g.node[data-source-node]'); if (node && ['Enter',' '].includes(event.key)) { event.preventDefault(); chooseNode(node.dataset.sourceNode); } });
  tick();
})();
