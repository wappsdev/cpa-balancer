// The panel's own affinity views: where a session went and why, the pin controls, the pin and home lists.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL("assets/app.js", import.meta.url), "utf8")
const grab = re => { const m = src.match(re); if (!m) throw new Error('not found: ' + re); return m[0] }
const esc = grab(/const esc = [^\n]+\n/)
const num = grab(/const num = [^\n]+\n/)
const depthLimit = grab(/const MAX_SESSION_DEPTH = [^\n]+\n/)
const reasons = grab(/const REASONS = \{[\s\S]*?\n\};\n/)
const reasonText = grab(/const reasonText = [^\n]+\n/)
const sessionsFn = grab(/function sessionsView\(w, picks\) \{[\s\S]*?\n\}\n/)
const placementFn = grab(/function placementHtml\(r, name, accounts\) \{[\s\S]*?\n\}\n/)
const listed = grab(/const LISTED = [^\n]+\n/)
const rowFn = grab(/function sessionRowHtml\(r, name, accounts\) \{[\s\S]*?\n\}\n/)
const pinsFn = grab(/function pinsHtml\(aff, name\) \{[\s\S]*?\n\}\n/)
const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const { sessionsView, sessionRowHtml, pinsHtml, reasonText: say } = new Function(
  `${esc}; ${num}; ${when}; ${depthLimit}; ${reasons}; ${reasonText}; ${listed}; ${sessionsFn}; ${placementFn}; ${rowFn}; ${pinsFn}; return { sessionsView, sessionRowHtml, pinsHtml, reasonText }`)()
const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const t = (requests, input, output) => ({ requests, input_tokens: input, output_tokens: output, cache_read_tokens: 0, cache_creation_tokens: 0 })
const SID = 'aaaaaaaa-1111-2222-3333-444444444444'
const win = { total: t(2, 10, 0), other: { sessions: 0, total: t(0, 0, 0) }, sessions: [
  { id: SID, last: '2026-11-15T10:00:00Z', own: t(1, 5, 0), total: t(2, 10, 0), accounts: { 'a.json': t(2, 10, 0) }, models: {},
    subagents: [{ id: SID + '/ab12', parent: SID, last: '2026-11-15T10:00:00Z', own: t(1, 5, 0), total: t(1, 5, 0), accounts: { 'a.json': t(1, 5, 0) }, models: {} }] },
  { id: 'bbbbbbbb-1111-2222-3333-444444444444', last: '2026-11-15T09:00:00Z', own: t(1, 1, 0), total: t(1, 1, 0), accounts: { 'a.json': t(1, 1, 0) }, models: {} },
] }
const picks = {
  [SID]: { account: 'b.json', reason: 'project_home', project: '-work-alpha', at: '2026-11-15T10:00:00Z' },
  [SID + '/ab12']: { account: 'b.json', reason: 'project_home', project: '-work-alpha', at: '2026-11-15T10:00:00Z' },
}
const name = k => ({ 'a.json': 'first', 'b.json': 'second' }[k] || k)
const accounts = [{ auth_file: 'a.json', name: 'first' }, { auth_file: 'b.json', name: 'second' }]

const v = sessionsView(win, picks)
check('a row carries the pick of its own session id', v.rows[0].pick?.account === 'b.json' && v.rows[1].pick?.account === 'b.json' && v.rows[2].pick === null)
check('a subagent the balancer has no pick for shows its main session\'s', (() => {
  const only = sessionsView(win, { [SID]: picks[SID] })
  return only.rows[1].id === SID + '/ab12' && only.rows[1].pick?.account === 'b.json'
})())
check('the view works without picks', sessionsView(win).rows.every(r => r.pick === null) && sessionsView(win, null).rows.length === 3)

for (const [reason, word] of [['project_home', 'project home'], ['folder_pin', 'folder pin'], ['session_pin', 'session pin'], ['fallback', 'fallback after cap'],
  ['folder_pin_fallback', 'folder pin: account full, fallback'], ['session_pin_fallback', 'session pin: account full, fallback'], ['no_project', 'no project']]) {
  check(`reason ${reason} is said in words`, say(reason) === word)
}
check('an unknown reason is shown as it came', say('something_new') === 'something_new')

