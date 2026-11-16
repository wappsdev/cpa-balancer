// What the page says about affinity: where a request of a session went and why, as each row of
// sessionsView carries it. The grouped card has its own test (grouped_test.mjs).
import { load } from './extract.mjs'
const { sessionsView, reasonText: say } = load({
  consts: ['MAX_SESSION_DEPTH', 'REASONS', 'reasonText'], fns: ['sessionsView'], returns: ['sessionsView', 'reasonText'],
})
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

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
