// Shared browser helpers. Go embeds this source directly; no bundler or packages.
"use strict";
(() => {
  const ms = value => { if (!value || String(value).startsWith("0001-")) return null; const n = Date.parse(value); return Number.isFinite(n) ? n : null; };
  const isJSON = type => /^application\/(json|[a-z0-9.+-]+\+json)(;|$)/i.test(type || "");
  const spanKey = s => JSON.stringify([s.node_id, s.visit || 1, s.attempt || 1]);
  const edgeKey = e => JSON.stringify([e.from, e.to]);
  const esc = value => String(value ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;"}[c]));
  const fmt = value => {
    if (!Number.isFinite(value) || value < 0) return "Unknown";
    if (value < 1000) return "<1s";
    const s = Math.floor(value / 1000);
    if (s < 60) return s + "s";
    if (s < 3600) return Math.floor(s / 60) + "m " + s % 60 + "s";
    return Math.floor(s / 3600) + "h " + Math.floor(s % 3600 / 60) + "m";
  };
  const duration = (s, end) => {
    const start = ms(s.started_at), stop = ms(s.ended_at) ?? end;
    return start === null || !Number.isFinite(stop) ? null : Math.max(0, stop - start);
  };
  const barText = (s, end) => s.is_gate ? "◇ v" + (s.visit || 1) + ((s.attempt || 1) > 1 ? " a" + s.attempt : "") : (s.current ? "● " : "") + fmt(duration(s, end));
  function runState(record = {}, doc = {}) {
    const status = record.status || doc.status;
    if (record.archived && !["completed", "failed"].includes(status)) return {kind:"snapshot", label:"Archived · last recorded " + (status || "unknown")};
    if (record.reachable === false && !record.archived) return {kind:"disconnected", label:"Connection lost · last seen " + (status || "unknown")};
    if (status === "completed") return {kind:"completed", label:"Completed"};
    if (status === "failed") return {kind:"failed", label:"Failed"};
    if (status === "running" && record.reachable !== false && !record.archived) return (doc.pending_questions || []).length ? {kind:"waiting",label:"Needs review"} : {kind:"running",label:"In progress"};
    return {kind:"unknown",label:"State unknown"};
  }
  function runElapsed(run, now) {
    const start = ms(run.started_at);
    if (start === null) return null;
    const terminal = ["completed", "failed"].includes(run.status);
    const end = ms(run.ended_at) ?? (terminal ? null : ["running", "waiting"].includes(run.state?.kind) ? now : ms(run.last_seen));
    return end === null ? null : Math.max(0,end - start);
  }
  function milestone(spans, members, activeNodes) {
    const latest = new Map(spans.filter(s => members.includes(s.node_id)).map(s => [s.node_id,s]));
    if (activeNodes.some(id => members.includes(id))) return {label:"Active",icon:"●",active:true};
    if ([...latest.values()].some(s => s.outcome === "fail")) return {label:"Failed",icon:"✗",active:false};
    if (latest.size === members.length && [...latest.values()].every(s => ms(s.ended_at) !== null && ["success","partial_success","skipped"].includes(s.outcome))) return {label:"Done",icon:"✓",active:false};
    return {label:latest.size ? "Visited" : "Not run",icon:"○",active:false};
  }
  function inWindow(run, days, now) {
    const end = ms(run.ended_at);
    if (end === null) return false;
    if (days === "all") return true;
    const start = days === "today" ? new Date(now).setHours(0, 0, 0, 0) : now - Number(days) * 86400000;
    return end >= start && end <= now;
  }
  function groupRuns(runs, enabled) {
    const groups = new Map();
    for (const run of runs) {
      const named = enabled && typeof run.name === "string" && run.name.trim() && run.graph_name;
      const key = named ? JSON.stringify(["name", run.name, run.graph_name]) : JSON.stringify(["run", run.run_id]);
      if (!groups.has(key)) groups.set(key, {key, runs:[]});
      groups.get(key).runs.push(run);
    }
    return [...groups.values()];
  }
  function friendlyAxis(spans, end) {
    const rows = spans.filter(s => ms(s.started_at) !== null).map(s => ({s, start:ms(s.started_at), end:Math.max(ms(s.started_at), ms(s.ended_at) ?? end)})).filter(r => Number.isFinite(r.end));
    const bounds = [...new Set(rows.flatMap(r => [r.start, r.end]))].sort((a, b) => a - b);
    if (!bounds.length) return {at:() => 0, width:700};
    const xs = [0], indices = new Map(bounds.map((b, i) => [b, i]));
    for (let i = 1; i < bounds.length; i++) {
      const a = bounds[i - 1], b = bounds[i];
      const work = rows.some(r => !r.s.is_gate && r.start <= a && r.end >= b);
      let x = xs[i - 1] + (work ? Math.min(95, 12 + Math.log1p((b - a) / 1000) * 8) : 4);
      for (const r of rows) if (!r.s.is_gate && r.end === b && r.start < b) x = Math.max(x, xs[indices.get(r.start)] + 44);
      xs.push(x);
    }
    return {
      at:t => {
        if (!Number.isFinite(t)) return 0;
        const i = bounds.findIndex(b => b >= t);
        if (i < 0) return xs.at(-1);
        if (i === 0 || bounds[i] === t) return xs[i];
        return xs[i - 1] + (xs[i] - xs[i - 1]) * (t - bounds[i - 1]) / (bounds[i] - bounds[i - 1]);
      },
      width:Math.max(700, xs.at(-1) + 76)
    };
  }
  function pack(items, gap = 8) {
    const ends = [];
    return [...items].sort((a, b) => a.x - b.x).map(item => {
      let slot = ends.findIndex(end => end + gap <= item.x);
      if (slot < 0) slot = ends.length;
      ends[slot] = item.x + item.w;
      return {...item, slot};
    });
  }
  function rankGraph(groups, edges) {
    const seen = new Set(), active = new Set(), back = new Set();
    function visit(id) {
      seen.add(id); active.add(id);
      for (const e of edges.filter(e => e.from === id)) {
        if (active.has(e.to)) back.add(edgeKey(e));
        else if (!seen.has(e.to)) visit(e.to);
      }
      active.delete(id);
    }
    groups.forEach(g => { if (!seen.has(g.id)) visit(g.id); });
    const forward = edges.filter(e => !back.has(edgeKey(e))), incoming = new Map(groups.map(g => [g.id, 0])), ranks = new Map(groups.map(g => [g.id, 0]));
    forward.forEach(e => incoming.set(e.to, (incoming.get(e.to) || 0) + 1));
    const queue = groups.filter(g => incoming.get(g.id) === 0).map(g => g.id);
    while (queue.length) {
      const id = queue.shift();
      for (const e of forward.filter(e => e.from === id)) {
        ranks.set(e.to, Math.max(ranks.get(e.to) || 0, ranks.get(id) + 1));
        incoming.set(e.to, incoming.get(e.to) - 1);
        if (incoming.get(e.to) === 0) queue.push(e.to);
      }
    }
    return {ranks, back};
  }
  const presets = [
    ["baseline", "Baseline checks", ["start", "set_base", "baseline.deps", "baseline.typecheck", "baseline.lint", "baseline.test"]],
    ["planning", "Planning", ["plan", "revise_plan"]], ["gate", "Plan gate", ["plan_gate"]],
    ["implement", "Implement", ["implement"]],
    ["checks", "Checks", ["checks.deps", "checks.typecheck", "checks.lint", "checks.test", "tree_clean", "fix_checks"]],
    ["review", "Review", ["review_loop.fan_out", "review_loop.correctness", "review_loop.design", "review_loop.prod_safety", "review_loop.simplification", "review_loop.tests", "review_loop.synth"]],
    ["revise", "Revise work", ["respond_to_review"]], ["ship", "Ship gate", ["ship"]], ["finish", "Finish", ["push_branch", "done"]]
  ];
  const label = id => String(id).replaceAll("_", " ").replaceAll(".", " / ");
  function groupsFor(topology, graphName, overview) {
    const known = overview && graphName === "plan_build_review";
    const groups = known ? presets.map(([id, name, members]) => ({id, name, members:members.filter(m => topology.nodes.some(n => n.id === m))})).filter(g => g.members.length) : [];
    const covered = new Set(groups.flatMap(g => g.members));
    topology.nodes.filter(n => !covered.has(n.id)).forEach(n => groups.push({id:n.id, name:label(n.id), members:[n.id]}));
    const owners = new Map(groups.flatMap(g => g.members.map(id => [id, g.id]))), edges = [];
    for (const edge of topology.edges) {
      const from = owners.get(edge.from), to = owners.get(edge.to);
      if (!from || !to || known && from === to) continue;
      const existing = edges.find(e => e.from === from && e.to === to);
      if (existing) { if (edge.label && !existing.labels.includes(edge.label)) existing.labels.push(edge.label); }
      else edges.push({from, to, labels:edge.label ? [edge.label] : []});
    }
    return {groups, edges, overview:known, nodeCount:topology.nodes.length, edgeCount:topology.edges.length};
  }
  function chartLayout(data) {
    const w = 166, h = 84, gap = 32, {ranks, back} = rankGraph(data.groups, data.edges), columns = new Map();
    for (const g of data.groups) { const rank = ranks.get(g.id); if (!columns.has(rank)) columns.set(rank, []); columns.get(rank).push(g); }
    const count = Math.max(1, ...[...columns.values()].map(c => c.length));
    const routed = data.edges.filter(e => back.has(edgeKey(e)) || ranks.get(e.to) - ranks.get(e.from) > 1).length;
    const topPadding = Math.max(80, (routed + 1) * 8 + 28) + 32, bodyHeight = Math.max(224, count * 112), height = topPadding + bodyHeight + 40, pos = new Map();
    columns.forEach((gs, rank) => gs.forEach((g, i) => pos.set(g.id, {x:32 + rank * (w + gap), y:topPadding + (bodyHeight - gs.length * 112) / 2 + i * 112, w, h})));
    return {pos, width:64 + (Math.max(0, ...ranks.values()) + 1) * (w + gap), height, ranks, back};
  }
  // Preserve native input focus, details, scroll containers and loaded evidence
  // while patching data during polling. Views use stable id/data-key identities.
  function patch(host, html) {
    const template = document.createElement("template"); template.innerHTML = html;
    const key = n => n.nodeType === 1 ? n.id || n.getAttribute("data-key") : null;
    const compatible = (a, b) => a.nodeType === b.nodeType && (a.nodeType !== 1 || a.tagName === b.tagName);
    function sync(a, b) {
      if (!compatible(a, b)) { a.replaceWith(b.cloneNode(true)); return; }
      if (a.nodeType !== 1) { if (a.nodeValue !== b.nodeValue) a.nodeValue = b.nodeValue; return; }
      const preserve = a.hasAttribute("data-preserve") && b.hasAttribute("data-preserve");
      for (const attr of [...a.attributes]) if (!b.hasAttribute(attr.name) && !(a.tagName === "DETAILS" && attr.name === "open")) a.removeAttribute(attr.name);
      for (const attr of [...b.attributes]) {
        if (attr.name === "open" && a.tagName === "DETAILS") continue;
        if (attr.name === "value" && document.activeElement === a) continue;
        if (a.getAttribute(attr.name) !== attr.value) a.setAttribute(attr.name, attr.value);
      }
      if (!preserve) children(a, b);
    }
    function children(a, b) {
      const old = [...a.childNodes], unused = new Set(old), keyed = new Map(old.filter(key).map(n => [key(n), n]));
      let cursor = a.firstChild;
      for (const next of [...b.childNodes]) {
        const id = key(next);
        let current = id ? keyed.get(id) : old.find(n => unused.has(n) && !key(n) && compatible(n, next));
        if (current && compatible(current, next)) { unused.delete(current); sync(current, next); }
        else current = next.cloneNode(true);
        if (current !== cursor) a.insertBefore(current, cursor);
        cursor = current.nextSibling;
      }
      unused.forEach(n => n.remove());
    }
    children(host, template.content);
  }
  const badge = (kind, text) => `<span class="badge ${esc(kind)}"><span class="statusdot" aria-hidden="true"></span>${esc(text)}</span>`;
  const fileURL = (base, path) => base + "/artifacts/" + path.split("/").map(encodeURIComponent).join("/");
  globalThis.UI = Object.freeze({runElapsed, milestone, isJSON, ms, spanKey, edgeKey, esc, fmt, duration, barText, runState, inWindow, groupRuns, friendlyAxis, pack, rankGraph, presets, label, groupsFor, chartLayout, patch, badge, fileURL});
})();
