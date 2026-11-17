// The Sessions card grouped by project folder: groups and their totals, what is open, the pin
// choices, the rebalance plan, and the open state kept in localStorage.
import { load } from './extract.mjs'

const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const lib = (params = {}) => load({
  consts: ['esc', 'num', 'MAX_SESSION_DEPTH', 'REASONS', 'reasonText', 'OPEN_KEY', 'MAX_OPEN'],
  fns: ['sessionsView', 'readOpen', 'writeOpen', 'projectGroups', 'homeInfo', 'toggleHtml', 'pinSelectHtml', 'placementText', 'homeText',
    'projectRowHtml', 'sessionRowHtml', 'subRowHtml', 'groupRowsHtml', 'strayPinsHtml', 'rebalanceHtml',
    'planShareText', 'ultraShareOf', 'meterInfo', 'meterLine', 'ultraBasisLine', 'meterTip', 'planCellHtml', 'modelLinesHtml'],
  extra: when, params,
  returns: ['sessionsView', 'readOpen', 'writeOpen', 'projectGroups', 'groupRowsHtml', 'strayPinsHtml', 'rebalanceHtml',
    'planShareText', 'meterInfo', 'meterLine', 'ultraBasisLine', 'meterTip', 'modelLinesHtml'],
})
const { sessionsView, readOpen, writeOpen, projectGroups, groupRowsHtml, strayPinsHtml, rebalanceHtml,
  planShareText, meterInfo, meterLine, ultraBasisLine, meterTip, modelLinesHtml } = lib({ localStorage: {} })

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }
const t = (requests, input, output = 0) => ({ requests, input_tokens: input, output_tokens: output, cache_read_tokens: 0, cache_creation_tokens: 0 })
const A = 'aaaaaaaa-1111-2222-3333-444444444444', B = 'bbbbbbbb-1111-2222-3333-444444444444', C = 'cccccccc-1111-2222-3333-444444444444', D = 'dddddddd-1111-2222-3333-444444444444'
const session = (id, total, subagents = []) => ({ id, last: '2026-11-15T10:00:00Z', own: total, total, accounts: { 'a.json': total }, models: {}, subagents })
const win = {
  total: t(20, 1000), other: { sessions: 0, total: t(0, 0) },
  sessions: [
    session(A, t(5, 100), [{ id: A + '/ab12', parent: A, last: '2026-11-15T10:00:00Z', own: t(2, 40), total: t(2, 40), accounts: { 'a.json': t(2, 40) }, models: {} }]),
    session(B, t(6, 500)), session(C, t(4, 300)), session(D, t(2, 20)), session('', t(3, 80)),
  ],
}
const aff = {
  session_projects: { [A]: '-work-alpha', [B]: '-work-beta', [C]: '-work-alpha' },
  projects: [
    { project: '-work-alpha', five_hour: { requests: 12, tokens: 460, last: '2026-11-15T10:00:00Z' }, week: { requests: 12, tokens: 460, last: '2026-11-15T10:00:00Z' } },
    { project: '-work-beta', five_hour: { requests: 6, tokens: 500, last: '2026-11-15T10:00:00Z' }, week: { requests: 6, tokens: 500, last: '2026-11-15T10:00:00Z' } },
    { project: '', five_hour: { requests: 5, tokens: 100, last: '2026-11-15T10:00:00Z' }, week: { requests: 5, tokens: 100, last: '2026-11-15T10:00:00Z' } },
  ],
  homes: [{ project: '-work-alpha', account: 'b.json', full: false }, { project: '-work-beta', account: 'a.json', full: true }],
  folder_pins: [{ key: '-work-beta', account: 'b.json', source: 'ui', full: false }, { key: '-work-gone', account: 'a.json', source: 'config', full: true }],
  session_pins: [{ key: C, account: 'b.json', source: 'ui', full: false }, { key: 'eeeeeeee-1111-2222-3333-444444444444', account: 'a.json', source: 'ui', full: false }],
  sessions: {
    [A]: { account: 'b.json', reason: 'project_home', project: '-work-alpha', at: 'x' },
    [A + '/ab12']: { account: 'b.json', reason: 'project_home', project: '-work-alpha', at: 'x' },
    [B]: { account: 'b.json', reason: 'folder_pin', project: '-work-beta', at: 'x' },
  },
}
const name = k => ({ 'a.json': 'first', 'b.json': 'second' }[k] || k)
const accounts = [{ auth_file: 'a.json', name: 'first' }, { auth_file: 'b.json', name: 'second' }]
const v = sessionsView(win, aff.sessions)
const groups = projectGroups(v, aff, 'week')

