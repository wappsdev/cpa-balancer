"use strict";

const ROUTES = "/v0/management/plugins/cpa-balancer";
const CONFIG = ROUTES + "/config"; // CPA's own plugin config API
const PREFIX = "enc::v1::";
const COLORS = ["#6366f1", "#f59e0b", "#10b981", "#ec4899", "#06b6d4", "#8b5cf6"];

const $ = id => document.getElementById(id);
const esc = s => String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const short = f => (f || "—").replace(/^claude-[0-9a-f]+-/, "").replace(/\.json$/, "");
const time = t => t ? new Date(t).toLocaleString() : "—";
// Relative time: "in 4d 3h", "12m ago"; the exact time goes into a title attribute.
function rel(t) {
  if (!t) return "—";
  let s = Math.round((Date.parse(t) - Date.now()) / 1000);
  const future = s > 0;
  s = Math.abs(s);
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  const span = d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : m ? `${m}m` : "now";
  return span === "now" ? "just now" : future ? `in ${span}` : `${span} ago`;
}
const when = t => `<span class="when" title="${esc(time(t))}">${esc(rel(t))}</span>`;
const num = n => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n);

// The Management Center persists its session under "cli-proxy-auth" (the key only with
// "Remember password"), XOR-obfuscated with this per-browser string.
function managementKey() {
  let raw;
  try { raw = localStorage.getItem("cli-proxy-auth"); } catch { return null; }
  if (!raw) return null;
  try {
    let json = raw;
    if (raw.startsWith(PREFIX)) {
      const data = Uint8Array.from(atob(raw.slice(PREFIX.length)), c => c.charCodeAt(0));
      const key = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}|${navigator.userAgent}`);
      json = new TextDecoder().decode(data.map((b, i) => b ^ key[i % key.length]));
    }
    return JSON.parse(json)?.state?.managementKey || null;
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
  $("meta").textContent = `${v.paused ? "Paused" : v.mode === "shadow" ? "Shadow mode — deciding, not applying" : "Balancing"} · updated ${rel(v.generated_at)}`;
  $("live-dot").className = "dot " + (v.paused ? "paused" : "on");
  const pause = $("pause");
  pause.disabled = false;
  pause.setAttribute("aria-checked", String(!v.paused));
  $("pause-label").textContent = v.paused ? "Paused" : "On";
  for (const b of $("mode").querySelectorAll("button")) {
    b.disabled = false;
    b.setAttribute("aria-pressed", String(b.dataset.mode === v.mode));
  }
  $("run").disabled = false;

  const choice = (label, c, order) => `<div class="card choice">
    <div class="eyebrow">${label}</div>
    <div class="account">${c.account ? esc(short(c.account)) : "No restriction"}</div>
    <div class="reason">${esc(c.reason)}</div>
    <div class="order">${(order || []).map((o, i) => `<span class="chip${o === c.account ? " accent" : ""}"><b>${i + 1}</b> ${esc(short(o))}</span>`).join("")}</div>
  </div>`;
  $("choices").innerHTML = v.paused
    ? `<div class="card choice"><div class="eyebrow">Paused</div><div class="account">CPA's fill-first order</div>
       <div class="reason">Every request uses CPA's own order. No probes or anchors run until balancing is turned back on.</div></div>`
    : choice("Main bucket", v.decision.main, v.decision.main_order) + choice("Fable bucket", v.decision.fable, v.decision.fable_order);

  $("accounts").innerHTML = (v.accounts || []).map(a => accountCard(a, v)).join("");

  $("changes").innerHTML = (v.changes || []).slice().reverse().slice(0, 12).map(c => `<li>
      <span class="mark"></span><span class="chip">${esc(c.bucket)}</span>
      <span class="what">${esc(short(c.from))} → <strong>${esc(short(c.to))}</strong><span class="reason">${esc(c.reason)}</span></span>
      ${when(c.at)}</li>`).join("") || '<li><span class="empty">No changes yet.</span></li>';
}

const BUCKETS = [["main", "Main"], ["fable", "Fable"], ["session", "5-hour"]];

function accountCard(a, v) {
  const activeIn = [["main", "main"], ["fable", "Fable"]].filter(([b]) => !v.paused && v.decision[b].account === a.auth_file).map(([, l]) => l);
  const rows = BUCKETS.map(([b, label]) => {
    const r = a.readings[b], cap = a.caps?.[b];
    const open = r && r.resets_at;
    const util = open ? r.util : 0;
    const level = cap == null ? "" : util >= cap ? "bad" : util >= cap - 10 ? "warn" : "";
    const value = !r ? '<span>no data</span>'
      : !open ? `<span>${b === "session" ? "window not running" : "closed"}</span>`
      : `<strong>${util.toFixed(1)}%</strong>${cap != null ? `<span>cap ${cap}%</span>` : ""}<span title="${esc(time(r.resets_at))}">resets ${esc(rel(r.resets_at))}</span>`;
    const why = a.eligibility?.[b];
    const blocked = why && !(why.startsWith("weekly") && why.includes("<"));
    return `<div class="meter-row">
        <span class="label">${label}</span>
        <div class="meter" role="meter" aria-label="${label} utilization" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${util}">
          <div class="fill ${level}" style="transform: scaleX(${Math.min(util, 100) / 100})"></div>
          ${open && cap != null ? `<div class="cap" style="left: ${cap}%" title="cap ${cap}%"></div>` : ""}
        </div>
        <span class="meter-value">${value}</span>
      </div>${blocked ? `<div class="why">${esc(why)}</div>` : ""}`;
  }).join("");
  const sources = [...new Set(Object.values(a.readings || {}).map(r => r.source))].join(", ");
  return `<article class="card account-card">
    <div class="account-head">
      <span class="name">${esc(short(a.auth_file))}</span>
      ${activeIn.length ? `<span class="chip accent">● active · ${activeIn.join(", ")}</span>` : ""}
      <span class="chip">${a.reserve ? "keeps a reserve" : "no reserve"}</span>
      ${a.listed ? "" : '<span class="chip bad">not listed by CPA</span>'}
    </div>
    ${rows}
    <div class="account-foot">
      <span>${a.last_anchor ? `Last anchor ${when(a.last_anchor)} · <code>${esc(a.anchor_model)}</code>` : "No anchor yet"}</span>
      <span>${sources ? `source: ${esc(sources)}` : ""}</span>
    </div>
  </article>`;
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
  const button = e.currentTarget;
  button.disabled = true;
  button.setAttribute("aria-checked", String(view.paused)); // flip at once; the status read confirms it
  try { await patchConfig({ paused: !view.paused }); } catch (err) { showError(err); renderStatus(view); }
});
$("mode").addEventListener("click", async e => {
  const mode = e.target.closest("button")?.dataset.mode;
  if (!mode || mode === view?.mode) return;
  const buttons = $("mode").querySelectorAll("button");
  for (const b of buttons) { b.disabled = true; b.setAttribute("aria-pressed", String(b.dataset.mode === mode)); }
  try { await patchConfig({ mode }); } catch (err) { showError(err); renderStatus(view); }
});
$("run").addEventListener("click", async e => {
  const button = e.currentTarget;
  button.disabled = true;
  button.classList.add("busy");
  try { renderStatus(await api("POST", ROUTES + "/run")); showError(null); }
  catch (err) { showError(err); }
  finally { button.disabled = false; button.classList.remove("busy"); }
});

