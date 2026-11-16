// The panel's own sessionsView() on what the balancer answers: rows, shares, nesting and escaping.
import { load } from './extract.mjs'
const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const { sessionsView, sessionRowHtml } = load({
  consts: ['esc', 'num', 'MAX_SESSION_DEPTH', 'REASONS', 'reasonText'], extra: when,
  fns: ['sessionsView', 'toggleHtml', 'pinSelectHtml', 'placementText', 'sessionRowHtml'], returns: ['sessionsView', 'sessionRowHtml'],
})
const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const t = (requests, input, output, cacheRead = 0, cacheWrite = 0) =>
  ({ requests, input_tokens: input, output_tokens: output, cache_read_tokens: cacheRead, cache_creation_tokens: cacheWrite })
const window = {
  total: t(10, 800, 100, 5000, 100), accounts: { 'a.json': t(10, 800, 100) },
  other: { sessions: 3, total: t(1, 10, 0) },
  sessions: [
    { id: 'aaaaaaaa-1111-2222-3333-444444444444', last: '2026-11-15T10:00:00Z', own: t(2, 100, 10), total: t(6, 500, 50, 4000, 50),
      accounts: { 'a.json': t(4, 300, 30), 'b.json': t(2, 200, 20) }, models: {},
      subagents: [{ id: 'bbbbbbbb-1111-2222-3333-444444444444', parent: 'aaaaaaaa-1111-2222-3333-444444444444', last: '2026-11-15T09:00:00Z',
        own: t(4, 400, 40), total: t(4, 400, 40), accounts: { 'a.json': t(4, 400, 40) }, models: {} }] },
    { id: '', last: '2026-11-15T08:00:00Z', own: t(3, 290, 40), total: t(3, 290, 40), accounts: { 'a.json': t(3, 290, 40) }, models: {} },
  ],
}
const v = sessionsView(window)
check('window total counts input, output and cache writes', v.total.tokens === 1000 && v.total.requests === 10)
check('rows: root, its subagent, the empty id', v.rows.map(r => `${r.depth}:${r.id}`).join('|') === '0:aaaaaaaa-1111-2222-3333-444444444444|1:bbbbbbbb-1111-2222-3333-444444444444|0:')
check('short id is the first eight characters', v.rows[0].label === 'aaaaaaaa' && v.rows[1].label === 'bbbbbbbb')
check('the empty id is named in words', v.rows[2].label === 'no session' && v.rows[2].none)
check('a root row counts its family and shows the cache apart', v.rows[0].tokens === 600 && v.rows[0].requests === 6 && v.rows[0].cacheRead === 4000)
check('a root also carries its own tokens', v.rows[0].ownTokens === 110)
check('share is of the window total', v.rows[0].share === 60 && v.rows[1].share === 44 && v.rows[2].share === 33)
check('accounts are listed largest first with their share of the row', JSON.stringify(v.rows[0].accounts) === JSON.stringify([{ key: 'a.json', share: 60 }, { key: 'b.json', share: 40 }]))
check('a row with one account says 100', v.rows[1].accounts.length === 1 && v.rows[1].accounts[0].share === 100)
check('the live sessions card has no advisor and no estimate: those come from transcripts only', v.rows.every(r => !('advisor' in r) && !('estimated' in r)))
check('the other row sums what was cut off', v.other.sessions === 3 && v.other.tokens === 10)
check('an empty window has no rows and no division by zero', (() => { const e = sessionsView({ total: t(0, 0, 0), sessions: [], other: { sessions: 0, total: t(0, 0, 0) } }); return e.rows.length === 0 && e.total.tokens === 0 })())
check('a window without sessions or other is read as empty', sessionsView({}).rows.length === 0)

// A chain deeper than the limit is not walked past it: its use is in the last row's total already.
const chain = (n) => ({ id: 'd' + n, own: t(1, 1, 0), total: t(10 - n, 10 - n, 0), accounts: {}, models: {}, subagents: n < 9 ? [chain(n + 1)] : [] })
check('depth is bounded', sessionsView({ total: t(10, 10, 0), sessions: [chain(0)] }).rows.length === 8)

// Every string comes from a client header or a model name: none may reach the page unescaped.
const hostile = '"><img src=x onerror=alert(1)>'
const bad = sessionsView({ total: t(1, 1, 1), accounts: {}, other: { sessions: 0, total: t(0, 0, 0) },
  sessions: [{ id: hostile, last: hostile, own: t(1, 1, 1), total: t(1, 1, 1), accounts: { [hostile]: t(1, 1, 1) }, models: {} }] })
const html = sessionRowHtml({ row: bad.rows[0], subs: [] }, false, undefined, k => k, [])
check('hostile text is escaped in the row', !html.includes('<img') && html.includes('&lt;img') && !html.includes('"><img'))
check('the full id is escaped into the title', html.includes('title="&quot;&gt;&lt;img'))

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