check('projects are sorted by use, the largest first', groups.map(g => g.project).join('|') === '-work-beta|-work-alpha|')
check('a session without a folder, and one without an id, are the group of no project', groups[2].none && groups[2].key === 'n:' && groups[2].sessions.length === 2)
check('a project takes the balancer\'s total, not the sum of what is listed', groups[1].tokens === 460 && groups[1].requests === 12)
check('a project\'s share is of the window', groups[0].share === 50 && groups[1].share === 46)
check('what the listed sessions leave out is counted', groups[1].unlisted === 460 - (100 + 300) && groups[0].unlisted === 0)
check('sessions are sorted by use inside a project and subagents nest under theirs', groups[1].sessions.map(f => f.row.id).join('|') === `${C}|${A}` && groups[1].sessions[1].subs.length === 1)
check('a project with no session listed still has its row (a cut week)', projectGroups(v, { ...aff, projects: [...aff.projects, { project: '-work-small', five_hour: { requests: 1, tokens: 5 }, week: { requests: 1, tokens: 5 } }] }, 'week').some(g => g.project === '-work-small' && g.sessions.length === 0 && g.unlisted === 5))
check('the row shows the other window beside the one in view', groups[1].alt?.label === '5 h' && groups[1].alt.tokens === 460 && groupRowsHtml(groups, new Set(), aff, name, accounts).includes('· 5 h 460'))
check('in the 5-hour view the other window is the week', projectGroups(v, aff, 'five_hour').find(g => g.project === '-work-beta').alt?.label === '7 d')
check('without the balancer\'s totals there is no other window', projectGroups(v, { sessions: aff.sessions }, 'week').every(g => g.alt === null))
check('the 5-hour span reads its own totals', projectGroups(v, { ...aff, projects: [{ project: '-work-alpha', five_hour: { requests: 1, tokens: 9 }, week: { requests: 99, tokens: 99 } }] }, 'five_hour').find(g => g.project === '-work-alpha').tokens === 9)
check('without the folder map a session falls back to the project of its pick', projectGroups(v, { sessions: aff.sessions }, 'week').some(g => g.project === '-work-alpha' && g.sessions.some(f => f.row.id === A)))
check('without any affinity every session is in one group', projectGroups(sessionsView(win), undefined, 'week').length === 1)

const closed = groupRowsHtml(groups, new Set(), aff, name, accounts)
check('closed on open: one row per project, no session rows', (closed.match(/<tr /g) || []).length === 3 && !closed.includes('class="session') && closed.includes('aria-expanded="false"'))
check('the project row shows the account and why: a pin first', closed.includes('folder pin') && closed.includes('account full') === false)
check('the project row shows its home', closed.includes('project home') && closed.includes('second'))
check('a home whose account is full says it moves on', groupRowsHtml(groups, new Set(), { ...aff, folder_pins: [] }, name, accounts).includes('full: moves on the next request'))
check('the no-project group has no pin choice and says CPA decides', closed.includes('CPA&#39;s own order') || closed.includes("CPA's own order"))
check('the choice of a pinned project has the pin selected', /data-pin-select="folder" data-key="-work-beta"[^>]*>\s*<option value="">no pin<\/option><option value="a.json">first<\/option><option value="b.json" selected>second/.test(closed))
check('a project with no pin has "no pin" selected', /data-key="-work-alpha"[^>]*>\s*<option value="" selected>no pin/.test(closed))