// ---------- tabs ----------

let tab = "overview";

// The indicator is a fixed 100px bar moved and scaled with transform, so only compositing runs.
function placeIndicator() {
  const b = document.querySelector(`.tabs button[data-tab="${tab}"]`);
  document.querySelector(".tab-indicator").style.transform = `translateX(${b.offsetLeft}px) scaleX(${b.offsetWidth / 100})`;
}

function selectTab(name) {
  tab = name;
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === name));
  placeIndicator();
  for (const t of ["overview", "history", "settings"]) $("tab-" + t).hidden = t !== name;
  if (name === "history") loadHistory();
  if (name === "settings") loadSettings();
}
for (const b of document.querySelectorAll(".tabs button")) b.addEventListener("click", () => selectTab(b.dataset.tab));

// ---------- history ----------

let range = "24h";
let hist = null;
let charts = [];

for (const b of $("range").querySelectorAll("button")) {
  b.addEventListener("click", () => {
    range = b.dataset.range;
    for (const o of $("range").querySelectorAll("button")) o.setAttribute("aria-pressed", String(o === b));
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
const pct = (u, v) => v == null ? "—" : v.toFixed(1) + "%";

function axes(yLabel, y = {}) {
  const stroke = cssVar("--muted"), grid = { stroke: cssVar("--line"), width: 1 }, font = "11px -apple-system, sans-serif";
  return [
    { stroke, grid, ticks: { show: false }, font },
    { stroke, grid, ticks: { show: false }, font, label: yLabel, labelFont: font, size: 52, ...y },
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
    weekly.series.push({ label: `${short(n)} main`, stroke: color(n), width: 2, value: pct });
    weekly.series.push({ label: `${short(n)} Fable`, stroke: color(n), width: 2, dash: [6, 4], value: pct });
    weekly.series.push({ label: `${short(n)} main cap`, stroke: alpha(color(n), 0.6), width: 1, paths: stepped(), dash: [2, 3], value: pct });
    weekly.series.push({ label: `${short(n)} Fable cap`, stroke: alpha(color(n), 0.6), width: 1, paths: stepped(), dash: [2, 3], show: false, value: pct });
    weeklyData.push(a.main, a.fable, a.main_cap, a.fable_cap);
  }

  const session = { ...base(150, { y: { range: [0, 100] } }), axes: axes("%"), series: [{}], hooks: { draw: [markers(["anchor"], ev => color(ev.account))] } };
  const sessionData = [s.t];
  for (const n of names) {
    session.series.push({ label: short(n), stroke: color(n), fill: alpha(color(n), 0.12), width: 2, value: pct });
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
  active.cursor = { ...active.cursor, points: { show: false } }; // a dot on a 10px lane only hides it
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

// Where the pointer is over a chart, so the tip can sit next to it.
let pointer = null;
document.addEventListener("pointermove", e => {
  pointer = e.target.closest?.(".u-over") ? { x: e.clientX, y: e.clientY } : null;
  if (!pointer) $("chart-tip").hidden = true;
});

// The hovered moment: its time in the bar, and requests per account and model plus the
// events inside the step in a tip beside the pointer.
function describe(i) {
  if (i == null || !hist) { $("chart-tip").hidden = true; return; }
  const s = hist, from = s.t[i], to = from + s.step_seconds;
  $("cursor-time").innerHTML = `<strong>${esc(time(from * 1000))}</strong>`;
  const lines = [];
  for (const [n, a] of Object.entries(s.accounts)) {
    if (!a.requests[i]) continue;
    const models = Object.entries(a.models || {}).filter(([, v]) => v[i]).map(([m, v]) => `${esc(m)} ${v[i]}`).join(", ");
    lines.push(`<strong>${esc(short(n))}</strong> ${a.requests[i]} requests · ${num(a.tokens[i])} tokens<br>${models}`);
  }
  for (const ev of s.events) {
    const t = Date.parse(ev.t) / 1000;
    if (t >= from && t < to) lines.push(eventText(ev));
  }
  const tip = $("chart-tip");
  if (!lines.length || !pointer) { tip.hidden = true; return; }
  tip.innerHTML = lines.map(l => `<div>${l}</div>`).join("");
  tip.hidden = false;
  // Right of the pointer, or left of it near the window edge.
  const x = pointer.x + 16 + tip.offsetWidth > innerWidth ? pointer.x - 16 - tip.offsetWidth : pointer.x + 16;
  tip.style.transform = `translate(${Math.max(8, x)}px, ${Math.max(8, pointer.y - tip.offsetHeight - 12)}px)`;
}

function eventText(ev) {
  switch (ev.kind) {
    case "switch": return `${esc(ev.bucket)}: ${esc(short(ev.from))} → <strong>${esc(short(ev.to))}</strong> <span class="reason">${esc(ev.reason)}</span>`;
    case "anchor": return `anchored ${esc(short(ev.account))} with <code>${esc(ev.model)}</code>`;
    case "reset": return `${esc(short(ev.account))} ${esc(ev.bucket)} window reset`;
  }
  return esc(ev.kind);
}

function renderEvents(s) {
  $("events").innerHTML = s.events.slice().sort((a, b) => Date.parse(b.t) - Date.parse(a.t)).slice(0, 200)
    .map(ev => `<li><span class="mark ${esc(ev.kind)}"></span><span class="chip">${esc(ev.kind)}</span>
      <span class="what">${eventText(ev)}</span>${when(ev.t)}</li>`).join("") ||
    '<li><span class="empty">No events in this range.</span></li>';
}

addEventListener("resize", () => {
  placeIndicator();
  const width = $("chart-weekly").clientWidth;
  for (const c of charts) c.setSize({ width, height: c.height });
});

// ---------- settings ----------

const SECTIONS = ["accounts", "buckets", "thresholds", "anchor"];
let original = null;
let accounts = [];
let credentials = [];
const buckets = { main: [], fable: [] };

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
  for (const b of Object.keys(buckets)) { buckets[b] = [...(c.buckets?.[b] || [])]; renderChips(b); }
  for (const el of document.querySelectorAll("[data-anchor]")) {
    const v = c.anchor?.[el.dataset.anchor];
    if (el.getAttribute("role") === "switch") el.setAttribute("aria-checked", String(!!v)); else el.value = v ?? "";
  }
  renderRuler();
}

// Model chips: Enter or comma adds, Backspace on an empty input removes the last one.
function renderChips(bucket) {
  const box = document.querySelector(`.chips[data-bucket="${bucket}"]`), input = box.querySelector("input");
  for (const c of box.querySelectorAll(".chip")) c.remove();
  for (const [i, m] of buckets[bucket].entries()) {
    const chip = document.createElement("span");
    chip.className = "chip";
    chip.innerHTML = `${esc(m)}<button type="button" data-chip="${i}" aria-label="Remove ${esc(m)}">×</button>`;
    box.insertBefore(chip, input);
  }
}

for (const box of document.querySelectorAll(".chips")) {
  const bucket = box.dataset.bucket, input = box.querySelector("input");
  const add = () => {
    const m = input.value.trim().replace(/,$/, "");
    if (m && !buckets[bucket].includes(m)) buckets[bucket].push(m);
    input.value = "";
    renderChips(bucket);
  };
  input.addEventListener("keydown", e => {
    if (e.key === "Enter" || e.key === ",") { e.preventDefault(); add(); }
    else if (e.key === "Backspace" && !input.value && buckets[bucket].length) { buckets[bucket].pop(); renderChips(bucket); }
  });
  input.addEventListener("blur", add);
  box.addEventListener("click", e => {
    const i = e.target.dataset.chip;
    if (i !== undefined) { buckets[bucket].splice(+i, 1); renderChips(bucket); } else input.focus();
  });
}

for (const sw of document.querySelectorAll("#settings .switch")) {
  sw.addEventListener("click", () => sw.setAttribute("aria-checked", String(sw.getAttribute("aria-checked") !== "true")));
}

// The ruler shows where the caps sit on 0–100 as they are typed.
function renderRuler() {
  const value = k => Number(document.querySelector(`[data-threshold="${k}"]`).value);
  const marks = [["reserve_cap_far", "≥48h", "up"], ["reserve_cap_near", "12–48h", "down"], ["final_cap", "final", "up"], ["session_cap", "5-hour", "down"]];
  $("ruler").innerHTML = '<div class="track"></div>' + marks.map(([k, label, side]) => {
    const v = value(k);
    if (!(v >= 0 && v <= 100)) return "";
    return `<div class="tick${k === "session_cap" ? " session" : ""}" style="left: ${v}%"></div><div class="tag ${side}" style="left: ${v}%">${label} ${v}%</div>`;
  }).join("");
}
for (const el of document.querySelectorAll("[data-threshold]")) el.addEventListener("input", renderRuler);

function renderAccounts() {
  $("account-list").innerHTML = accounts.map((a, i) => `<li>
    <div class="who"><strong>${esc(short(a.auth_file))}</strong><span>${esc(a.auth_file)}${credentials.some(c => c.auth_file === a.auth_file) ? "" : " · not known to CPA"}</span></div>
    <div class="reserve"><button type="button" role="switch" class="switch" data-reserve="${i}" aria-checked="${a.reserve}" aria-label="Keeps a reserve"><span class="knob"></span></button>Reserve</div>
    <button type="button" class="icon-button" data-remove="${i}" aria-label="Remove ${esc(short(a.auth_file))}">
      <svg class="icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button></li>`).join("");
  const free = credentials.filter(c => !accounts.some(a => a.auth_file === c.auth_file));
  $("account-add").innerHTML = free.length
    ? free.map(c => `<option value="${esc(c.auth_file)}">${esc(c.email || short(c.auth_file))}${c.disabled ? " (disabled)" : ""}</option>`).join("")
    : '<option value="">No other Claude credentials in CPA</option>';
  $("account-add-button").disabled = !free.length;
}

$("account-list").addEventListener("click", e => {
  const reserve = e.target.closest("[data-reserve]"), remove = e.target.closest("[data-remove]");
  if (reserve) {
    const a = accounts[+reserve.dataset.reserve];
    a.reserve = !a.reserve;
    reserve.setAttribute("aria-checked", String(a.reserve));
  } else if (remove) {
    accounts.splice(+remove.dataset.remove, 1);
    renderAccounts();
  }
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
  const anchor = {};
  for (const el of document.querySelectorAll("[data-anchor]")) {
    if (el.getAttribute("role") === "switch") anchor[el.dataset.anchor] = el.getAttribute("aria-checked") === "true";
    else if (el.value.trim()) anchor[el.dataset.anchor] = el.value.trim();
  }
  return { accounts: accounts.map(a => ({ ...a })), buckets: { main: [...buckets.main], fable: [...buckets.fable] }, thresholds, anchor };
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

placeIndicator();
refreshStatus();
setInterval(() => { if (!document.hidden) refreshStatus(); }, 15000);
setInterval(() => { if (!document.hidden && tab === "history") loadHistory(); }, 60000);
