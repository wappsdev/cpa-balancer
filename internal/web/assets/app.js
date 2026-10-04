"use strict";

const ROUTES = "/v0/management/plugins/cpa-balancer";
const CONFIG = ROUTES + "/config"; // CPA's own plugin config API
const PREFIX = "enc::v1::";
const COLORS = ["#4f46e5", "#d97706", "#059669", "#db2777", "#0891b2", "#7c3aed"];

const $ = id => document.getElementById(id);
const esc = s => String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const short = f => (f || "—").replace(/^claude-[0-9a-f]+-/, "").replace(/\.json$/, "");
const time = t => t ? new Date(t).toLocaleString() : "—";
const num = n => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n);

// The Management Center stores its key XOR-obfuscated with this per-browser string.
function managementKey() {
  let raw;
  try { raw = localStorage.getItem("managementKey"); } catch { return null; }
  if (!raw) return null;
  try {
    if (!raw.startsWith(PREFIX)) return JSON.parse(raw);
    const data = Uint8Array.from(atob(raw.slice(PREFIX.length)), c => c.charCodeAt(0));
    const key = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}|${navigator.userAgent}`);
    return JSON.parse(new TextDecoder().decode(data.map((b, i) => b ^ key[i % key.length])));
  } catch { return null; }
}

async function api(method, path, body) {
  const key = managementKey();
  if (!key) throw new Error("Sign in to the Management Center with “Remember password” enabled, then reload this page.");
  const res = await fetch(path, {
    method, cache: "no-store",
    headers: { Authorization: `Bearer ${key}`, ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!res.ok) throw new Error((data && (data.message || data.error)) || text || `HTTP ${res.status}`);
  return data;
}

function showError(e) {
  $("error").hidden = !e;
  $("error").textContent = e ? e.message || String(e) : "";
}

// ---------- header and overview ----------

let view = null;
let configError = ""; // kept apart from view: it is reported even before any config was accepted

function renderStatus(v) {
  configError = v.config_error || "";
  $("config-error").hidden = !v.config_error;
  $("config-error").textContent = v.config_error ? `The last configuration was rejected and is not in use: ${v.config_error}` : "";
  if (!v.decision) { // no engine yet: the plugin has never accepted a configuration
    $("meta").textContent = "Not configured";
    return;
  }
  view = v;
  $("meta").textContent = `Updated ${time(v.generated_at)}`;
  const pause = $("pause");
  pause.disabled = false;
  pause.setAttribute("aria-pressed", String(!v.paused));
  pause.textContent = v.paused ? "Balancing: Paused" : "Balancing: On";
  $("mode").disabled = false;
  $("mode").value = v.mode;
  $("run").disabled = false;

  const choice = (label, c, order) => `<div class="card"><div class="label">${label}</div>
    <div class="value">${c.account ? esc(short(c.account)) : "no restriction"}</div>
    <div class="muted">${esc(c.reason)}</div>
    <div class="muted">order: ${(order || []).map(o => esc(short(o))).join(" → ") || "—"}</div></div>`;
  $("choices").innerHTML = v.paused
    ? `<div class="card"><div class="label">Paused</div><div class="muted">Every request uses CPA's fill-first order; no probes or anchors run.</div></div>`
    : choice("Main bucket", v.decision.main, v.decision.main_order) + choice("Fable bucket", v.decision.fable, v.decision.fable_order);

  const reading = r => {
    if (!r) return '<span class="muted">no data</span>';
    if (!r.resets_at) return `closed <span class="muted">(${esc(r.source)})</span>`;
    return `${r.util.toFixed(1)}% <span class="muted">resets ${time(r.resets_at)} · ${esc(r.source)}</span>`;
  };
  const eligible = why => `<span class="${why.startsWith("weekly") && why.includes("<") ? "ok" : "bad"}">${esc(why)}</span>`;
  $("accounts").innerHTML = `<tr><th>Account</th><th>Main</th><th>Fable</th><th>5-hour</th><th>Anchor</th></tr>` +
    (v.accounts || []).map(a => `<tr>
      <td><strong>${esc(short(a.auth_file))}</strong><br><span class="muted">${a.reserve ? "keeps a reserve" : "no reserve"}${a.listed ? "" : " · not listed by CPA"}</span></td>
      <td>${reading(a.readings.main)}<br>${eligible(a.eligibility.main)}</td>
      <td>${reading(a.readings.fable)}<br>${eligible(a.eligibility.fable)}</td>
      <td>${reading(a.readings.session)}</td>
      <td>${a.last_anchor ? `${time(a.last_anchor)}<br><code>${esc(a.anchor_model)}</code>` : '<span class="muted">—</span>'}</td>
    </tr>`).join("");
  $("changes").innerHTML = `<tr><th>Time</th><th>Bucket</th><th>Change</th><th>Reason</th></tr>` +
    ((v.changes || []).slice().reverse().map(c => `<tr><td>${time(c.at)}</td><td>${esc(c.bucket)}</td>
      <td>${esc(short(c.from))} → ${esc(short(c.to))}</td><td class="muted">${esc(c.reason)}</td></tr>`).join("") ||
     '<tr><td colspan="4" class="muted">No changes yet.</td></tr>');
}