const alphaOpen = groupRowsHtml(groups, new Set(['p:-work-alpha']), aff, name, accounts)
check('an open project lists its sessions, subagents still closed', alphaOpen.includes('class="session') && !alphaOpen.includes(A + '/ab12') && alphaOpen.includes('data-toggle="s:' + A + '"'))
check('a session pin selects its account in the session row', new RegExp(`data-pin-select="session" data-key="${C}"[^>]*>\\s*<option value="">no pin</option><option value="a.json">first</option><option value="b.json" selected>`).test(alphaOpen))
check('smaller sessions not listed are one line under the project', alphaOpen.includes('smaller sessions not listed'))
const both = groupRowsHtml(groups, new Set(['p:-work-alpha', 's:' + A]), aff, name, accounts)
check('an open session lists its subagents, with no pin choice of their own', both.includes('class="sub"') && both.includes(A + '/ab12') && !both.includes(`data-key="${A}/ab12"`))
check('where a session went is said in words', both.includes('project home'))

const stray = strayPinsHtml(aff, groups, name)
check('pins outside the window are listed so they can be removed', stray.includes('-work-gone') && stray.includes('eeeeeeee') && stray.includes('data-unpin="session"'))
check('a pin in the config is read only', stray.includes('set in the config') && !stray.includes('data-unpin="folder"'))
check('a pin on a listed project or session is not repeated', !stray.includes('-work-beta') && !stray.includes('cccccccc'))
check('no pins outside gives nothing', strayPinsHtml({ folder_pins: [], session_pins: [] }, groups, name) === '' && strayPinsHtml(undefined, groups, name) === '')

// folder names, session ids and account names are user data: none may reach the page unescaped
const hostile = '"><img src=x onerror=alert(1)>'
const evilAff = {
  session_projects: { [A]: hostile }, projects: [{ project: hostile, five_hour: { requests: 1, tokens: 1, last: hostile }, week: { requests: 1, tokens: 1, last: hostile } }],
  homes: [{ project: hostile, account: hostile, full: true }], folder_pins: [{ key: hostile, account: hostile, source: 'ui', full: true }],
  session_pins: [{ key: hostile, account: hostile, source: 'ui', full: true }],
  sessions: { [A]: { account: hostile, reason: hostile, project: hostile, at: hostile } },
}
const evilGroups = projectGroups(sessionsView(win, evilAff.sessions), evilAff, 'week')
const evilRows = groupRowsHtml(evilGroups, new Set(['p:' + hostile, 's:' + A]), evilAff, k => k, [{ auth_file: hostile, name: hostile }]) + strayPinsHtml(evilAff, evilGroups, k => k)
check('hostile text is escaped everywhere', !evilRows.includes('<img') && evilRows.includes('&lt;img') && !evilRows.includes('"><img'))
check('no attribute can be closed by a folder name', !/data-(toggle|key)="[^"]*"[^>]*onerror/.test(evilRows))

// use of an Ultra account's limit window
check('a share is shown to one decimal', planShareText(3.24) === '~3.2%' && planShareText(12) === '~12.0%')
check('none at all is ~0.0%, a trace is <0.1%', planShareText(0) === '~0.0%' && planShareText(0.01) === '<0.1%')
check('an unknown share, and anything that is not a plain number, is a dash', [null, undefined, 'x', NaN, Infinity, -1, '<img src=x>', {}].every(v => planShareText(v) === '—'))

