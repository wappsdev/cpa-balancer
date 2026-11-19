"use strict";

const ROUTES = "/v0/management/plugins/cpa-balancer";
const CONFIG = ROUTES + "/config"; // CPA's own plugin config API
const PREFIX = "enc::v2::";
const COLORS = ["#6366f1", "#f59e0b", "#10b981", "#ec4899", "#06b6d4", "#8b5cf6"];

const $ = id => document.getElementById(id);
const esc = s => String(s ?? "").replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
// How an account holds quota back, in words: "no reserve", "default reserve", "50% until 5 d before reset".
function reserveText(a) {
  if (a.reserve === "default") return "default reserve";
  if (a.reserve === "custom") return stepsText(a.reserve_steps || []);
  return "no reserve";
}
const emailOf = f => (f || "").replace(/^claude-[0-9a-f]+-/, "").replace(/\.json$/, "");

// Each account's name as the balancer reports it (its label, or one from its e-mail): the same
// name the HUD and its commands use. An account the status does not list yet shows its e-mail.
let names = {};
// Accounts whose plan has no 5-hour limit get no 5-hour setting (from the status; unknown: shown).
let noFiveHour = new Set();
let plans = {};
const planOf = f => ({ max_20x: "Ultra", max_5x: "Max", pro: "Pro" }[plans[f]] || "");
const hasFiveHour = f => !noFiveHour.has(f);
const short = f => names[f] || (f || "—").replace(/^claude-[0-9a-f]+-/, "").replace(/\.json$/, "");
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
// A length of seconds as "3d 13h", "5h 10m" or "12m".
function span(seconds) {
  const s = Math.max(0, Math.round(seconds));
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}
const when = t => `<span class="when" title="${esc(time(t))}">${esc(rel(t))}</span>`;
const num = n => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n);