async function refreshStatus() {
  try {
    renderStatus(await api("GET", ROUTES + "/status"));
    showError(null);
  } catch (e) { showError(e); }
}

// A config write makes CPA rebuild the plugin; read the status back once it has settled.
const settle = () => new Promise(r => setTimeout(r, 2000));

async function patchConfig(change) {
  await api("PATCH", CONFIG, change);
  await settle();
  await refreshStatus();
}

$("pause").addEventListener("click", async e => {
  e.currentTarget.disabled = true;
  try { await patchConfig({ paused: !view.paused }); } catch (err) { showError(err); e.currentTarget.disabled = false; }
});
$("mode").addEventListener("change", async e => {
  e.currentTarget.disabled = true;
  try { await patchConfig({ mode: e.currentTarget.value }); } catch (err) { showError(err); e.currentTarget.disabled = false; }
});
$("run").addEventListener("click", async e => {
  const button = e.currentTarget;
  button.disabled = true;
  button.textContent = "Running…";
  try { renderStatus(await api("POST", ROUTES + "/run")); showError(null); }
  catch (err) { showError(err); }
  finally { button.disabled = false; button.textContent = "Run now"; }
});

// ---------- tabs ----------

let tab = "overview";

function selectTab(name) {
  tab = name;
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === name));
  for (const t of ["overview", "history", "settings"]) $("tab-" + t).hidden = t !== name;
  if (name === "history") loadHistory();
  if (name === "settings") loadSettings();
}
for (const b of document.querySelectorAll(".tabs button")) b.addEventListener("click", () => selectTab(b.dataset.tab));

// ---------- history ----------

let range = "24h";
let hist = null;
let charts = [];

for (const b of document.querySelectorAll(".range button")) {
  b.addEventListener("click", () => {
    range = b.dataset.range;
    for (const o of document.querySelectorAll(".range button")) o.setAttribute("aria-pressed", String(o === b));
    loadHistory();
  });
}

async function loadHistory() {
  try {
    hist = await api("GET", `${ROUTES}/history?range=${range}`);
    renderHistory(hist);
    showError(null);
  } catch (e) { showError(e); }
}

const cssVar = name => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const alpha = (hex, a) => hex + Math.round(a * 255).toString(16).padStart(2, "0");
const stepped = () => uPlot.paths.stepped({ align: 1 });

function axes(yLabel, y = {}) {
  const stroke = cssVar("--muted"), grid = { stroke: cssVar("--line"), width: 1 };
  return [
    { stroke, grid, ticks: grid },
    { stroke, grid, ticks: grid, label: yLabel, size: 56, ...y },
  ];
}