const meterStatus = {
  meter: { label: 'Ultra', basis: 'input + output + cache writes + 1.5% of cache reads', fable_models: ['fable-1'] },
  accounts: [
    { auth_file: 'a.json', plan_label: 'Ultra', meter: { known: true, per_million: 0.226, from_previous: false, window_hours: 168, outside: 6.4 } },
    { auth_file: 'b.json', plan_label: 'Ultra', meter: { known: false, per_million: 0, from_previous: false } },
    { auth_file: 'c.json', plan_label: 'Pro' },
  ],
}
const m = meterInfo(meterStatus)
check('no meter in the status, no meter info', meterInfo({}) === null && meterInfo(undefined) === null)
check('the header line gives each Ultra account its own figure, never a mean', meterLine(m, name) === 'Ultra: 1M units of use ≈ first 0.23% · second not learned yet of a limit window · window 168 h · outside the proxy: 6 points of first (in no row)')
check('a figure taken from the previous window says so', meterLine(meterInfo({ ...meterStatus, accounts: [{ auth_file: 'a.json', meter: { known: true, per_million: 3, from_previous: true } }] }), name).includes('3.00% (previous window)'))
check('without an Ultra account there is no header line', meterLine(meterInfo({ meter: meterStatus.meter, accounts: [] }), name) === '')
const tip = meterTip(m, 1_200_000)
check('the tooltip says it is an estimate learned from the account\'s own meter, and names the basis', tip.includes('estimate') && tip.includes('own meter') && tip.includes('input + output + cache writes + 1.5% of cache reads'))
check('the tooltip says what is left out', tip.includes('Advisor calls are not counted') && tip.includes('per-model weights are not learned yet') && tip.includes('On Pro: 1.2M tokens') && tip.includes('Window: 168 h'))
check('no Pro tokens, no mention of them', !meterTip(m, 0).includes('tokens (not in this percent)'))
check('a status with nothing outside the proxy says nothing of it', !meterLine(meterInfo({ ...meterStatus, accounts: [{ auth_file: 'a.json', meter: { known: true, per_million: 1, outside: 0 } }] }), name).includes('outside'))
check('the page never says week or Max 20x', !/week|Max 20x/i.test(tip + meterLine(m, name)))

// the line under the Sessions header says what the percent is based on, from what the status serves
const basisLine = ultraBasisLine(m)
check('the basis line names the percent, the account, the window and the basis', basisLine === "~% Ultra: share of one Ultra account's 7-day limit, learned from each account's own meter (input + output + cache writes + 1.5% of cache reads; advisor calls not counted)")
check('the window length is the served one, in hours when it is not whole days', ultraBasisLine(meterInfo({ ...meterStatus, accounts: [{ auth_file: 'a.json', meter: { known: true, per_million: 1, window_hours: 5 } }] })).includes("account's 5-hour limit"))
check('with no window measured yet the line says limit window', ultraBasisLine(meterInfo({ ...meterStatus, accounts: [{ auth_file: 'b.json', meter: { known: false, per_million: 0 } }] })).includes("account's limit window,"))
check('the basis is the one the status serves, not a copy', ultraBasisLine(meterInfo({ ...meterStatus, meter: { ...meterStatus.meter, basis: 'a different unit' } })).includes('(a different unit; '))
check('no meter, no basis line', ultraBasisLine(null) === '' && ultraBasisLine(meterInfo({})) === '')

const planWin = {
  total: t(10, 1000), other: { sessions: 0, total: t(0, 0) },
  sessions: [{ ...session(A, t(5, 100), [{ id: A + '/ab12', parent: A, last: '2026-11-15T10:00:00Z', own: t(2, 40), total: t(2, 40), accounts: { 'a.json': t(2, 40) }, models: {}, planShare5h: 1, planShare7d: 2.5, elsewhere5h: 0, elsewhere7d: 0, model_lines: [] }]),
    planShare5h: 3.24, planShare7d: 12, elsewhere5h: 1_200_000, elsewhere7d: 1_200_000,
    model_lines: [{ model: 'fable-1', first: '2026-11-15T09:00:00Z', last: '2026-11-15T10:00:00Z', own: t(1, 5000), subagents: t(1, 2500) }] },
    { ...session(B, t(5, 100)), planShare5h: null, planShare7d: null, elsewhere5h: 0, elsewhere7d: 0 }],
}
const planAff = { session_projects: { [A]: '-work-alpha', [B]: '-work-alpha' }, projects: [{ project: '-work-alpha', five_hour: { requests: 10, tokens: 200, models: [] }, week: { requests: 10, tokens: 200 },
  planShare5h: 4.24, planShare7d: 14.5, elsewhere5h: 1_200_000, elsewhere7d: 1_200_000 }], homes: [], folder_pins: [], session_pins: [], sessions: {} }