// The Management Center persists its session under "cli-proxy-auth" (the key only with
// "Remember password"), XOR-obfuscated with a string of its host (format enc::v2::; it removes
// any older entry itself).
function managementKey() {
  let raw;
  try { raw = localStorage.getItem("cli-proxy-auth"); } catch { return null; }
  if (!raw) return null;
  try {
    let json = raw;
    if (raw.startsWith(PREFIX)) {
      const data = Uint8Array.from(atob(raw.slice(PREFIX.length)), c => c.charCodeAt(0));
      const key = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}`);
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
  names = Object.fromEntries((v.accounts || []).map(a => [a.auth_file, a.name]));
  noFiveHour = new Set((v.accounts || []).filter(a => a.five_hour === false).map(a => a.auth_file));
  plans = Object.fromEntries((v.accounts || []).map(a => [a.auth_file, a.plan]));
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
  $("recheck-all").disabled = false;
  $("rebalance").disabled = false;

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
  $("waste").innerHTML = wasteHtml(v.accounts, short);
  $("extra-models-head").hidden = $("extra-models").hidden = !v.extra_models?.length;
  $("extra-models").innerHTML = extraModelsHtml(v.extra_models, short);
  renderSessions(v.sessions);

  $("changes").innerHTML = (v.changes || []).slice().reverse().slice(0, 12).map(c => `<li>
      <span class="mark"></span><span class="chip">${esc(c.bucket)}</span>
      <span class="what">${esc(short(c.from))} → <strong>${esc(short(c.to))}</strong><span class="reason">${esc(c.reason)}</span></span>
      ${when(c.at)}</li>`).join("") || '<li><span class="empty">No changes yet.</span></li>';
}

const BUCKETS = [["main", "Main"], ["fable", "Fable"], ["session", "5-hour"]];

function accountCard(a, v) {
  const activeIn = [["main", "main"], ["fable", "Fable"]].filter(([b]) => !v.paused && v.decision[b].account === a.auth_file).map(([b, l]) => l + (v.decision[b].credits ? " (credits)" : ""));
  // a plan without a 5-hour or a Fable limit has no row for it
  const rows = BUCKETS.filter(([b]) => !(b === "session" && a.five_hour === false) && !(b === "fable" && a.fable === false)).map(([b, label]) => {
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
      <span class="chip"${a.reserve === "custom" ? ` title="${esc(reserveText(a))}"` : ""}>${a.reserve === "custom" ? "custom reserve" : esc(reserveText(a))}</span>
      ${a.plan ? `<span class="chip">${esc({ max_20x: "Ultra", max_5x: "Max", pro: "Pro" }[a.plan] || a.plan)}</span>` : ""}
      ${a.extra_usage ? '<span class="chip">extra usage on</span>' : ""}
      ${a.listed ? "" : '<span class="chip bad">not listed by CPA</span>'}
    </div>
    ${rows}
    ${accountProjectsHtml(a, v.affinity)}
    <div class="account-foot">
      <span>${a.last_anchor ? `Last anchor ${when(a.last_anchor)} · <code>${esc(a.anchor_model)}</code>` : "No anchor yet"}</span>
      <span>${sources ? `source: ${esc(sources)}` : ""}</span>
      <button type="button" class="button" data-recheck="${esc(a.auth_file)}"><svg class="icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3h-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg><span>Recheck</span></button>
    </div>
  </article>`;
}

// The models outside CPA's catalog, each with the accounts it is pinned to, in the order they are
// tried. A request for one goes only to these accounts; with none ready, it is refused.
const EXTRA_STATE = { ready: "", "held back": "warn", disabled: "bad", "not loaded": "bad" };
function extraModelsHtml(models, name) {
  return (models || []).map(m => {
    const accounts = m.accounts || [];
    const chips = accounts.map((a, i) => {
      const level = EXTRA_STATE[a.state] ?? "bad";
      return `<span class="chip${level ? " " + level : ""}"><b>${i + 1}</b> ${esc(name(a.auth_file))}${a.state === "ready" ? "" : " · " + esc(a.state)}</span>`;
    }).join("");
    return `<article class="card account-card">
    <div class="account-head">
      <span class="name">${esc(m.display_name)}</span>
      <code>${esc(m.id)}</code>
      ${accounts.some(a => a.state === "ready") ? "" : '<span class="chip bad">refused: no account ready</span>'}
    </div>
    <div class="order">${chips}</div>
  </article>`;
  }).join("");
}

// The room each account left unused at its last weekly resets, in Pro plans' points (100 minus the
// last reading, times the plan's size), with the totals of the last 7 and 30 days. Read only.
function wasteHtml(accounts, name) {
  const list = (accounts || []).map(a => ({ file: a.auth_file, week: a.waste?.week || 0, month: a.waste?.month || 0, resets: a.waste?.resets || [] }));
  if (!list.some(a => a.resets.length)) return '<p class="empty">No weekly reset seen yet.</p>';
  const pts = n => String(Math.round(n || 0));
  const day = t => new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const reset = r => `<span class="chip" title="${esc(`last reading ${Math.round(r.util)}% at ${new Date(r.observed_at).toLocaleString()}`)}">${esc(day(r.resets_at))}: ${pts(r.wasted)} pts · ${Math.round(r.util)}%</span>`;
  const rows = list.map(a => `<tr><th scope="row">${esc(name(a.file))}</th><td>${pts(a.week)}</td><td>${pts(a.month)}</td>
    <td class="left">${a.resets.slice(0, 4).map(reset).join(" ") || '<span class="faint">none yet</span>'}</td></tr>`).join("");
  const sum = k => list.reduce((n, a) => n + a[k], 0);
  return `<table class="sessions-table"><thead><tr><th class="left">Account</th><th>7 days</th><th>30 days</th><th class="left">Last resets: wasted · last reading</th></tr></thead>
    <tbody>${rows}<tr><th scope="row">All</th><td>${pts(sum("week"))}</td><td>${pts(sum("month"))}</td><td></td></tr></tbody></table>`;
}

async function refreshStatus() {
  try {
    renderStatus(await api("GET", ROUTES + "/status"));
    showError(null);
  } catch (e) { showError(e); }
  try { renderOutlook((await api("GET", ROUTES + "/outlook")).week); } catch { $("outlook").hidden = true; }
}

// What the outlook says, in words: a verdict chip and the lines under it. Pure, so it is tested.
function outlookView(o) {
  const clock = t => new Date(t).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
  const pct = x => x >= 10 ? "1000%+" : `${Math.round(x * 100)}%`;
  // the pace's method: typical, else the fastest of the others, else the week's own
  const fastest = m => `${m}, the fastest method (typical not measured)`;
  const BASIS = { typical: "typical, a heavy but normal day", peak: fastest("peak"), trend: fastest("trend"), now: fastest("now"), "7d": "the last 7 days, no method measured" };
  const lines = [];
  let chip = { tone: "", text: "No pace yet" };
  if (o.verdict === "lasts") {
    chip = { tone: "accent", text: "Lasts" };
    lines.push(`At this pace the room lasts until the resets bring more. It would still last at ${pct(o.lasts)} of this pace.`);
  } else if (o.verdict === "tight") {
    chip = { tone: "warn", text: "Tight" };
    lines.push(`At this pace the room lasts, but only up to ${pct(o.lasts)} of it: a heavier day or two runs it out.`);
  } else if (o.verdict === "out") {
    chip = { tone: "bad", text: "Runs out" };
    const dry = o.dry;
    lines.push(`At this pace the room runs out ${clock(o.runs_out)}${dry ? `, and stays out ${span(dry.seconds)} until ${dry.refill} resets (${clock(dry.at)})` : ""}. Work about ${100 - Math.round(o.lasts * 100)}% slower to last.`);
    if (dry && dry.next) lines.push(dry.next.lasts ? "The refill after that lasts at this pace." : `After that refill the room runs out again, for ${span(dry.next.seconds)}.`);
  } else {
    lines.push("Not enough use in the last week to tell.");
  }
  if (o.verdict !== "out" && o.horizon) {
    lines.push(o.runs_out
      ? `At this pace the room runs out ${clock(o.runs_out)} (counting the resets before it).`
      : `At this pace the room lasts past ${clock(o.horizon)} (counting the resets before it).`);
  }
  if (o.pace) {
    const p = o.pace;
    lines.push(p.source === "measured"
      ? `Pace: ${p.per_hour.toFixed(1)} Pro-plan points an hour, by ${BASIS[p.basis] || BASIS["7d"]}${p.active_per_hour > 0 ? ` (${p.active_per_hour.toFixed(1)} while working, about ${p.hours_per_day.toFixed(0)} h a day)` : ""}.`
      : `Pace: ${p.per_hour.toFixed(1)} Pro-plan points an hour, each window's own average (too little recent work to measure).`);
  }
  return { chip, lines, used: Math.round(o.percent) };
}

// The week at each method's pace (`o.by_method`), for Σ, the account in use (`v`, the status) and
// the next ones in the balancer's order: each cell the pace in that row's own percent an hour and
// its ⚑ time, coloured as the outlook is (red only for a run-out within the week looked at). Every
// method has its column; one not measured is a dash with why. Notes say how each is worked out.
// Null without methods. Pure, so it is tested.
function methodsTable(o, v, now, name) {
  if (!o.by_method?.length) return null;
  const HOW = {
    peak: "the heaviest 24 hours of the last 7 days, as if every day were like it",
    typical: "a heavy but normal day — the 75th percentile of the last 7 days' working days",
    trend: "the last day's pace plus half of the change from the last 3 days' pace (lower when use slows down)",
    now: "the pace while working in the last 5 hours × the hours worked per day",
  };
  const ways = o.by_method; // peak, typical, trend, now: the balancer gives all four
  const dash = w => ({ pace: "", flag: "—", stops: false, tone: "", title: w.reason });
  const accounts = v?.accounts || [];
  const active = v?.decision?.main?.account;
  const known = accounts.length > 0 && accounts.every(a => a.weight > 0);
  const weight = f => (known ? accounts.find(a => a.auth_file === f)?.weight : 1) || 1;
  // all the room in Pro-plan points: each account's weight times its cap once reserves are released
  const room = accounts.reduce((sum, a) => sum + weight(a.auth_file) * (v.final_cap || a.caps?.main || 100), 0) || 1;
  const until = t => span((Date.parse(t) - now) / 1000);
  const rate = (perHour, per) => `${(perHour * 100 / per).toFixed(1)}%/h`;
  const sum = w => {
    const tone = w.verdict === "out" ? "bad" : w.verdict === "tight" ? "warn" : w.verdict === "lasts" && w.lasts < 1.4 ? "ok" : "";
    const stops = w.verdict === "out";
    const dry = stops && w.dry ? `, out ${span(w.dry.seconds)} until ${name(w.dry.refill)} resets; about ${Math.max(1, Math.round((1 - w.dry.lasts) * 100))}% slower lasts` : "";
    return w.runs_out
      ? { pace: rate(w.per_hour, room), flag: `⚑${until(w.runs_out)}`, stops, tone, title: `The room runs out ${new Date(w.runs_out).toLocaleString()}${dry}` }
      : { pace: rate(w.per_hour, room), flag: `⚑>${until(o.horizon)}`, stops: false, tone, title: "The room lasts the two weeks looked at" };
  };
  const one = (w, f) => {
    const h = (w.accounts || []).find(a => a.key === f) || {};
    const pace = rate(w.per_hour, weight(f) * 100);
    if (h.handoff) {
      const stops = !!h.exhausts;
      return { pace, flag: `⚑${until(h.handoff)}`, stops, tone: stops && w.verdict === "out" ? "bad" : "warn",
        title: `${stops ? "Work stops" : "Hands off"} ${new Date(h.handoff).toLocaleString()}` };
    }
    // no handoff within its current window: the account resets first. The balancer's own run says
    // when it would reach its cap at this pace in a later window (`capped`); none: it never does
    // within the two weeks looked at. Warn when it hands off at 1.2 times the pace, else ok.
    if (!h.key) return { pace, flag: "—", stops: false, tone: "", title: "Not in the balancer's run" };
    const faster = h.margin === 1.2 || h.margin === 1.4 ? ` Within its window it hands off at ${h.margin}× the pace.` : "";
    const tone = h.margin === 1.2 ? "warn" : "ok";
    if (h.capped) return { pace, flag: `⚑${until(h.capped)}`, stops: false, tone, title: `It resets first. At this pace it would reach its cap ${new Date(h.capped).toLocaleString()}.${faster}` };
    return { pace, flag: "✓", stops: false, tone: "ok", title: `It resets first and does not reach its cap in the two weeks looked at.${faster}` };
  };
  const order = [...new Set([active, ...(v?.decision?.main_order || [])].filter(Boolean))];
  return {
    columns: ways.map(w => ({ name: w.name, used: !!w.used, ...(w.reason ? { reason: w.reason } : {}) })),
    rows: [
      { kind: "sum", label: "Σ", cells: ways.map(w => (w.reason ? dash(w) : sum(w))) },
      ...order.map(f => ({ kind: f === active ? "active" : "next", label: name(f), cells: ways.map(w => (w.reason ? dash(w) : one(w, f))) })),
    ],
    notes: [
      ...ways.map(w => `${w.name}: ${HOW[w.name]}${w.reason ? ` — not measured: ${w.reason}` : ""}`),
      "In an account's row: yellow ⚑ and a time when it hands off at that pace. When it resets first: ⚑ and the time it would reach its cap at that pace, from the balancer's own run through the two weeks looked at (green; dim yellow when it would hand off at 1.2 times the pace), or ✓ when it does not reach its cap in them. — when not measured.",
      "Σ and the outlook above use typical (falling back to the fastest method when typical cannot be measured).",
    ],
  };
}

function renderOutlook(o) {
  const card = $("outlook");
  if (!o || o.verdict === undefined) { card.hidden = true; return; }
  const v = outlookView(o);
  const t = methodsTable(o, view, Date.now(), short);
  const cell = (c, used) => `<td class="${used ? "used" : ""}" title="${esc(c.title)}"><span class="pace">${esc(c.pace)}</span>`
    + `<span class="flag ${c.tone}">${esc(c.flag)}${c.stops ? `<span class="${c.tone === "bad" ? "" : "faint"}"> ∅</span>` : ""}</span></td>`;
  const table = !t ? "" : `<table class="methods">
      <thead><tr><th></th>${t.columns.map(c => `<th class="${c.used ? "used" : ""}"${c.reason ? ` title="not measured: ${esc(c.reason)}"` : ""}>${esc(c.name)}${c.used ? ' <span class="used-mark">used</span>' : ""}</th>`).join("")}</tr></thead>
      <tbody>${t.rows.map(r => `<tr class="${r.kind}"><th scope="row">${r.kind === "active" ? "● " : r.kind === "next" ? "○ " : ""}${esc(r.label)}</th>${r.cells.map((c, i) => cell(c, t.columns[i].used)).join("")}</tr>`).join("")}</tbody>
    </table>
    <ul class="method-notes">${t.notes.map(n => `<li>${esc(n)}</li>`).join("")}</ul>`;
  card.hidden = false;
  card.innerHTML = `<div class="eyebrow">All accounts · the week ahead</div>
    <div class="outlook-head"><span class="chip ${v.chip.tone}">${esc(v.chip.text)}</span><span class="reason">${v.used}% of all the weekly room used</span></div>
    ${v.lines.map(l => `<p class="reason">${esc(l)}</p>`).join("")}${table}`;
}

// ---------- sessions ----------

// What the sessions card shows for one window: rows nested under their parent, each with its
// tokens and share. A session's tokens are input, output and cache writes (cache reads are
// shown apart); a parent's count its subagents' too. Pure, so it is tested.
const MAX_SESSION_DEPTH = 8;
function sessionsView(w, picks) {
  const tokens = t => (t?.input_tokens || 0) + (t?.output_tokens || 0) + (t?.cache_creation_tokens || 0);
  const total = { requests: w.total?.requests || 0, tokens: tokens(w.total) };
  const percent = (n, of) => of ? Math.round(n * 100 / of) : 0;
  const split = m => {
    const entries = Object.entries(m || {}).map(([key, t]) => ({ key, n: tokens(t) || t.requests || 0 })).sort((a, b) => b.n - a.n || (a.key < b.key ? -1 : 1));
    const sum = entries.reduce((s, e) => s + e.n, 0);
    return entries.map(e => ({ key: e.key, share: sum ? percent(e.n, sum) : 100 }));
  };
  const rows = [];
  const add = (s, depth) => {
    rows.push({
      depth, id: s.id, label: s.id ? s.id.slice(0, 8) : "no session", none: s.id === "", last: s.last,
      requests: s.total?.requests || 0, tokens: tokens(s.total), ownTokens: tokens(s.own), cacheRead: s.total?.cache_read_tokens || 0,
      share: percent(tokens(s.total), total.tokens), accounts: split(s.accounts),
      // use of the Ultra accounts' limit windows in the 5 hours and the 7 days (null: not known), and tokens on
      // accounts with no meter, which those figures leave out; and each model the family used, by first seen
      share5: s.planShare5h ?? null, share7: s.planShare7d ?? null, off5: s.elsewhere5h || 0, off7: s.elsewhere7d || 0, lines: s.model_lines || [],
      // what it used of each account in both windows: tokens and the percent of that account's own limit window
      byAccount: s.by_account || {},
      // where its last request went and why (the balancer keeps it in memory only); a subagent the balancer
      // saw no request of, such as one CPA names from the request body, shows its main session's
      pick: picks?.[s.id] ?? picks?.[s.id.split("/")[0]] ?? null,
    });
    // The balancer folds anything deeper into its ancestor, so a deeper row is only defence: its use is already in this row's total.
    if (depth + 1 < MAX_SESSION_DEPTH) for (const c of s.subagents || []) add(c, depth + 1);
  };
  for (const s of w.sessions || []) add(s, 0);
  return { total, rows, other: { sessions: w.other?.sessions || 0, tokens: tokens(w.other?.total) } };
}

// Why a session is on its account, in words.
const REASONS = {
  project_home: "project home", folder_pin: "folder pin", session_pin: "session pin", fallback: "fallback after cap",
  folder_pin_fallback: "folder pin: account full, fallback", session_pin_fallback: "session pin: account full, fallback", no_project: "no project",
  fill: "fill", running: "running session: stays where it started",
};
const reasonText = r => REASONS[r] || r;

// The card has four levels, all closed when the page opens: a project (a repository, or the group
// of sessions with none), its git worktrees, the sessions of the project and of each worktree, and
// each session's subagents. The open ones are remembered in localStorage under their keys:
// "p:<project folder>", "n:" for no project, "w:<worktree folder>", "s:<session id>".
const OPEN_KEY = "cpa-balancer.sessions-open";
const MAX_OPEN = 200;
function readOpen() {
  try {
    const keys = JSON.parse(localStorage.getItem(OPEN_KEY));
    return new Set(Array.isArray(keys) ? keys.filter(k => typeof k === "string").slice(-MAX_OPEN) : []);
  } catch { return new Set(); }
}
function writeOpen(open) {
  try { localStorage.setItem(OPEN_KEY, JSON.stringify([...open].slice(-MAX_OPEN))); } catch { /* storage can be blocked: the state then lasts until the next reload */ }
}

// What a row used of ONE Ultra account's limit window, in percent: "~3.2%", "<0.1%", or "—" when it
// cannot be priced. Anything that is not a plain number is "—", never raw text.
function planShareText(p) {
  if (typeof p !== "number" || !Number.isFinite(p) || p < 0) return "—";
  return p === 0 ? "~0.0%" : p < 0.05 ? "<0.1%" : `~${p.toFixed(1)}%`;
}

// A row's figures for both windows, whatever window is in view; only the tokens on accounts with no
// meter are those of the window in view.
function ultraShareOf(share5, share7, off5, off7, span) {
  return { five: share5, week: share7, elsewhere: span === "week" ? off7 : off5 };
}

// What the status says about the Ultra accounts' meters, ready to show: null when it says nothing.
function meterInfo(v) {
  if (!v?.meter) return null;
  const accounts = (v.accounts || []).filter(a => a.meter);
  const hours = [...new Set(accounts.map(a => a.meter.window_hours).filter(h => h > 0).map(h => Math.round(h * 10) / 10))];
  const apart = [...new Set((v.accounts || []).filter(a => !a.meter && a.plan_label).map(a => a.plan_label))];
  return { label: v.meter.label, basis: v.meter.basis, fable: v.meter.fable_models || [], accounts, hours, apart };
}

// The header line: what a million units of use cost each Ultra account, learned from its own meter.
function meterLine(m, name) {
  if (!m || !m.accounts.length) return "";
  const parts = m.accounts.map(a => a.meter.known
    ? `${name(a.auth_file)} ${a.meter.per_million.toFixed(2)}%${a.meter.from_previous ? " (previous window)" : ""}`
    : `${name(a.auth_file)} not learned yet`);
  // a rise of the meter with no use of the proxy near it: shown apart, and not in any row
  const outside = m.accounts.filter(a => a.meter.outside > 0).map(a => `${Math.round(a.meter.outside)} points of ${name(a.auth_file)}`);
  return `${m.label}: 1M units of use ≈ ${parts.join(" · ")} of a limit window${m.hours.length ? ` · window ${m.hours.join(" / ")} h` : ""}${outside.length ? ` · outside the proxy: ${outside.join(", ")} (in no row)` : ""}`;
}

// The line under the Sessions header: what the "~% Ultra" column is based on, in words. The window
// length and the unit are the ones the status serves; with no window measured yet it says only "limit window".
function ultraBasisLine(m) {
  if (!m) return "";
  const length = m.hours.length === 1 ? `${m.hours[0] % 24 === 0 ? `${m.hours[0] / 24}-day` : `${m.hours[0]}-hour`} limit` : "limit window";
  return `~% ${m.label}: share of one ${m.label} account's ${length}, learned from each account's own meter (${m.basis}; advisor calls not counted)`;
}

// The tooltip of the "~% Ultra" cells: it is an estimate, where it comes from, and what it leaves out.
function meterTip(m, elsewhere) {
  if (!m) return "";
  const parts = [
    `An estimate of how much of one ${m.label} account's limit window this used.`,
    `Learned from each ${m.label} account's own meter: its percent so far over what it has served since its window opened, so the rows add up to the meter. One unit of use = ${m.basis}. Accounts are not mixed: a row is priced at the factor of the account that served it.`,
    "A window with under 5 points on the meter takes the previous window's factor; with none, the figure is —.",
    "Models are not weighted apart: per-model weights are not learned yet.",
    "Advisor calls are not counted.",
  ];
  if (m.hours.length) parts.push(`Window: ${m.hours.join(" / ")} h, measured between resets.`);
  if (elsewhere > 0) parts.push(`On ${m.apart.length ? m.apart.join(" / ") : "other accounts"}: ${num(elsewhere)} tokens (not in this percent).`);
  return parts.join(" ");
}

// The "~% Ultra" cell: both windows, "5h ~a% · 7d ~b%", whatever window is in view.
function planCellHtml(plan = { five: null, week: null, elsewhere: 0 }, m = null) {
  return `<td title="${esc(meterTip(m, plan.elsewhere))}">${esc(`5h ${planShareText(plan.five)} · 7d ${planShareText(plan.week)}`)}</td>`;
}

// What a row used of each account, in both windows, as a compact line under the row's name:
// "wapps 5h ~2.1% · 7d ~3.4% · gmail 7d ~0.6%". An open row adds the tokens (on the meter's basis)
// of each window: "wapps 5h 18k tok ~2.1% · …". The percent is of that account's own limit window
// ("—": the account has no factor yet). `uses` is the status's by-account map; an account that
// served nothing in either window has no entry. Account names are escaped. Pure, so it is tested.
function accountUseHtml(uses, name, open = false) {
  const entries = Object.entries(uses || {}).filter(([, u]) => u && (u.tokens5h > 0 || u.tokens7d > 0))
    .sort(([ka, a], [kb, b]) => (b.tokens7d || 0) - (a.tokens7d || 0) || (b.tokens5h || 0) - (a.tokens5h || 0) || (ka < kb ? -1 : ka > kb ? 1 : 0));
  if (!entries.length) return "";
  const windows = u => [["5h", u.tokens5h, u.share5h], ["7d", u.tokens7d, u.share7d]].filter(([, tokens]) => tokens > 0)
    .map(([label, tokens, share]) => `${label} ${open ? `${num(tokens)} tok ` : ""}${esc(planShareText(share))}`).join(" · ");
  const items = entries.map(([account, u]) => `<span class="acct-item"><span class="acct-name">${esc(name(account))}</span> ${windows(u)}</span>`);
  return `<div class="acct-use faint">${items.join(" · ")}</div>`;
}

// The account card's list of the projects that used the account most in the last 7 days, with the
// tokens and the percent of this account's own limit window. The balancer sums it from every project.
// The project "" is what has no project; folder labels are escaped. Pure, so it is tested.
function accountProjectsHtml(a, aff) {
  if (!a?.by_project?.length) return "";
  const items = a.by_project.map(p => `<li><span class="stitle${p.project === "" ? " none" : ""}" title="${esc(p.project)}">${p.project === "" ? "no project" : esc(folderLabel(aff, p.project))}</span> <span class="faint">${num(p.tokens7d)} tok · ${esc(planShareText(p.share7d))}</span></li>`);
  const more = a.more_projects > 0 ? `<li class="faint">+${a.more_projects} more</li>` : "";
  return `<div class="acct-projects"><div class="eyebrow">By project · 7 days</div><ul>${items.join("")}${more}</ul></div>`;
}

// Each model a project or session used, by first seen: when it was first and last seen, and its
// tokens, with what its subagents ran apart. Models of the Fable limit are marked.
function modelLinesHtml(lines, fable) {
  if (!lines?.length) return "";
  const tokens = t => (t?.input_tokens || 0) + (t?.output_tokens || 0) + (t?.cache_creation_tokens || 0);
  const whole = lines.reduce((n, l) => n + tokens(l.own) + tokens(l.subagents), 0);
  return `<div class="mlines">${lines.map(l => {
    const sub = tokens(l.subagents), all = tokens(l.own) + sub;
    return `<div class="mline"><span class="mname" title="${esc(l.model)}">${esc(l.model || "no model name")}</span>${(fable || []).includes(l.model) ? ' <span class="chip">Fable limit</span>' : ""} <span class="faint">${when(l.first)} – ${when(l.last)} · ${num(all)}${sub ? ` (${num(sub)} in subagents)` : ""} · ${whole ? Math.round(all * 100 / whole) : 0}% of the row</span></div>`;
  }).join("")}</div>`;
}

// Whether a window's figure holds any use.
function usedIn(u) {
  return !!(u && (u.tokens || u.requests));
}

// What a list of session families (see projectGroups) adds up to.
function listedUse(sessions) {
  const listed = { requests: 0, tokens: 0, last: "" };
  for (const { row } of sessions) {
    listed.requests += row.requests;
    listed.tokens += row.tokens;
    if (row.last && (!listed.last || Date.parse(row.last) > Date.parse(listed.last))) listed.last = row.last;
  }
  return listed;
}

// The figures of a project row or a worktree row in the window in view: the balancer's own totals
// for the window (`used`), or what the listed sessions add up to when it has none; the other window
// beside it; and the Ultra percent of both windows (`p` is the balancer's entry for the row).
function rowFigures(v, span, used, listed, p) {
  const other = span === "week" ? "five_hour" : "week";
  const u = used || listed, o = p?.[other];
  return {
    alt: o ? { label: other === "week" ? "7 d" : "5 h", requests: o.requests || 0, tokens: o.tokens || 0 } : null,
    requests: u.requests, tokens: u.tokens, last: u.last || "", share: v.total.tokens ? Math.round(u.tokens * 100 / v.total.tokens) : 0,
    // a row with no use in a window has no figure for it, like a session that is in one window only
    plan: ultraShareOf(usedIn(p?.five_hour) ? p.planShare5h ?? null : null, usedIn(p?.week) ? p.planShare7d ?? null : null, p?.elsewhere5h || 0, p?.elsewhere7d || 0, span),
    lines: u.models || [], accounts: p?.accounts || {},
  };
}

// The rows of sessionsView, grouped by project and sorted by use, the largest first. A project is a
// repository: a git worktree's folder (aff.project_of) is a sub-row `trees` of its main repository's
// project, which counts its use too. A project's totals are the balancer's own (summed from every
// session, so the week's cut list does not make them short); what the listed sessions leave out is
// `unlisted`, of the project's own folder (the worktrees' totals are not in it) or of a worktree.
// Pure, so it is tested.
function projectGroups(v, aff, span) {
  const projectOf = folder => aff?.project_of?.[folder] ?? folder;
  const groups = new Map();
  const group = project => {
    if (!groups.has(project)) groups.set(project, { project, sessions: [], trees: new Map() });
    return groups.get(project);
  };
  const tree = (project, folder) => {
    const g = group(project);
    if (!g.trees.has(folder)) g.trees.set(folder, { folder, sessions: [] });
    return g.trees.get(folder);
  };
  // the list a folder's sessions go in: its project's own, or its worktree's
  const listOf = folder => projectOf(folder) === folder ? group(folder).sessions : tree(projectOf(folder), folder).sessions;
  const withPlan = r => ({ ...r, plan: ultraShareOf(r.share5, r.share7, r.off5, r.off7, span) });
  let family = null;
  for (const r of v.rows) {
    if (r.depth === 0) {
      family = { row: withPlan(r), subs: [] };
      listOf(aff?.session_projects?.[r.id] ?? r.pick?.project ?? "").push(family);
    } else if (family) family.subs.push(withPlan(r));
  }
  const used = new Map(), usage = new Map((aff?.projects || []).map(p => [p.project, p])), worktrees = new Map();
  for (const p of aff?.projects || []) {
    if (usedIn(p[span])) { group(p.project); used.set(p.project, p[span]); }
    for (const w of p.worktrees || []) {
      worktrees.set(w.project, w);
      if (usedIn(w[span])) tree(p.project, w.project);
    }
  }
  const byUse = list => list.sort((a, b) => b.row.tokens - a.row.tokens || (a.row.id < b.row.id ? -1 : 1));
  return [...groups.values()].map(g => {
    byUse(g.sessions);
    const subs = [...g.trees.values()].map(t => {
      byUse(t.sessions);
      const listed = listedUse(t.sessions), w = worktrees.get(t.folder);
      const figures = rowFigures(v, span, usedIn(w?.[span]) ? w[span] : null, listed, w);
      return {
        ...figures, key: "w:" + t.folder, folder: t.folder, name: w?.name || aff?.names?.[t.folder] || t.folder, path: w?.path || t.folder,
        inferred: aff?.project_inferred?.[t.folder] === true, sessions: t.sessions, unlisted: Math.max(0, figures.tokens - listed.tokens),
      };
    }).sort((a, b) => b.tokens - a.tokens || (a.folder < b.folder ? -1 : a.folder > b.folder ? 1 : 0));
    const figures = rowFigures(v, span, used.get(g.project) || null, listedUse([...g.sessions, ...subs.flatMap(w => w.sessions)]), usage.get(g.project));
    const inTrees = subs.reduce((n, w) => n + w.tokens, 0);
    return {
      ...figures, key: g.project === "" ? "n:" : "p:" + g.project, project: g.project, none: g.project === "", sessions: g.sessions, trees: subs,
      unlisted: Math.max(0, figures.tokens - inTrees - listedUse(g.sessions).tokens),
    };
  }).sort((a, b) => b.tokens - a.tokens || (a.project < b.project ? -1 : a.project > b.project ? 1 : 0));
}

// A folder's short name from the balancer (the folder itself when it has none); the full folder goes in the title.
const folderLabel = (aff, key) => aff?.names?.[key] || key;

// A project's pin and home, as the balancer reports them.
function homeInfo(project, aff) {
  return {
    pin: (aff?.folder_pins || []).find(p => p.key === project) || null,
    home: (aff?.homes || []).find(h => h.project === project) || null,
  };
}

function toggleHtml(key, open) {
  return `<button type="button" class="toggle" data-toggle="${esc(key)}" aria-expanded="${open}" aria-label="${open ? "Hide" : "Show"} contents">${open ? "▾" : "▸"}</button>`;
}

// The account choice of a project or a session: "no pin", or one account. Choosing sets the pin at
// once; "no pin" clears it. A pin set in the config is shown and cannot be changed here.
function pinSelectHtml(kind, key, pin, accounts, name) {
  const list = [...accounts];
  if (pin && !list.some(a => a.auth_file === pin.account)) list.push({ auth_file: pin.account, name: name(pin.account) });
  const options = list.map(a => `<option value="${esc(a.auth_file)}"${pin?.account === a.auth_file ? " selected" : ""}>${esc(a.name)}</option>`).join("");
  const locked = pin?.source === "config";
  return `<select class="select small" data-pin-select="${kind}" data-key="${esc(key)}" aria-label="Pin to an account"${locked ? ' disabled title="set in the config"' : ""}>
    <option value=""${pin ? "" : " selected"}>no pin</option>${options}</select>${locked ? ' <span class="faint">config</span>' : ""}`;
}

// Why fill chose an account, as it was when it placed the project (at): its reset and its room.
function fillText(fill, at) {
  const hours = Math.max(0, Math.round((Date.parse(fill.resets_at) - Date.parse(at)) / 3600000));
  return `fill: resets in ${hours}h with ${Math.round(fill.room)}% room`;
}

// Where a session's last request went and why.
function placementText(pick, name) {
  if (!pick) return '<span class="faint">—</span>';
  const why = pick.reason === "fill" && pick.fill ? fillText(pick.fill, pick.at) : reasonText(pick.reason);
  return `<span class="chip${/fallback/.test(pick.reason) ? " warn" : ""}">${esc(name(pick.account))}</span> <span class="faint">${esc(why)}</span>`;
}

// A project's account and why: its folder pin when there is one, else its home.
function homeText(project, aff, name) {
  const { pin, home } = homeInfo(project, aff);
  if (pin) {
    return `<span class="chip accent">${esc(name(pin.account))}</span> <span class="faint">folder pin</span>${pin.full ? ' <span class="chip warn">account full: falling back</span>' : ""}`;
  }
  if (home) {
    return `<span class="chip">${esc(name(home.account))}</span> <span class="faint">project home${home.fill ? " · " + esc(fillText(home.fill, home.since)) : ""}</span>${home.full ? ' <span class="chip warn">full: moves on the next request</span>' : ""}`;
  }
  return '<span class="faint">no home yet</span>';
}

function projectRowHtml(g, open, aff, name, accounts, m = null, fable = []) {
  const label = g.none ? '<span class="stitle none">no project</span>' : `<span class="stitle" title="${esc(g.project)}">${esc(folderLabel(aff, g.project))}</span>`;
  const count = g.sessions.length + g.trees.reduce((n, w) => n + w.sessions.length, 0);
  const partial = g.unlisted > 0 || g.trees.some(w => w.unlisted > 0);
  const trees = g.trees.length ? ` · ${g.trees.length} worktree${g.trees.length === 1 ? "" : "s"}` : "";
  const pin = homeInfo(g.project, aff).pin;
  const alt = n => g.alt ? ` <span class="faint">· ${g.alt.label} ${n(g.alt)}</span>` : "";
  return `<tr class="project">
    <th scope="row">${toggleHtml(g.key, open)}${label} <span class="faint">${count}${partial ? "+" : ""} session${count === 1 && !partial ? "" : "s"}${trees}</span>${modelLinesHtml(g.lines, fable)}${accountUseHtml(g.accounts, name, open)}</th>
    <td>${g.last ? when(g.last) : "—"}</td><td>${g.requests}${alt(a => a.requests)}</td><td>${num(g.tokens)}${alt(a => num(a.tokens))}</td><td>${g.share}%</td>${planCellHtml(g.plan, m)}
    <td class="left">${g.none ? '<span class="faint">CPA\'s own order</span>' : homeText(g.project, aff, name)}</td>
    <td class="left">${g.none ? "" : pinSelectHtml("folder", g.project, pin, accounts, name)}</td></tr>`;
}

// A git worktree's sub-row under its project: its own totals, the account it has of its own pin
// (else it follows the project), and the choice to pin this folder alone. The name is the folder's
// last path segment; the full path is in the title.
function worktreeRowHtml(t, open, aff, name, accounts, m = null, fable = []) {
  const pin = homeInfo(t.folder, aff).pin;
  const count = t.sessions.length;
  const alt = n => t.alt ? ` <span class="faint">· ${t.alt.label} ${n(t.alt)}</span>` : "";
  return `<tr class="worktree">
    <th scope="row">${toggleHtml(t.key, open)}<span class="stitle" title="${esc(t.path)}">${esc(t.name)}</span> <span class="chip">worktree</span>${t.inferred ? ' <span class="chip faint" title="Its folder is gone; it is grouped here because the other worktrees in the same folder belong to this repository.">inferred</span>' : ""} <span class="faint">${count}${t.unlisted > 0 ? "+" : ""} session${count === 1 && !t.unlisted ? "" : "s"}</span>${modelLinesHtml(t.lines, fable)}${accountUseHtml(t.accounts, name, open)}</th>
    <td>${t.last ? when(t.last) : "—"}</td><td>${t.requests}${alt(a => a.requests)}</td><td>${num(t.tokens)}${alt(a => num(a.tokens))}</td><td>${t.share}%</td>${planCellHtml(t.plan, m)}
    <td class="left">${pin ? homeText(t.folder, aff, name) : '<span class="faint">with the project</span>'}</td>
    <td class="left">${pinSelectHtml("folder", t.folder, pin, accounts, name)}</td></tr>`;
}

function sessionRowHtml(f, open, aff, name, accounts, m = null, fable = [], deep = false) {
  const r = f.row;
  const used = r.accounts.map(a => name(a.key) + (r.accounts.length > 1 ? ` ${a.share}%` : "")).join(" · ");
  const detail = [`own ${num(r.ownTokens)}`, `cache reads ${num(r.cacheRead)}`, used && `accounts ${used}`].filter(Boolean).join(" · ");
  const pin = (aff?.session_pins || []).find(p => p.key === r.id) || null;
  const subs = f.subs.length;
  return `<tr class="session${deep ? " deep" : ""}${r.none ? " none" : ""}">
    <th scope="row" title="${esc(r.id)}">${subs ? toggleHtml("s:" + r.id, open) : '<span class="toggle-space"></span>'}<span class="sid">${esc(r.label)}</span>${subs ? ` <span class="faint">${subs} subagent${subs === 1 ? "" : "s"}</span>` : ""}${modelLinesHtml(r.lines, fable)}${accountUseHtml(r.byAccount, name, open || !subs)}</th>
    <td>${r.last ? when(r.last) : "—"}</td><td>${r.requests}</td><td title="${esc(detail)}">${num(r.tokens)}</td><td>${r.share}%</td>${planCellHtml(r.plan, m)}
    <td class="left">${placementText(r.pick, name)}</td>
    <td class="left">${r.id ? pinSelectHtml("session", r.id, pin, accounts, name) : ""}</td></tr>`;
}

function subRowHtml(r, name, m = null, fable = [], extra = 0) {
  return `<tr class="sub">
    <th scope="row" title="${esc(r.id)}" style="padding-left: ${28 + extra + r.depth * 14}px"><span class="faint">↳ </span><span class="sid">${esc(r.label)}</span>${modelLinesHtml(r.lines, fable)}${accountUseHtml(r.byAccount, name, true)}</th>
    <td>${r.last ? when(r.last) : "—"}</td><td>${r.requests}</td><td title="${esc(`cache reads ${num(r.cacheRead)}`)}">${num(r.tokens)}</td><td>${r.share}%</td>${planCellHtml(r.plan, m)}
    <td class="left">${placementText(r.pick, name)}</td><td></td></tr>`;
}

// The table body: every project, and below an open one its own sessions and a sub-row per worktree,
// below an open worktree its sessions, and below an open session its subagents.
function groupRowsHtml(groups, open, aff, name, accounts, m = null) {
  const rows = [];
  const fable = m?.fable || [];
  const sessionRows = (sessions, deep) => {
    for (const f of sessions) {
      const sessionOpen = open.has("s:" + f.row.id);
      rows.push(sessionRowHtml(f, sessionOpen, aff, name, accounts, m, fable, deep));
      if (sessionOpen) for (const s of f.subs) rows.push(subRowHtml(s, name, m, fable, deep ? 26 : 0));
    }
  };
  const unlistedRow = (tokens, pad) => rows.push(`<tr class="sub"><th scope="row" class="faint" style="padding-left: ${pad}px">smaller sessions not listed</th><td></td><td></td><td>${num(tokens)}</td><td></td><td></td><td></td><td></td></tr>`);
  for (const g of groups) {
    const isOpen = open.has(g.key);
    rows.push(projectRowHtml(g, isOpen, aff, name, accounts, m, fable));
    if (!isOpen) continue;
    sessionRows(g.sessions, false);
    if (g.unlisted > 0) unlistedRow(g.unlisted, 28);
    for (const t of g.trees) {
      const treeOpen = open.has(t.key);
      rows.push(worktreeRowHtml(t, treeOpen, aff, name, accounts, m, fable));
      if (!treeOpen) continue;
      sessionRows(t.sessions, true);
      if (t.unlisted > 0) unlistedRow(t.unlisted, 54);
    }
  }
  return rows.join("");
}

// A pin names a folder or a session the window may not show; it is listed here so it can still be
// removed. A pin set in the config is read only. Pure, so it is tested.
function strayPinsHtml(aff, groups, name) {
  if (!aff) return "";
  const projects = new Set(groups.flatMap(g => [g.project, ...g.trees.map(w => w.folder)]));
  const sessions = new Set(groups.flatMap(g => [...g.sessions, ...g.trees.flatMap(w => w.sessions)].map(f => f.row.id)));
  const item = (kind, p) => {
    const label = kind === "session" ? p.key.slice(0, 8) : folderLabel(aff, p.key);
    return `<li><span class="chip">${kind}</span> <span class="${kind === "session" ? "sid" : "stitle"}" title="${esc(p.key)}">${esc(label)}</span> → <strong>${esc(name(p.account))}</strong>
      ${p.full ? '<span class="chip warn">account full: falling back</span>' : ""}
      ${p.source === "ui" ? `<button type="button" class="button" data-unpin="${kind}" data-key="${esc(p.key)}">Unpin</button>` : '<span class="faint">set in the config</span>'}</li>`;
  };
  const items = [
    ...(aff.folder_pins || []).filter(p => !projects.has(p.key)).map(p => item("folder", p)),
    ...(aff.session_pins || []).filter(p => !sessions.has(p.key)).map(p => item("session", p)),
  ];
  return items.length ? `<div class="affinity"><h3>Pins outside this window</h3><ul class="pins">${items.join("")}</ul></div>` : "";
}

// The rebalance plan, or its result once applied: the projects that move, from → to. A project
// that stays is only counted. Folder and account names are escaped.
function rebalanceHtml(res, name, names = {}) {
  const moves = (res.plan || []).filter(m => m.from !== m.to);
  const stay = (res.plan || []).length - moves.length;
  const list = moves.map(m => `<li><span class="stitle" title="${esc(m.project)}">${esc(names[m.project] || m.project)}</span> ${esc(name(m.from))} → <strong>${esc(name(m.to))}</strong></li>`).join("");
  const counts = `${stay} stay${res.pinned ? ` · ${res.pinned} pinned, left alone` : ""}`;
  const close = '<button type="button" class="button" data-rebalance="close">Close</button>';
  if (res.applied) {
    return `<p>${moves.length ? `Moved ${moves.length} project${moves.length === 1 ? "" : "s"}.` : "Nothing to move."} <span class="faint">${counts}</span></p>${moves.length ? `<ul class="pins">${list}</ul>` : ""}<div class="actions">${close}</div>`;
  }
  if (!moves.length) return `<p>Already balanced: no project moves. <span class="faint">${counts}</span></p><div class="actions">${close}</div>`;
  return `<p>${moves.length} project${moves.length === 1 ? "" : "s"} would move. <span class="faint">${counts}</span></p><ul class="pins">${list}</ul>
    <div class="actions"><button type="button" class="button primary" data-rebalance="apply">Apply</button><button type="button" class="button" data-rebalance="close">Cancel</button></div>`;
}

let sessionSpan = "five_hour";
let sessionData = null;
const openGroups = readOpen();

function renderSessions(s) {
  sessionData = s || null; // a status without sessions clears the card
  drawSessions();
}

function drawSessions() {
  const w = sessionData?.[sessionSpan];
  const card = $("sessions");
  if (!w) { card.innerHTML = '<p class="empty">No session data yet.</p>'; $("sessions-meter").hidden = true; $("sessions-basis").hidden = true; return; }
  // a refresh must not close the account list the owner has open
  if (card.contains(document.activeElement) && document.activeElement.matches("select")) return;
  const aff = view?.affinity;
  const v = sessionsView(w, aff?.sessions);
  const groups = projectGroups(v, aff, sessionSpan);
  const meter = meterInfo(view);
  $("sessions-hint").textContent = `${sessionSpan === "week" ? "last 7 days" : "last 5 hours"} · ${v.total.requests} requests · ${num(v.total.tokens)} tokens (input, output, cache writes)`;
  card.innerHTML = groups.length ? `<table class="sessions-table">
      <thead><tr><th>Project</th><th>Last seen</th><th>Requests</th><th>Tokens</th><th>Share</th><th>~% ${esc(meter?.label ?? "")}</th><th class="left">Account</th><th class="left">Pin</th></tr></thead>
      <tbody>${groupRowsHtml(groups, openGroups, aff, short, pinAccounts(), meter)}</tbody>
    </table>`
    : '<p class="empty">No requests in this window.</p>';
  card.innerHTML += strayPinsHtml(aff, groups, short);
  const line = $("sessions-meter");
  line.textContent = meterLine(meter, short);
  line.title = meter ? `One unit of use = ${meter.basis}.` : "";
  line.hidden = !line.textContent;
  const basis = $("sessions-basis");
  basis.textContent = ultraBasisLine(meter);
  basis.hidden = !basis.textContent;
}

const pinAccounts = () => (view?.accounts || []).map(a => ({ auth_file: a.auth_file, name: a.name }));

async function setPin(kind, key, account) {
  try { renderStatus(await api("POST", ROUTES + "/pin", { kind, key, account })); showError(null); }
  catch (err) { showError(err); drawSessions(); }
}

// Some browsers fire "change" on every arrow key of a closed list, so a choice is set once it has
// stopped changing, not on each key press.
const pinTimers = new Map();
$("sessions").addEventListener("change", e => {
  const select = e.target.closest("[data-pin-select]");
  if (!select) return;
  const kind = select.dataset.pinSelect, key = select.dataset.key, id = kind + ":" + key;
  clearTimeout(pinTimers.get(id));
  pinTimers.set(id, setTimeout(async () => {
    pinTimers.delete(id);
    select.blur(); // so the redraw that follows is not held back by the focus
    select.disabled = true;
    await setPin(kind, key, select.value);
  }, 500));
});
$("sessions").addEventListener("click", async e => {
  const toggle = e.target.closest("[data-toggle]"), unpin = e.target.closest("[data-unpin]");
  if (toggle) {
    const key = toggle.dataset.toggle;
    if (!openGroups.delete(key)) openGroups.add(key);
    writeOpen(openGroups);
    drawSessions();
    [...$("sessions").querySelectorAll("[data-toggle]")].find(b => b.dataset.toggle === key)?.focus(); // a redraw keeps the keyboard where it was
  } else if (unpin) {
    unpin.disabled = true;
    await setPin(unpin.dataset.unpin, unpin.dataset.key, "");
  }
});

// "Dengele": place every project without a folder pin again, balanced. The plan is shown first;
// nothing changes until it is applied.
async function rebalance(apply) {
  const panel = $("rebalance-plan");
  try {
    const res = await api("POST", ROUTES + "/rebalance", { apply });
    panel.innerHTML = rebalanceHtml(res, short, view?.affinity?.names);
    panel.hidden = false;
    showError(null);
    if (apply) await refreshStatus();
  } catch (err) { showError(err); }
}
$("rebalance").addEventListener("click", async e => {
  const button = e.currentTarget;
  button.disabled = true;
  try { await rebalance(false); } finally { button.disabled = false; }
});
$("rebalance-plan").addEventListener("click", async e => {
  const choice = e.target.closest("[data-rebalance]");
  if (!choice) return;
  if (choice.dataset.rebalance === "close") { $("rebalance-plan").hidden = true; return; }
  choice.disabled = true;
  try { await rebalance(true); } finally { choice.disabled = false; }
});

for (const b of $("session-span").querySelectorAll("button")) {
  b.addEventListener("click", () => {
    sessionSpan = b.dataset.span;
    for (const o of $("session-span").querySelectorAll("button")) o.setAttribute("aria-pressed", String(o === b));
    drawSessions();
  });
}

// ---------- transcript history ----------

// What the transcript card shows for one window of /transcripts: root sessions with their
// subagents nested under them, tokens by model and the advisor models' tokens apart. A session's
// tokens are input, output and cache writes (cache reads are shown apart). These numbers come from
// Claude Code's transcripts, not from the proxy, so there is no account in them. Pure, so it is tested.
function transcriptsView(w) {
  w = w || {};
  const tokens = t => (t?.input_tokens || 0) + (t?.output_tokens || 0) + (t?.cache_creation_tokens || 0);
  const total = { requests: w.total?.requests || 0, tokens: tokens(w.total) };
  const percent = (n, of) => of ? Math.round(n * 100 / of) : 0;
  const byModel = m => Object.entries(m || {}).map(([key, t]) => ({ key, tokens: tokens(t) })).sort((a, b) => b.tokens - a.tokens || (a.key < b.key ? -1 : 1));
  const rows = [];
  const add = (s, depth) => {
    const agent = s.id.includes("/") ? s.id.slice(s.id.indexOf("/") + 1) : s.id;
    rows.push({
      depth, id: s.id, label: (depth ? agent : s.id).slice(0, 8), last: s.last, title: depth ? "" : s.title || "", titleKind: s.title_kind || "",
      requests: s.total?.requests || 0, tokens: tokens(s.total), cacheRead: s.total?.cache_read_tokens || 0,
      share: percent(tokens(s.total), total.tokens), models: byModel(s.models),
      advisor: { requests: s.advisor?.requests || 0, tokens: tokens(s.advisor), models: byModel(s.advisor_models) },
    });
    for (const c of s.subagents || []) add(c, depth + 1);
    // Subagents past the listed ones are one line under their session; its total already counts them.
    if (s.other_subagents?.sessions) rows.push({ more: true, depth: depth + 1, count: s.other_subagents.sessions, tokens: tokens(s.other_subagents.total) + tokens(s.other_subagents.advisor) });
  };
  for (const s of w.sessions || []) add(s, 0);
  return {
    total, rows, truncated: !!w.truncated, advisor: { requests: w.advisor?.requests || 0, tokens: tokens(w.advisor) },
    other: { sessions: w.other?.sessions || 0, tokens: tokens(w.other?.total) },
  };
}

function transcriptRowHtml(r) {
  if (r.more) return `<tr class="sub"><th scope="row" colspan="7"><span class="faint">↳ ${r.count} smaller subagents not listed: ${num(r.tokens)} tokens</span></th></tr>`;
  const kinds = { custom: "set with /rename", agent: "agent name", ai: "made up by Claude Code" };
  const name = r.title ? `<span class="stitle" title="${esc(r.title)} — ${esc(kinds[r.titleKind] || r.titleKind)}">${esc(r.title)}</span> ` : "";
  const models = r.models.map(m => `${esc(m.key || "—")} <span class="faint">${num(m.tokens)}</span>`).join(" · ") || "—";
  const advisor = r.advisor.requests
    ? `${r.advisor.requests} · ${num(r.advisor.tokens)} <span class="faint">${r.advisor.models.map(m => esc(m.key || "—")).join(", ")}</span>` : "—";
  return `<tr class="${r.depth ? "sub" : ""}">
    <th scope="row" title="${esc(r.id)}">${r.depth ? '<span class="faint">↳ </span>' : ""}${name}<span class="sid${r.title ? " faint" : ""}">${esc(r.label)}</span></th>
    <td>${r.last ? when(r.last) : "—"}</td><td>${r.requests}</td><td title="${esc(`cache reads ${num(r.cacheRead)}`)}">${num(r.tokens)}</td>
    <td>${r.share}%</td><td class="left">${models}</td><td class="left">${advisor}</td></tr>`;
}

let transcriptSpan = "7d";

async function loadTranscripts() {
  const card = $("transcripts");
  try {
    const v = transcriptsView(await api("GET", `${ROUTES}/transcripts?range=${transcriptSpan}`));
    $("transcripts-hint").textContent = `${v.total.requests} requests · ${num(v.total.tokens)} tokens (input, output, cache writes)`;
    card.innerHTML = v.rows.length ? `<table class="sessions-table">
        <thead><tr><th>Session</th><th>Last used</th><th>Requests</th><th>Tokens</th><th>Share</th><th class="left">Models</th><th class="left">Advisor</th></tr></thead>
        <tbody>${v.rows.map(transcriptRowHtml).join("")}</tbody>
      </table>${v.other.sessions ? `<p class="hint">${v.other.sessions} smaller sessions not listed: ${num(v.other.tokens)} tokens.</p>` : ""}${v.truncated
        ? '<p class="hint">The store holds more rows than one request reads: the oldest are left out.</p>' : ""}`
      : '<p class="empty">No transcript data in this window. Run cpa-balancer-import to fill it.</p>';
  } catch (e) {
    $("transcripts-hint").textContent = "";
    card.innerHTML = `<p class="empty">${esc(e.message || e)}</p>`;
  }
}

for (const b of $("transcript-span").querySelectorAll("button")) {
  b.addEventListener("click", () => {
    transcriptSpan = b.dataset.range;
    for (const o of $("transcript-span").querySelectorAll("button")) o.setAttribute("aria-pressed", String(o === b));
    loadTranscripts();
  });
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

// CPA keeps a credential cooled until its weekly reset even when Anthropic opens the window early
// (a manual reset). The balancer probes the account now; a locked one it finds open is released
// here, because a plugin cannot reach CPA's cooldown.
async function recheck(authFile) {
  const results = await api("POST", ROUTES + "/recheck" + (authFile ? `?auth_file=${encodeURIComponent(authFile)}` : ""));
  if (!Array.isArray(results)) throw new Error(results?.error || "unreadable answer from the balancer");
  const lines = [];
  for (const r of results) {
    const name = short(r.auth_file);
    if (r.error) lines.push(`${name}: ${r.error}`);
    else if (r.disabled) lines.push(`${name}: disabled in CPA — turn it on there`);
    else if (r.locked && r.open) {
      try {
        await api("POST", "/v8/management/routing/cooldown/reset", { auth_index: r.auth_index });
        lines.push(`${name}: unlocked — weekly ${Math.round(r.weekly)}%`);
      } catch (err) { lines.push(`${name}: open, but the release failed — ${err.message || err}`); }
    } else if (r.locked) lines.push(`${name}: still at its limit — weekly ${Math.round(r.weekly)}%, 5h ${Math.round(r.session)}%`);
    else lines.push(`${name}: not locked — weekly ${Math.round(r.weekly)}%`);
  }
  renderStatus(await api("POST", ROUTES + "/run")); // picks up the released credentials at once
  return lines;
}

async function runRecheck(button, authFile) {
  button.disabled = true;
  button.classList.add("busy");
  try {
    const lines = await recheck(authFile);
    $("notice").textContent = lines.join(" · ");
    $("notice").hidden = false;
    showError(null);
  } catch (err) { showError(err); }
  finally { button.disabled = false; button.classList.remove("busy"); }
}

$("recheck-all").addEventListener("click", e => runRecheck(e.currentTarget, ""));
$("accounts").addEventListener("click", e => {
  const button = e.target.closest("[data-recheck]");
  if (button) runRecheck(button, button.dataset.recheck);
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
  if (name === "history") { loadHistory(); loadTranscripts(); }
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
  accounts = (c.accounts || []).map(a => ({ auth_file: a.auth_file, reserve: a.reserve || "off", reserve_steps: (a.reserve_steps || []).map(s => ({ ...s })), session_steps: (a.session_steps || []).map(s => ({ ...s })), extra_usage: !!a.extra_usage, label: a.label || "" }));
  defaultSteps = (c.thresholds?.reserve || []).map(s => ({ ...s })).sort((x, y) => y.hours - x.hours);
  for (const el of document.querySelectorAll("[data-threshold]")) el.value = c.thresholds?.[el.dataset.threshold] ?? "";
  renderAccounts();
  renderDefaultSteps();
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
  const marks = [
    ...defaultSteps.map((st, k) => [st.cap, `${timeText("week", st.hours)}`, k % 2 ? "down" : "up", false]),
    [value("final_cap"), "final", "up", false], [value("session_cap"), "5-hour", "down", true],
  ];
  $("ruler").innerHTML = '<div class="track"></div>' + marks.map(([v, label, side, session]) => {
    if (!(v >= 0 && v <= 100)) return "";
    return `<div class="tick${session ? " session" : ""}" style="left: ${v}%"></div><div class="tag ${side}${v > 90 ? " end" : v < 10 ? " start" : ""}" style="left: ${v}%">${label} ${v}%</div>`;
  }).join("");
}
for (const el of document.querySelectorAll("[data-threshold]")) el.addEventListener("input", renderRuler);

// ---------- limits: steps over a window, edited the same way for the week and the 5 hours ----------

// A window: its length in hours, its time units, and how a time before its reset reads.
const WINDOWS = {
  week: { hours: 168, units: [["d", 24, "days"], ["h", 1, "hours"]] },
  five: { hours: 5, units: [["h", 1, "hours"], ["m", 1 / 60, "minutes"]] },
};
// The largest time a step may end before the reset, per unit: under the window's length.
const timeMax = (window, unit) => (window === "week" ? (unit === "d" ? 6 : 167) : unit === "h" ? 4 : 299);
const unitOf = (window, hours) => (window === "week" ? (hours > 0 && hours % 24 === 0 ? "d" : "h") : (Number.isInteger(hours) ? "h" : "m"));
const timeText = (window, hours) => {
  if (hours === 0) return "the reset";
  const u = unitOf(window, hours);
  return u === "d" ? `${hours / 24} d` : u === "m" ? `${Math.round(hours * 60)} m` : `${hours} h`;
};
// "50% until 5 d, 70% until 2 d before reset", "60% to the reset": the earliest step first.
const stepsText = (steps, window = "week") => {
  const sorted = [...steps].sort((x, y) => y.hours - x.hours);
  const before = sorted.filter(s => s.hours > 0).map(s => `${s.cap}% until ${timeText(window, s.hours)}`).join(", ");
  const atReset = sorted.find(s => s.hours === 0);
  return [before && `${before} before reset`, atReset && `${atReset.cap}% to the reset`].filter(Boolean).join(", then ");
};

// The window as a bar from its start to its reset: each step's cap over the time it is in force,
// then `last` (the final cap, or the default 5-hour cap) after the last step.
function timelineHtml(steps, window, last) {
  const total = WINDOWS[window].hours;
  const sorted = [...steps].filter(s => s.hours >= 0 && s.hours < total).sort((x, y) => y.hours - x.hours);
  const parts = [];
  let from = 0;
  for (const s of sorted) {
    const to = total - s.hours;
    if (to > from) parts.push({ from, to, cap: s.cap });
    from = Math.max(from, to);
  }
  if (from < total) parts.push({ from, to: total, cap: last });
  const tone = cap => (cap >= 100 ? "full" : cap >= 75 ? "high" : cap >= 40 ? "mid" : "low");
  const falls = parts.some((p, k) => k > 0 && p.cap < parts[k - 1].cap);
  // above the bar the total a span allows, inside it what the span adds to the one before
  const wide = p => p.to - p.from >= total / 14;
  const added = (p, k) => p.cap - (k ? parts[k - 1].cap : 0);
  return `<div class="timeline">
    <div class="totals">${parts.map(p => wide(p) ? `<span style="left: ${((p.from + p.to) / 2 / total) * 100}%">${p.cap}%</span>` : "").join("")}</div>
    <div class="bar">${parts.map((p, k) => `<div class="seg ${tone(p.cap)}" style="flex: ${p.to - p.from}" title="up to ${p.cap}% in all, ${added(p, k) >= 0 ? "+" : ""}${added(p, k)}% in this span">${wide(p) ? `${added(p, k) >= 0 ? "+" : ""}${added(p, k)}%` : ""}</div>`).join("")}</div>
    <div class="axis"><span style="left: 0">start</span>${axisLabels(sorted, window, total)}<span class="end" style="left: 100%">reset</span></div>
    ${falls ? `<p class="step-warning">A later step allows less than an earlier one. Use only grows, so the account would stop there: raise the later cap.</p>` : ""}
  </div>`;
}

// The times of the steps under the bar, each left out where it would crowd its neighbour or the end.
function axisLabels(sorted, window, total) {
  const out = [];
  let last = 0;
  for (const s of sorted.filter(s => s.hours > 0)) {
    const at = ((total - s.hours) / total) * 100;
    if (at - last < 7 || 100 - at < 7) continue;
    out.push(`<span style="left: ${at}%">${timeText(window, s.hours)}</span>`);
    last = at;
  }
  return out.join("");
}

// One step list: "Until [n] [unit] before the reset → use up to [x]%". `scope` names the list.
function stepsHtml(steps, scope, window, last, lastName) {
  const max = window === "week" ? finalCap() : 100;
  const tail = `<div class="step tail"${steps.some(s => s.hours === 0) ? " hidden" : ""}>Then to the reset <span class="arrow">→</span> use up to <strong>${last}%</strong> (${lastName})</div>`;
  return steps.map((s, k) => {
    const unit = unitOf(window, s.hours), factor = WINDOWS[window].units.find(([u]) => u === unit)[1];
    return `<div class="step" data-scope="${scope}" data-window="${window}" data-step="${k}">
      Until <input class="text small-number" type="number" min="0" step="any" data-field="time" max="${timeMax(window, unit)}" value="${Math.round((s.hours / factor) * 100) / 100}" aria-label="Time before the reset">
      <select class="select small" data-field="unit" aria-label="Unit">${WINDOWS[window].units.map(([u, , l]) => `<option value="${u}"${u === unit ? " selected" : ""}>${l}</option>`).join("")}</select>
      before the reset <span class="arrow">→</span> use up to
      <span class="input-unit small"><input type="number" min="1" max="${max}" step="1" data-field="cap" value="${s.cap}" aria-label="Cap"><em>%</em></span>
      ${steps.length > 1 ? `<button type="button" class="icon-button" data-step-remove aria-label="Remove this step"><svg class="icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button>` : ""}
    </div>`;
  }).join("") + tail + `<button type="button" class="link-button" data-step-add="${scope}" data-window="${window}">+ Add step</button>`;
}

let defaultSteps = [];
let draft = null; // the account being edited, a copy until Done
const stepsOf = scope => (scope === "default" ? defaultSteps : scope === "week" ? draft.reserve_steps : draft.session_steps);
const finalCap = () => Number(document.querySelector('[data-threshold="final_cap"]').value) || 100;
const sessionCap = () => Number(document.querySelector('[data-threshold="session_cap"]').value) || 100;

function renderDefaultSteps() {
  $("default-steps").innerHTML = timelineHtml(defaultSteps, "week", finalCap()) + stepsHtml(defaultSteps, "default", "week", finalCap(), "final cap");
  renderRuler();
}

// Edits change the steps in place; only the timeline is redrawn, so the focus stays in its field.
function stepInput(e) {
  const row = e.target.closest(".step");
  if (!row) return false;
  if (e.type === "change") {
    const unit = row.querySelector('[data-field="unit"]').value;
    const clamp = (field, lo, hi) => {
      const el = row.querySelector(`[data-field="${field}"]`);
      el.max = hi;
      if (el.value !== "") el.value = Math.min(hi, Math.max(lo, Number(el.value)));
    };
    clamp("time", 0, timeMax(row.dataset.window, unit));
    clamp("cap", 1, row.dataset.window === "week" ? finalCap() : 100);
  }
  const step = stepsOf(row.dataset.scope)[+row.dataset.step];
  const factor = WINDOWS[row.dataset.window].units.find(([u]) => u === row.querySelector('[data-field="unit"]').value)[1];
  step.cap = Number(row.querySelector('[data-field="cap"]').value);
  step.hours = Number(row.querySelector('[data-field="time"]').value) * factor;
  const box = row.closest(".limit-steps");
  box.querySelector(".timeline").outerHTML = timelineHtml(stepsOf(row.dataset.scope), row.dataset.window, row.dataset.window === "week" ? finalCap() : sessionCap());
  const tail = box.querySelector(".step.tail");
  if (tail) tail.hidden = stepsOf(row.dataset.scope).some(s => s.hours === 0);
  if (row.dataset.scope === "default") renderRuler();
  if (e.type === "change") {
    const steps = stepsOf(row.dataset.scope);
    const sorted = [...steps].sort((x, y) => y.hours - x.hours);
    if (sorted.some((st, k) => st !== steps[k])) {
      steps.splice(0, steps.length, ...sorted);
      if (row.dataset.scope === "default") renderDefaultSteps(); else renderEditor();
    }
  }
  return true;
}

function stepClick(e) {
  const add = e.target.closest("[data-step-add]"), remove = e.target.closest("[data-step-remove]");
  if (add) {
    const steps = stepsOf(add.dataset.stepAdd), window = WINDOWS[add.dataset.window];
    // the new step takes the first free whole day (or hour) before the reset, the cap of the step
    // in force there: a start to edit
    const back = add.dataset.window === "week" ? 24 : 1;
    let hours = 0;
    while (steps.some(s => s.hours === hours) && hours + back < window.hours) hours += back;
    if (steps.some(s => s.hours === hours)) return true;
    const later = [...steps].filter(s => s.hours < hours).sort((x, y) => y.hours - x.hours)[0];
    steps.push({ cap: later?.cap ?? (add.dataset.window === "week" ? finalCap() : sessionCap()), hours });
  } else if (remove) {
    const row = remove.closest(".step");
    stepsOf(row.dataset.scope).splice(+row.dataset.step, 1);
  } else return false;
  // shown earliest first, so the list reads in the same order as the timeline
  for (const scope of ["default", ...(draft ? ["week", "five"] : [])]) stepsOf(scope).sort((x, y) => y.hours - x.hours);
  const scope = (add || remove.closest(".step")).dataset.stepAdd ?? remove.closest(".step").dataset.scope;
  if (scope === "default") renderDefaultSteps(); else renderEditor();
  return true;
}

$("default-steps").addEventListener("input", stepInput);
$("default-steps").addEventListener("change", stepInput);
$("default-steps").addEventListener("click", stepClick);

// ---------- accounts: one summary line each; Edit opens the account in a dialog ----------

function weeklySummary(a) {
  if (a.reserve === "off") return "no limit";
  if (a.reserve === "default") return "default";
  const caps = a.reserve_steps.map(s => s.cap);
  return a.reserve_steps.length === 1 ? stepsText(a.reserve_steps) : `${a.reserve_steps.length} steps, ${Math.min(...caps)}–${Math.max(...caps)}%`;
}
function fiveSummary(a) {
  if (!hasFiveHour(a.auth_file)) return "—";
  if (!a.session_steps.length) return "default";
  const caps = a.session_steps.map(s => s.cap);
  return a.session_steps.length === 1 ? stepsText(a.session_steps, "five") : `${a.session_steps.length} steps, ${Math.min(...caps)}–${Math.max(...caps)}%`;
}

function renderAccounts() {
  $("account-list").innerHTML = accounts.map((a, i) => `<li>
    <div class="who"><strong>${esc(a.label || short(a.auth_file))}</strong><span>${esc(emailOf(a.auth_file))}${credentials.some(c => c.auth_file === a.auth_file) ? "" : " · not known to CPA"}</span></div>
    <span class="plan">${esc(planOf(a.auth_file))}</span>
    <span class="sum"><em>Weekly</em> ${esc(weeklySummary(a))}</span>
    <span class="sum"><em>5-hour</em> ${esc(fiveSummary(a))}</span>
    ${a.extra_usage ? '<span class="chip">extra usage</span>' : ""}
    <button type="button" class="button" data-edit="${i}">Edit</button>
  </li>`).join("");
  const free = credentials.filter(c => !accounts.some(a => a.auth_file === c.auth_file));
  $("account-add").innerHTML = free.length
    ? free.map(c => `<option value="${esc(c.auth_file)}">${esc(c.email || short(c.auth_file))}${c.disabled ? " (disabled)" : ""}</option>`).join("")
    : '<option value="">No other Claude credentials in CPA</option>';
  $("account-add-button").disabled = !free.length;
}

function openEditor(i) {
  draft = { index: i, ...structuredClone(accounts[i]) };
  for (const steps of [draft.reserve_steps, draft.session_steps]) steps.sort((x, y) => y.hours - x.hours);
  renderEditor();
  $("account-editor").showModal();
}

function renderEditor() {
  const a = draft, five = hasFiveHour(a.auth_file);
  $("editor-body").innerHTML = `
    <header class="editor-head">
      <div><strong>${esc(a.label || short(a.auth_file))}</strong><span>${esc(emailOf(a.auth_file))}</span></div>
      <span class="plan">${esc(planOf(a.auth_file))}</span>
    </header>
    <div class="editor-row">
      <label class="field"><span>Name</span><input class="text" data-draft="label" value="${esc(a.label)}" placeholder="${esc(short(a.auth_file))}" maxlength="20"
        title="The name the Management Center, the HUD and its commands use; empty takes one from the e-mail"></label>
      <div class="reserve"><button type="button" role="switch" class="switch" data-draft="extra_usage" aria-checked="${a.extra_usage}" aria-label="Extra usage credits are on"><span class="knob"></span></button>Extra usage</div>
    </div>
    <section class="limit">
      <div class="limit-head"><h3>Weekly limit</h3>
        <div class="segmented" role="group" aria-label="Weekly limit">${[["off", "No limit"], ["default", "Default"], ["custom", "Custom"]].map(([k, l]) =>
          `<button type="button" data-weekly="${k}" aria-pressed="${a.reserve === k}">${l}</button>`).join("")}</div></div>
      ${a.reserve === "off" ? `<p class="hint">Used up to the final cap (${finalCap()}%).</p>`
        : a.reserve === "default" ? `<div class="limit-steps">${timelineHtml(defaultSteps, "week", finalCap())}</div><p class="hint">The default steps, set under Thresholds.</p>`
        : `<div class="limit-steps">${timelineHtml(a.reserve_steps, "week", finalCap())}${stepsHtml(a.reserve_steps, "week", "week", finalCap(), "final cap")}</div>`}
    </section>
    ${five ? `<section class="limit">
      <div class="limit-head"><h3>5-hour limit</h3>
        <div class="segmented" role="group" aria-label="5-hour limit">${[["default", "Default"], ["custom", "Custom"]].map(([k, l]) =>
          `<button type="button" data-five="${k}" aria-pressed="${(a.session_steps.length > 0) === (k === "custom")}">${l}</button>`).join("")}</div></div>
      ${a.session_steps.length
        ? `<div class="limit-steps">${timelineHtml(a.session_steps, "five", sessionCap())}${stepsHtml(a.session_steps, "five", "five", sessionCap(), "default 5-hour cap")}</div>`
        : `<p class="hint">Switches away at the 5-hour cap under Thresholds (${sessionCap()}%).</p>`}
    </section>` : '<p class="hint">This plan has no 5-hour limit.</p>'}`;
}

$("editor-body").addEventListener("input", e => {
  if (stepInput(e)) return;
  if (e.target.matches('[data-draft="label"]')) draft.label = e.target.value.trim();
});
$("editor-body").addEventListener("change", e => { if (e.target.closest(".step")) stepInput(e); });
$("editor-body").addEventListener("click", e => {
  if (stepClick(e)) return;
  const weekly = e.target.closest("[data-weekly]"), five = e.target.closest("[data-five]"), extra = e.target.closest('[data-draft="extra_usage"]');
  if (weekly) {
    draft.reserve = weekly.dataset.weekly;
    if (draft.reserve === "custom" && !draft.reserve_steps.length) draft.reserve_steps = defaultSteps.map(s => ({ ...s }));
  } else if (five) {
    draft.session_steps = five.dataset.five === "custom" ? (draft.session_steps.length ? draft.session_steps : [{ cap: 60, hours: 0 }]) : [];
  } else if (extra) {
    draft.extra_usage = !draft.extra_usage;
  } else return;
  renderEditor();
});
$("editor-done").addEventListener("click", () => {
  const { index, ...a } = draft;
  accounts[index] = a;
  draft = null;
  $("account-editor").close();
  renderAccounts();
});
$("editor-cancel").addEventListener("click", () => { draft = null; $("account-editor").close(); });
$("editor-remove").addEventListener("click", () => {
  accounts.splice(draft.index, 1);
  draft = null;
  $("account-editor").close();
  renderAccounts();
});

$("account-list").addEventListener("click", e => {
  const edit = e.target.closest("[data-edit]");
  if (edit) openEditor(+edit.dataset.edit);
});
$("account-add-button").addEventListener("click", () => {
  const f = $("account-add").value;
  if (!f) return;
  accounts.push({ auth_file: f, reserve: "off", reserve_steps: [], session_steps: [], extra_usage: false, label: "" });
  renderAccounts();
  openEditor(accounts.length - 1);
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
  thresholds.reserve = defaultSteps.map(s => ({ ...s }));
  // An empty label is left out (the balancer then takes a name from the e-mail); weekly steps go
  // only with a custom limit, 5-hour steps only when there are any.
  const copy = steps => steps.map(s => ({ ...s }));
  return { accounts: accounts.map(({ label, reserve_steps, session_steps, ...a }) => ({ ...a, ...(label ? { label } : {}),
    ...(a.reserve === "custom" ? { reserve_steps: copy(reserve_steps) } : {}), ...(session_steps.length ? { session_steps: copy(session_steps) } : {}) })), buckets: { main: [...buckets.main], fable: [...buckets.fable] }, thresholds, anchor };
}

// The same rules as config.Validate; the plugin checks them again.
function validate(c) {
  const errors = [], t = c.thresholds;
  for (const k of ["final_cap", "session_cap"]) {
    if (k in t && !(t[k] >= 0 && t[k] <= 100)) errors.push(`${k} must be between 0 and 100`);
  }
  if ("min_dwell_minutes" in t && !(Number.isInteger(t.min_dwell_minutes) && t.min_dwell_minutes >= 0)) errors.push("minimum dwell must be a whole number ≥ 0");
  if ("stale_hours" in t && !(t.stale_hours > 0)) errors.push("stale after must be more than 0 hours");
  if (!c.accounts.length) errors.push("at least one account is required");
  for (const a of c.accounts) if (a.label && !/^[\p{L}\p{N}_-]{1,20}$/u.test(a.label)) errors.push(`name "${a.label}" must be one word of up to 20 letters, digits, - or _`);
  const final = t.final_cap ?? 100;
  // The same rules as config.validSteps.
  const checkSteps = (steps, who, max, window, span) => {
    if (!steps.length) errors.push(`${who}: a limit needs at least one step`);
    for (const st of steps) {
      if (!(st.cap > 0 && st.cap <= max)) errors.push(`${who}: a step's cap must be above 0 and at most ${max}%`);
      if (!(st.hours >= 0 && st.hours < window)) errors.push(`${who}: a step must end from 0 to under ${span} before the reset`);
    }
    if (new Set(steps.map(st => st.hours)).size !== steps.length) errors.push(`${who}: two steps end at the same time`);
    const sorted = [...steps].sort((x, y) => y.hours - x.hours);
    if (sorted.some((st, k) => k > 0 && st.cap < sorted[k - 1].cap)) errors.push(`${who}: a later step allows less than an earlier one`);
  };
  checkSteps(t.reserve || [], "default weekly limit", final, 168, "7 days");
  for (const a of c.accounts.filter(a => a.reserve === "custom")) checkSteps(a.reserve_steps, `${short(a.auth_file)} weekly`, final, 168, "7 days");
  for (const a of c.accounts.filter(a => a.session_steps)) checkSteps(a.session_steps, `${short(a.auth_file)} 5-hour`, 100, 5, "5 hours");
  const labels = c.accounts.map(a => (a.label || "").toLowerCase()).filter(Boolean);
  if (new Set(labels).size !== labels.length) errors.push("two accounts have the same name");
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
      const was = Object.fromEntries((before.accounts || []).map(a => [a.auth_file, { reserve: reserveText(a), five: a.session_steps?.length ? stepsText(a.session_steps, "five") : "default", extra: !!a.extra_usage, label: a.label || "" }]));
      const now = Object.fromEntries(after.accounts.map(a => [a.auth_file, { reserve: reserveText(a), five: a.session_steps?.length ? stepsText(a.session_steps, "five") : "default", extra: !!a.extra_usage, label: a.label || "" }]));
      const order = (before.accounts || []).map(a => a.auth_file).join() !== after.accounts.map(a => a.auth_file).join();
      for (const f of Object.keys(now)) {
        if (!(f in was)) lines.push(`add account ${short(f)} (${now[f].reserve})${now[f].extra ? " (extra usage)" : ""}`);
        else {
          if (was[f].reserve !== now[f].reserve) lines.push(`${short(f)}: ${now[f].reserve}`);
          if (was[f].extra !== now[f].extra) lines.push(`${short(f)}: extra usage ${now[f].extra ? "on" : "off"}`);
          if (was[f].five !== now[f].five) lines.push(`${short(f)}: 5-hour ${now[f].five}`);
          if (was[f].label !== now[f].label) lines.push(`${short(f)}: name ${now[f].label ? `"${now[f].label}"` : "from the e-mail"}`);
        }
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