// Vertical markers for events of the given kinds, drawn over the plot.
function markers(kinds, colorOf) {
  return u => {
    const ctx = u.ctx, { top, height } = u.bbox;
    ctx.save();
    ctx.lineWidth = 1.5 * devicePixelRatio;
    ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
    for (const ev of hist.events) {
      if (!kinds.includes(ev.kind)) continue;
      const x = u.valToPos(Date.parse(ev.t) / 1000, "x", true);
      if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) continue;
      ctx.strokeStyle = colorOf(ev);
      ctx.beginPath();
      ctx.moveTo(x, top);
      ctx.lineTo(x, top + height);
      ctx.stroke();
    }
    ctx.restore();
  };
}

function renderHistory(s) {
  for (const c of charts) c.destroy();
  charts = [];
  const names = Object.keys(s.accounts).sort();
  const color = name => COLORS[names.indexOf(name) % COLORS.length] || cssVar("--muted");
  const width = $("chart-weekly").clientWidth || 800;
  const base = (height, scales) => ({
    width, height, scales: { x: { time: true }, ...scales },
    cursor: { sync: { key: "history", setSeries: false }, drag: { x: false, y: false } },
    legend: { live: true },
  });

  const weekly = { ...base(220, { y: { range: [0, 100] } }), axes: axes("%"), series: [{}], hooks: { draw: [markers(["reset"], ev => color(ev.account))] } };
  const weeklyData = [s.t];
  for (const n of names) {
    const a = s.accounts[n];
    weekly.series.push({ label: `${short(n)} main`, stroke: color(n), width: 2, spanGaps: false });
    weekly.series.push({ label: `${short(n)} Fable`, stroke: color(n), width: 2, dash: [6, 4] });
    weekly.series.push({ label: `${short(n)} main cap`, stroke: alpha(color(n), 0.6), width: 1, paths: stepped(), dash: [2, 3] });
    weekly.series.push({ label: `${short(n)} Fable cap`, stroke: alpha(color(n), 0.6), width: 1, paths: stepped(), dash: [2, 3], show: false });
    weeklyData.push(a.main, a.fable, a.main_cap, a.fable_cap);
  }

  const session = { ...base(150, { y: { range: [0, 100] } }), axes: axes("%"), series: [{}], hooks: { draw: [markers(["anchor"], ev => color(ev.account))] } };
  const sessionData = [s.t];
  for (const n of names) {
    session.series.push({ label: short(n), stroke: color(n), fill: alpha(color(n), 0.12), width: 2 });
    sessionData.push(s.accounts[n].session);
  }

  // Lanes: main at y=2, Fable at y=1; a point also covers the step after it so stepped paths join.
  const lane = (bucket, name, y) => s.t.map((_, i) => {
    const act = (s.active[bucket] || [])[i], prev = (s.active[bucket] || [])[i - 1];
    return act === name || (i > 0 && prev === name) ? y : null;
  });
  const active = {
    ...base(100, { y: { range: [0.4, 2.6] } }),
    axes: axes("", { splits: () => [1, 2], values: (u, vals) => vals.map(v => v === 2 ? "main" : "Fable") }),
    series: [{}], hooks: { draw: [markers(["switch"], () => cssVar("--muted"))] },
  };
  const activeData = [s.t];
  for (const n of names) {
    active.series.push({ label: `${short(n)} main`, stroke: color(n), width: 10, paths: stepped(), points: { show: false }, value: (u, v) => v == null ? "—" : "active" });
    active.series.push({ label: `${short(n)} Fable`, stroke: color(n), width: 10, paths: stepped(), points: { show: false }, value: (u, v) => v == null ? "—" : "active" });
    activeData.push(lane("main", n, 2), lane("fable", n, 1));
  }

  // Stacked tokens: each series is the running total, drawn largest first so smaller ones paint over it.
  const tokens = { ...base(180, {}), axes: axes("tokens", { values: (u, vals) => vals.map(num) }), series: [{}] };
  const tokenData = [s.t];
  const totals = s.t.map(() => 0);
  const stacks = names.map(n => s.accounts[n].tokens.map((v, i) => (totals[i] += v)));
  for (let k = names.length - 1; k >= 0; k--) {
    const n = names[k];
    tokens.series.push({ label: short(n), stroke: color(n), fill: alpha(color(n), 0.35), width: 1, paths: stepped(),
      value: (u, v, si, i) => i == null ? "—" : num(s.accounts[n].tokens[i]) });
    tokenData.push(stacks[k]);
  }

  const specs = [["chart-weekly", weekly, weeklyData], ["chart-session", session, sessionData],
                 ["chart-active", active, activeData], ["chart-tokens", tokens, tokenData]];
  for (const [id, opts, data] of specs) {
    $(id).innerHTML = "";
    charts.push(new uPlot(opts, data, $(id)));
  }
  charts[0].hooks.setCursor = charts[0].hooks.setCursor || [];
  charts[0].hooks.setCursor.push(u => describe(u.cursor.idx));
  renderEvents(s);
}