const planGroups = projectGroups(sessionsView(planWin, planAff.sessions), planAff, 'five_hour')
check('a project carries both windows\' figures', planGroups[0].plan.five === 4.24 && planGroups[0].plan.week === 14.5)
const weekGroups = projectGroups(sessionsView(planWin, planAff.sessions), planAff, 'week')
check('the figures do not depend on the window in view', weekGroups[0].plan.five === 4.24 && weekGroups[0].plan.week === 14.5)
const oneWindow = { ...planAff, projects: [{ ...planAff.projects[0], five_hour: { requests: 0, tokens: 0 } }] }
const oneGroups = projectGroups(sessionsView(planWin, oneWindow.sessions), oneWindow, 'week')
check('a project with no use in a window has no figure for it', oneGroups[0].plan.five === null && oneGroups[0].plan.week === 14.5)
const planShown = groupRowsHtml(planGroups, new Set(['p:-work-alpha', 's:' + A]), planAff, name, accounts, m)
check('the project, the session and the subagent rows each have the cell', planShown.includes('~4.2%') && planShown.includes('~3.2%') && planShown.includes('~1.0%'))
check('every cell shows both windows, whatever the window in view', planShown.includes('5h ~4.2% · 7d ~14.5%') && planShown.includes('5h ~3.2% · 7d ~12.0%') && planShown.includes('5h ~1.0% · 7d ~2.5%'))
const openAlpha = new Set(['p:-work-alpha', 's:' + A])
check('the 7-day view shows the same cells', groupRowsHtml(weekGroups, openAlpha, planAff, name, accounts, m).includes('5h ~3.2% · 7d ~12.0%'))
check('a session that cannot be priced shows a dash for both', planShown.includes('5h — · 7d —'))
const lone = { ...planWin, sessions: [{ ...session(A, t(5, 100)), planShare5h: null, planShare7d: 7, elsewhere5h: 0, elsewhere7d: 0 }] }
const loneShown = groupRowsHtml(projectGroups(sessionsView(lone), { session_projects: { [A]: '-work-alpha' } }, 'week'), new Set(['p:-work-alpha']), undefined, name, accounts, m)
check('a session in one window only shows a dash for the other', loneShown.includes('5h — · 7d ~7.0%'))
check('the cell has the tooltip, with the tokens on the Pro account', /<td title="[^"]*On Pro: 1\.2M tokens[^"]*">5h ~3\.2%/.test(planShown))
const cells = html => html.split('</tr>').filter(r => r.includes('<tr')).map(r => (r.match(/<td/g) || []).length)
check('every row has as many cells as the header has columns after the first', cells(planShown).every(n => n === 7))
check('the row of smaller sessions has the cell too', cells(groupRowsHtml(planGroups.map(g => ({ ...g, unlisted: 5 })), new Set(['p:-work-alpha']), planAff, name, accounts, m)).every(n => n === 7))
check('without a status the cell is still there, with a dash', groupRowsHtml(projectGroups(sessionsView(win), undefined, 'week'), new Set(), undefined, name, accounts).includes('5h — · 7d —'))