const root = sessionRowHtml(v.rows[0], name, accounts)
check('the row says the account and the reason', root.includes('second') && root.includes('project home'))
check('the row names the project, with the full name in a title', root.includes('title="-work-alpha"'))
check('a root row with a pick has pin controls for its folder and for itself',
  root.includes('data-pin="folder"') && root.includes('data-pin="session"') && root.includes(`data-key="${SID}"`) && root.includes('data-key="-work-alpha"'))
check('the account choice lists every account', root.includes('<option value="a.json"') && root.includes('<option value="b.json"'))
const sub = sessionRowHtml(v.rows[1], name, accounts)
check('a subagent row says its placement but has no pin controls (it follows its parent)', sub.includes('project home') && !sub.includes('data-pin='))
const bare = sessionRowHtml(v.rows[2], name, accounts)
check('a session without a pick shows a dash and no controls', !bare.includes('data-pin=') && bare.includes('<td class="left placement">—</td>'))
const noProject = sessionRowHtml(sessionsView(win, { [SID]: { account: 'a.json', reason: 'no_project', at: 'x' } }).rows[0], name, accounts)
check('a session with no project can be pinned itself only', noProject.includes('data-pin="session"') && !noProject.includes('data-pin="folder"') && noProject.includes('no project'))

const aff = {
  homes: [{ project: '-work-alpha', account: 'b.json', since: 'x', last: 'y', full: true }, { project: '-work-beta', account: 'a.json', since: 'x', last: 'y', full: false }],
  folder_pins: [{ key: '-work-alpha', account: 'a.json', source: 'ui', full: true }],
  session_pins: [{ key: SID, account: 'b.json', source: 'config', full: false }],
}
const list = pinsHtml(aff, name)
check('a pin shows its account', list.includes('first') && list.includes('second'))
check('a pin whose account is full says it falls back', list.includes('account full: falling back'))
check('a pin set on the page can be removed', list.includes('data-unpin="folder"') && list.includes('data-key="-work-alpha"'))
check('a pin set in the config is read only and says so', !list.includes('data-unpin="session"') && list.includes('set in the config'))
check('a session pin shows the short id with the full id in the title', list.includes('>aaaaaaaa<') && list.includes(`title="${SID}"`))
check('a home whose account is full says it will move', list.includes('full: moves on the next request'))
check('nothing to show gives nothing', pinsHtml({ homes: [], folder_pins: [], session_pins: [] }, name) === '' && pinsHtml(undefined, name) === '')
const many = pinsHtml({ homes: Array.from({ length: 25 }, (_, i) => ({ project: `-work-${i}`, account: 'a.json', full: false })), folder_pins: [], session_pins: [] }, name)
check('long lists are cut and say how many more', (many.match(/<li/g) || []).length === 20 && many.includes('and 5 more'))

// Folder names, session ids and account names are user data: none may reach the page unescaped.
const hostile = '"><img src=x onerror=alert(1)>'
const evilPicks = { [SID]: { account: hostile, reason: hostile, project: hostile, at: hostile } }
const evil = sessionRowHtml(sessionsView(win, evilPicks).rows[0], k => k, [{ auth_file: hostile, name: hostile }])
check('hostile text is escaped in the row', !evil.includes('<img') && evil.includes('&lt;img'))
check('no attribute can be closed by a folder name', !/data-key="[^"]*"[^>]*onerror/.test(evil) && !evil.includes('"><img'))
const evilList = pinsHtml({ homes: [{ project: hostile, account: hostile, full: true }], folder_pins: [{ key: hostile, account: hostile, source: 'ui', full: true }],
  session_pins: [{ key: hostile, account: hostile, source: 'ui', full: false }] }, k => k)
check('hostile text is escaped in the lists', !evilList.includes('<img') && evilList.includes('&lt;img') && !evilList.includes('"><img'))

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