// The cursor box: requests per account and model at that moment, and events inside the step.
function describe(i) {
  if (i == null || !hist) return;
  const s = hist, from = s.t[i], to = from + s.step_seconds;
  const parts = [`<strong>${time(from * 1000)}</strong>`];
  for (const [n, a] of Object.entries(s.accounts)) {
    if (!a.requests[i]) continue;
    const models = Object.entries(a.models || {}).filter(([, v]) => v[i]).map(([m, v]) => `${esc(m)} ${v[i]}`).join(", ");
    parts.push(`${esc(short(n))}: ${a.requests[i]} requests, ${num(a.tokens[i])} tokens (${models})`);
  }
  for (const ev of s.events) {
    const t = Date.parse(ev.t) / 1000;
    if (t >= from && t < to) parts.push(eventText(ev));
  }
  $("cursor-info").innerHTML = parts.join(" · ");
}

function eventText(ev) {
  switch (ev.kind) {
    case "switch": return `${esc(ev.bucket)}: ${esc(short(ev.from))} → ${esc(short(ev.to))} <span class="muted">${esc(ev.reason)}</span>`;
    case "anchor": return `anchored ${esc(short(ev.account))} with <code>${esc(ev.model)}</code>`;
    case "reset": return `${esc(short(ev.account))} ${esc(ev.bucket)} window reset`;
  }
  return esc(ev.kind);
}

function renderEvents(s) {
  const rows = s.events.slice().sort((a, b) => Date.parse(b.t) - Date.parse(a.t)).slice(0, 200)
    .map(ev => `<tr><td>${time(ev.t)}</td><td>${esc(ev.kind)}</td><td>${eventText(ev)}</td></tr>`).join("");
  $("events").innerHTML = `<tr><th>Time</th><th>Kind</th><th>Detail</th></tr>` +
    (rows || '<tr><td colspan="3" class="muted">No events in this range.</td></tr>');
}

addEventListener("resize", () => {
  const width = $("chart-weekly").clientWidth;
  for (const c of charts) c.setSize({ width, height: c.height });
});

// ---------- settings ----------

const SECTIONS = ["accounts", "buckets", "thresholds", "anchor"];
let original = null;
let accounts = [];
let credentials = [];

async function loadSettings() {
  $("review-panel").hidden = true;
  $("validation").textContent = "";
  try {
    const [cfg, creds] = await Promise.all([api("GET", CONFIG), api("GET", ROUTES + "/credentials")]);
    credentials = creds;
    original = Object.fromEntries(SECTIONS.map(k => [k, cfg[k] ?? null]));
    fillForm(original);
    showError(null);
  } catch (e) { showError(e); }
}