const lines = modelLinesHtml([{ model: 'm-1', first: '2026-11-15T09:00:00Z', last: '2026-11-15T10:00:00Z', own: t(1, 5000), subagents: t(1, 2500) }, { model: '', first: '2026-11-15T10:00:00Z', last: '2026-11-15T11:00:00Z', own: t(1, 9), subagents: t(0, 0) }], ['m-1'])
check('model lines give first and last seen and tokens, the subagents\' apart', lines.includes('7.5k (2.5k in subagents)') && lines.includes('· 9') && lines.includes('no model name'))
check('each line says its share of the row', lines.includes('100% of the row') && lines.includes('0% of the row'))
check('a model of the Fable limit is marked, another is not', (lines.match(/Fable limit/g) || []).length === 1)
check('no lines, no markup', modelLinesHtml([], []) === '' && modelLinesHtml(undefined, []) === '')
check('the lines are in the session row and the project row', planShown.includes('class="mlines"'))
const evilLines = modelLinesHtml([{ model: hostile, first: hostile, last: hostile, own: t(1, 1), subagents: t(0, 0) }], [hostile])
check('a hostile model name is escaped', !evilLines.includes('<img') && evilLines.includes('&lt;img'))
const evilMeter = meterInfo({ meter: { label: hostile, basis: hostile, fable_models: [] }, accounts: [{ auth_file: 'a.json', meter: { known: true, per_million: 1, window_hours: 168 } }] })
const evilCell = groupRowsHtml(planGroups, new Set(), planAff, name, accounts, evilMeter)
check('a hostile plan name cannot leave the tooltip', !evilCell.includes('<img') && evilCell.includes('&lt;img') && !/title="[^"]*"[^>]*onerror/.test(evilCell))

// rebalance
const plan = { applied: false, pinned: 1, plan: [{ project: '-work-a', from: 'a.json', to: 'b.json' }, { project: '-work-b', from: 'a.json', to: 'a.json' }] }
const shown = rebalanceHtml(plan, name)
check('a plan lists the projects that move, from and to', shown.includes('-work-a') && shown.includes('first') && shown.includes('second') && !shown.includes('-work-b'))
check('a plan counts the ones that stay and the pinned ones', shown.includes('1 stay') && shown.includes('1 pinned'))
check('a plan can be applied or dismissed', shown.includes('data-rebalance="apply"') && shown.includes('data-rebalance="close"'))
const nothing = rebalanceHtml({ applied: false, pinned: 0, plan: [{ project: '-work-b', from: 'a.json', to: 'a.json' }] }, name)
check('a balanced layout says so and cannot be applied', nothing.includes('Already balanced') && !nothing.includes('data-rebalance="apply"'))
const done = rebalanceHtml({ ...plan, applied: true }, name)
check('an applied plan says what moved and offers no second apply', done.includes('Moved 1 project.') && !done.includes('data-rebalance="apply"') && done.includes('data-rebalance="close"'))
const evilPlan = rebalanceHtml({ applied: false, pinned: 0, plan: [{ project: hostile, from: hostile, to: 'a.json' }] }, k => k)
check('hostile text is escaped in the plan', !evilPlan.includes('<img') && evilPlan.includes('&lt;img'))

// the open state
const store = (initial, broken) => {
  const kept = {}
  if (initial !== undefined) kept.k = initial
  return { kept, localStorage: { getItem: () => { if (broken) throw new Error('blocked'); return kept.k ?? null }, setItem: (_, x) => { if (broken) throw new Error('blocked'); kept.k = x } } }
}
const withStore = s => lib({ localStorage: s.localStorage })
const keys = ['p:-work-a', 's:' + A]
const s1 = store(JSON.stringify(keys))
check('the open groups are read back', [...withStore(s1).readOpen()].join('|') === keys.join('|'))
const s2 = store(undefined)
withStore(s2).writeOpen(new Set(keys))
check('the open groups are written as JSON', s2.kept.k === JSON.stringify(keys))
for (const [label, s] of [['nothing stored', store(undefined)], ['not json', store('{nope')], ['not a list', store('{"a":1}')], ['storage blocked', store(undefined, true)]]) {
  check(`${label}: all closed, no error`, withStore(s).readOpen().size === 0)
}
check('only strings are kept', [...withStore(store(JSON.stringify(['p:x', 3, null, { a: 1 }]))).readOpen()].join('|') === 'p:x')
check('writing to blocked storage does not throw', (() => { try { withStore(store(undefined, true)).writeOpen(new Set(['p:x'])); return true } catch { return false } })())
const many = Array.from({ length: 250 }, (_, i) => 'p:' + i)
check('the remembered set is bounded, the newest kept', (() => { const r = [...withStore(store(JSON.stringify(many))).readOpen()]; return r.length === 200 && r[199] === 'p:249' })())

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
