"use strict";
(() => {
  const {esc, ms, fmt, runState, groupRuns, inWindow, patch, badge, presets} = UI;
  const $ = id => document.getElementById(id);
  let records = [], summaries = new Map(), fullDocs = new Map(), online = false, lastSuccess = null, loaded = false;
  const inFlight = new Set();
  const prefs = {window:"7", grouped:false};
  try { Object.assign(prefs, JSON.parse(localStorage.getItem("attractor.hub.preferences") || "{}")); } catch (_) {}
  if (!["today", "7", "all"].includes(prefs.window)) prefs.window = "7";
  $("historywindow").value = prefs.window; $("groupnames").checked = !!prefs.grouped;
  const stamp = value => ms(value) === null ? "Time unknown" : new Date(value).toLocaleString(undefined, {month:"short",day:"numeric",hour:"2-digit",minute:"2-digit"});
  const title = run => run.name || run.goal || run.run_id;
  const url = (id, node) => "/ui/" + encodeURIComponent(id) + (node ? "?node=" + encodeURIComponent(node) : "");
  async function json(path, options = {}) {
    const response = await fetch(path, {...options, signal:AbortSignal.timeout(12000)});
    if (!response.ok) throw new Error(response.status + " " + await response.text());
    return response.status === 204 ? null : response.json();
  }
  function joined() {
    return records.map(record => {
      const summary = summaries.get(record.run_id) || {}, cached = fullDocs.get(record.run_id);
      const run = {...summary, ...record, status:summary.status || record.status, name:summary.name, goal:summary.goal, graph_name:summary.graph_name, ended_at:summary.ended_at};
      if (cached) run.spans = cached.doc.spans || [];
      if (!online && !run.archived) { run.reachable = false; run.last_seen ||= lastSuccess; }
      run.state = runState(run, summary);
      return run;
    });
  }
  function matches(run) {
    const q = $("search").value.trim().toLocaleLowerCase();
    return [title(run), run.run_id, run.graph_name, run.status].join(" ").toLocaleLowerCase().includes(q);
  }
  function elapsed(run) {
    return fmt(UI.runElapsed(run,Date.now()));
  }
  function historyStamp(run) {
    if (["completed","failed"].includes(run.status)) return ms(run.ended_at) === null ? "Completion time unknown" : stamp(run.ended_at);
    return ms(run.last_seen) === null ? "Observation time unknown" : "Last seen " + stamp(run.last_seen);
  }

  function removeMenu(run) {
    if (run.reachable || !online) return "";
    return `<details class="rowmenu" data-key="menu-${esc(run.run_id)}"><summary aria-label="Actions for ${esc(title(run))}">⋯</summary><button data-remove="${esc(run.run_id)}">Remove from hub</button></details>`;
  }
  function miniChart(run) {
    if (!Array.isArray(run.spans)) return '<p class="note">Loading execution summary…</p>';
    if (run.graph_name !== "plan_build_review") {
      const done = run.spans.filter(s => s.ended_at).length;
      return `<p class="note">${run.spans.length} recorded executions · ${done} finished</p>`;
    }
    return `<div class="minichart" role="group" aria-label="Milestone overview">${presets.slice(0, 4).map(([id, name, members], i) => {
      const spans = run.spans.filter(s => members.includes(s.node_id));
      const activeNode = (run.active_nodes || []).find(node => members.includes(node));
      const latest = spans.reduce((a, b) => !a || ms(b.started_at) > ms(a.started_at) ? b : a, null);
      const summary = UI.milestone(spans,members,run.active_nodes || []);
      const status = id === "gate" && spans.length && summary.label !== "Active" && summary.label !== "Failed" ? spans.length + " visits" : summary.label;
      const node = activeNode || latest?.node_id || members[0];
      const icon = id === "gate" && summary.label !== "Failed" ? "◇" : summary.icon;
      return `${i ? '<span class="miniarrow" aria-hidden="true">→</span>' : ''}<a class="minijob ${activeNode ? 'now' : ''}" href="${esc(url(run.run_id, node))}" aria-label="${esc(name + ', ' + status + '. Open this group.')}"><strong><span class="miniicon" aria-hidden="true">${icon}</span> ${esc(id === "baseline" ? "Baseline" : name)}</strong><small>${esc(status)}</small></a>`;
    }).join("")}</div><p class="minichartcaption">Milestone overview · ${run.spans.length} recorded executions · open graph for dependencies</p>`;
  }
  function currentCard(run) {
    const waiting = run.state.kind === "waiting", active = (run.active_nodes || []).join(", ");
    return `<article class="card activecard" data-key="current-${esc(run.run_id)}"><div class="cardhead"><div class="labelwrap"><a class="titlelink" href="${url(run.run_id)}">${esc(title(run))}</a><div class="metadata"><span>${esc(run.graph_name || 'Pipeline unknown')}</span><span>${esc(run.run_id)}</span></div></div>${badge(run.state.kind, run.state.label)}</div><div class="current"><div><b>${esc(waiting ? ((run.pending_questions || [])[0]?.question?.text || 'Waiting for human review') : active || 'Starting…')}</b><small>${waiting ? (run.pending_questions || []).length + ' pending question(s)' : 'Active node'}</small></div><span class="muted">${elapsed(run)}</span></div>${miniChart(run)}<div class="cardfoot"><small>Started ${stamp(run.started_at)}</small><a href="${esc(url(run.run_id, waiting ? run.pending_questions?.[0]?.node_id : null))}">${waiting ? 'Review run' : 'Open run'} →</a></div></article>`;
  }
  function historyRow(run, compact = false) {
    return `<div class="${compact ? 'groupentry' : 'historyrow'}" data-key="history-${esc(run.run_id)}">${compact ? `<a href="${url(run.run_id)}">${esc(run.run_id)}</a><small>${historyStamp(run)}</small>${badge(run.state.kind,run.state.label)}${removeMenu(run)}` : `<div class="labelwrap"><a class="titlelink" href="${url(run.run_id)}">${esc(title(run))}</a><small>${esc(run.graph_name || 'Pipeline unknown')} · ${esc(run.run_id)}</small></div>${badge(run.state.kind, run.state.label)}<div class="historytime">${historyStamp(run)}<br><small>${elapsed(run)}</small></div>${removeMenu(run)}`}</div>`;
  }
  function history(runs, section) {
    if (!runs.length) return '<div class="empty">No runs match this view.</div>';
    return `<div class="rows">${groupRuns(runs, $("groupnames").checked).map(group => {
      const run = group.runs[0], key = section + ":" + group.key;
      if (group.runs.length === 1) return `<article class="historyitem" data-key="${esc(key)}">${historyRow(run)}</article>`;
      return `<article class="historyitem" data-key="${esc(key)}"><div class="historyrow"><div class="labelwrap"><b>${esc(title(run))}</b><small>${esc(run.graph_name)} · ${group.runs.length} runs with similar names</small></div>${badge('snapshot','Mixed run history')}<div class="historytime">${historyStamp(run)}<br><small>${section === 'snapshots' ? 'Stored snapshots' : 'Latest result'}</small></div></div><details data-key="disclosure-${esc(key)}"><summary>Similar names · show all ${group.runs.length} runs</summary>${group.runs.map(r => historyRow(r, true)).join('')}<p class="note">Matching labels do not establish a retry relationship.</p></details></article>`;
    }).join('')}</div>`;
  }
  function render() {
    const all = joined().filter(matches), active = all.filter(r => r.state.kind === "running"), attention = all.filter(r => r.state.kind === "waiting");
    const terminal = all.filter(r => ["completed", "failed"].includes(r.state.kind));
    const recent = terminal.filter(r => inWindow(r, $("historywindow").value, Date.now())).sort((a, b) => ms(b.ended_at) - ms(a.ended_at));
    const unknownTime = terminal.filter(r => ms(r.ended_at) === null);
    const snapshots = all.filter(r => ["snapshot", "disconnected", "unknown"].includes(r.state.kind));
    const section = (id, name, rows, content, subtitle = "") => `<section class="group" data-key="section-${id}"><div class="sectiontitle"><h2>${name} <span class="count">${rows.length}</span></h2>${subtitle ? `<small>${subtitle}</small>` : ''}</div>${content}</section>`;
    patch($("hub-results"), `${attention.length ? section('attention', 'Needs attention', attention, attention.map(currentCard).join('')) : ''}${section('active', 'In progress', active, active.length ? active.map(currentCard).join('') : `<div class="empty">${$("search").value ? 'No active runs match this search.' : 'No current reachable runs.'}</div>`)}${section('recent','Recent results',recent,history(recent,'recent'),$("historywindow").value === 'all' ? 'All dated results' : $("historywindow").value === 'today' ? 'Finished today' : 'Finished in the last 7 days')}${snapshots.length ? section('snapshots','Last known state',snapshots,history(snapshots,'snapshots')) : ''}${unknownTime.length ? section('unknown-time','Completion time unknown',unknownTime,history(unknownTime,'unknown-time')) : ''}${loaded && !records.length ? '<div class="empty">No runs yet. Launch a run to begin.</div>' : ''}`);
    $("connection-dot").classList.toggle("offline", !online);
    $("connection-label").textContent = online ? "Connected" : loaded ? "Connection lost" : "Connecting…";
    patch($("connection-notice"), online ? "" : loaded ? '<div class="notice attention">Connection to the hub lost. Showing last fetched records; retrying…</div>' : '');
  }
  async function loadActiveDetails() {
    const active = joined().filter(r => ["running", "waiting"].includes(r.state.kind) && matches(r));
    await Promise.all(active.map(async run => {
      if (inFlight.has(run.run_id) || Date.now() - (fullDocs.get(run.run_id)?.at || 0) < 2500) return;
      inFlight.add(run.run_id);
      try { const doc = await json("/pipelines/" + encodeURIComponent(run.run_id)); fullDocs.set(run.run_id, {doc, at:Date.now()}); if (online) render(); }
      catch (_) { /* summary still useful; try again on the next poll */ }
      finally { inFlight.delete(run.run_id); }
    }));
  }
  async function tick() {
    try {
      const [runs, docs] = await Promise.all([json("/runs"), json("/pipelines")]);
      records = Array.isArray(runs) ? runs : []; summaries = new Map((Array.isArray(docs) ? docs : []).map(d => [d.run_id, d]));
      online = true; loaded = true; lastSuccess = new Date().toISOString(); render(); await loadActiveDetails();
    } catch (_) { online = false; loaded = true; render(); }
    setTimeout(tick, 2000);
  }
  function savePrefs() { try { localStorage.setItem("attractor.hub.preferences", JSON.stringify({window:$("historywindow").value,grouped:$("groupnames").checked})); } catch (_) {} }
  $("search").addEventListener("input", () => { render(); loadActiveDetails(); });
  $("historywindow").addEventListener("change", () => { savePrefs(); render(); });
  $("groupnames").addEventListener("change", () => { savePrefs(); render(); });
  $("launch-toggle").onclick = () => $("launch-dialog").showModal();
  $("launch-close").onclick = () => $("launch-dialog").close();
  $("launch").onsubmit = async event => {
    event.preventDefault(); const vars = {};
    for (const pair of $("l-vars").value.trim().split(/\s+/).filter(Boolean)) { const at = pair.indexOf("="); if (at > 0) vars[pair.slice(0, at)] = pair.slice(at + 1); }
    $("launch-submit").disabled = true; $("l-msg").textContent = "Launching…";
    try { await json("/pipelines", {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({path:$("l-path").value,cwd:$("l-cwd").value,vars})}); $("l-msg").textContent = "Launched. The run will appear when it announces."; }
    catch (error) { $("l-msg").textContent = "Launch failed: " + error.message; }
    finally { $("launch-submit").disabled = false; }
  };
  document.addEventListener("click", async event => {
    const button = event.target.closest("[data-remove]"); if (!button) return;
    const id = button.dataset.remove, current = joined().find(r => r.run_id === id);
    if (!current || current.reachable || !online) return;
    if (!confirm("Remove run " + id + " from the hub? This deletes its stored record.")) return;
    button.disabled = true;
    try { await json("/runs/" + encodeURIComponent(id), {method:"DELETE"}); records = records.filter(r => r.run_id !== id); fullDocs.delete(id); render(); }
    catch (error) { alert("Removal failed: " + error.message); button.disabled = false; }
  });
  tick();
})();