function fillForm(c) {
  accounts = (c.accounts || []).map(a => ({ auth_file: a.auth_file, reserve: !!a.reserve }));
  renderAccounts();
  for (const el of document.querySelectorAll("[data-threshold]")) el.value = c.thresholds?.[el.dataset.threshold] ?? "";
  for (const el of document.querySelectorAll("[data-bucket]")) el.value = (c.buckets?.[el.dataset.bucket] || []).join(", ");
  for (const el of document.querySelectorAll("[data-anchor]")) {
    const v = c.anchor?.[el.dataset.anchor];
    if (el.type === "checkbox") el.checked = !!v; else el.value = v ?? "";
  }
}

function renderAccounts() {
  $("account-list").innerHTML = `<tr><th>Account</th><th>Reserve</th><th></th></tr>` + accounts.map((a, i) => `<tr>
    <td><strong>${esc(short(a.auth_file))}</strong><br><span class="muted">${esc(a.auth_file)}${credentials.some(c => c.auth_file === a.auth_file) ? "" : " · not known to CPA"}</span></td>
    <td><label><input type="checkbox" data-reserve="${i}" ${a.reserve ? "checked" : ""}> keeps a reserve</label></td>
    <td><button type="button" data-remove="${i}" aria-label="Remove ${esc(short(a.auth_file))}">Remove</button></td></tr>`).join("");
  const free = credentials.filter(c => !accounts.some(a => a.auth_file === c.auth_file));
  $("account-add").innerHTML = free.length
    ? free.map(c => `<option value="${esc(c.auth_file)}">${esc(c.email || short(c.auth_file))}${c.disabled ? " (disabled)" : ""}</option>`).join("")
    : '<option value="">No other Claude credentials in CPA</option>';
  $("account-add-button").disabled = !free.length;
}

$("account-list").addEventListener("change", e => {
  const i = e.target.dataset.reserve;
  if (i !== undefined) accounts[+i].reserve = e.target.checked;
});
$("account-list").addEventListener("click", e => {
  const i = e.target.dataset.remove;
  if (i !== undefined) { accounts.splice(+i, 1); renderAccounts(); }
});
$("account-add-button").addEventListener("click", () => {
  const f = $("account-add").value;
  if (f) { accounts.push({ auth_file: f, reserve: false }); renderAccounts(); }
});

function collect() {
  const thresholds = {};
  for (const el of document.querySelectorAll("[data-threshold]")) {
    if (el.value !== "") thresholds[el.dataset.threshold] = Number(el.value);
  }
  const buckets = {};
  for (const el of document.querySelectorAll("[data-bucket]")) {
    buckets[el.dataset.bucket] = el.value.split(",").map(m => m.trim()).filter(Boolean);
  }
  const anchor = {};
  for (const el of document.querySelectorAll("[data-anchor]")) {
    if (el.type === "checkbox") anchor[el.dataset.anchor] = el.checked;
    else if (el.value.trim()) anchor[el.dataset.anchor] = el.value.trim();
  }
  return { accounts: accounts.map(a => ({ ...a })), buckets, thresholds, anchor };
}

// The same rules as config.Validate; the plugin checks them again.
function validate(c) {
  const errors = [], t = c.thresholds;
  for (const k of ["reserve_cap_far", "reserve_cap_near", "final_cap", "session_cap"]) {
    if (k in t && !(t[k] >= 0 && t[k] <= 100)) errors.push(`${k} must be between 0 and 100`);
  }
  if ("reserve_cap_far" in t && "reserve_cap_near" in t && t.reserve_cap_far > t.reserve_cap_near) errors.push("reserve cap ≥ 48 h must not exceed the 12–48 h cap");
  if ("reserve_cap_near" in t && "final_cap" in t && t.reserve_cap_near > t.final_cap) errors.push("reserve cap 12–48 h must not exceed the final cap");
  if ("min_dwell_minutes" in t && !(Number.isInteger(t.min_dwell_minutes) && t.min_dwell_minutes >= 0)) errors.push("minimum dwell must be a whole number ≥ 0");
  if ("stale_hours" in t && !(t.stale_hours > 0)) errors.push("stale after must be more than 0 hours");
  if (!c.accounts.length) errors.push("at least one account is required");
  if (!c.buckets.main.length) errors.push("the main bucket needs at least one model");
  return errors;
}

// Flattens a section to "path: value" lines so the review can list exactly what changed.
function flatten(v, prefix, out) {
  if (Array.isArray(v) && v.every(x => typeof x !== "object")) out[prefix] = v.join(", ");
  else if (v && typeof v === "object") for (const [k, x] of Object.entries(v)) flatten(x, prefix ? `${prefix}.${k}` : k, out);
  else out[prefix] = v;
  return out;
}

function changes(before, after) {
  const lines = [], changed = [];
  for (const k of SECTIONS) {
    if (k === "accounts") {
      const was = Object.fromEntries((before.accounts || []).map(a => [a.auth_file, !!a.reserve]));
      const now = Object.fromEntries(after.accounts.map(a => [a.auth_file, a.reserve]));
      const order = (before.accounts || []).map(a => a.auth_file).join() !== after.accounts.map(a => a.auth_file).join();
      for (const f of Object.keys(now)) {
        if (!(f in was)) lines.push(`add account ${short(f)}${now[f] ? " (keeps a reserve)" : ""}`);
        else if (was[f] !== now[f]) lines.push(`${short(f)}: ${now[f] ? "keeps a reserve" : "no reserve"}`);
      }
      for (const f of Object.keys(was)) if (!(f in now)) lines.push(`remove account ${short(f)}`);
      if (lines.length || order) changed.push(k);
      continue;
    }
    const a = flatten(before[k] ?? {}, k, {}), b = flatten(after[k], k, {});
    let any = false;
    for (const p of new Set([...Object.keys(a), ...Object.keys(b)])) {
      if (String(a[p] ?? "") !== String(b[p] ?? "")) { lines.push(`${p}: ${a[p] ?? "default"} → ${b[p] ?? "default"}`); any = true; }
    }
    if (any) changed.push(k);
  }
  return { lines, changed };
}

let pending = null;

$("review").addEventListener("click", () => {
  if (!original) return;
  const current = collect();
  const errors = validate(current);
  $("validation").innerHTML = errors.map(esc).join("<br>");
  $("settings-status").textContent = "";
  if (errors.length) { $("review-panel").hidden = true; return; }
  const { lines, changed } = changes(original, current);
  if (!changed.length) { $("settings-status").textContent = "No changes."; $("review-panel").hidden = true; return; }
  pending = Object.fromEntries(changed.map(k => [k, current[k]]));
  $("review-list").innerHTML = (lines.length ? lines : ["account order"]).map(l => `<li>${esc(l)}</li>`).join("");
  $("review-panel").hidden = false;
});

$("cancel").addEventListener("click", () => { $("review-panel").hidden = true; pending = null; });

$("save").addEventListener("click", async e => {
  const button = e.currentTarget;
  button.disabled = true;
  $("settings-status").textContent = "Saving…";
  try {
    await patchConfig(pending);
    if (configError) {
      $("settings-status").textContent = "";
      $("validation").textContent = `Saved, but the plugin rejected it: ${configError}`;
    } else {
      $("settings-status").textContent = "Saved.";
      await loadSettings();
      $("settings-status").textContent = "Saved.";
    }
    $("review-panel").hidden = true;
  } catch (err) {
    $("settings-status").textContent = "";
    $("validation").textContent = `Not saved: ${err.message}`;
  } finally { button.disabled = false; }
});

// ---------- start ----------

refreshStatus();
setInterval(() => { if (!document.hidden) refreshStatus(); }, 15000);
setInterval(() => { if (!document.hidden && tab === "history") loadHistory(); }, 60000);
