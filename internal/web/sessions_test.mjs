// The panel's own sessionsView() on what the balancer answers: rows, shares, nesting and escaping.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL("assets/app.js", import.meta.url), "utf8")
const esc = src.match(/const esc = [^\n]+\n/)[0]
const depthLimit = src.match(/const MAX_SESSION_DEPTH = [^\n]+\n/)[0]
const fn = src.match(/function sessionsView\(w\) \{[\s\S]*?\n\}\n/)[0]
const rowHtml = src.match(/function sessionRowHtml\(r, name\) \{[\s\S]*?\n\}\n/)[0]
const num = src.match(/const num = [^\n]+\n/)[0]
const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const { sessionsView, sessionRowHtml } = new Function(`${esc}; ${num}; ${when}; ${depthLimit}; ${fn}; ${rowHtml}; return { sessionsView, sessionRowHtml }`)()
const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const t = (requests, input, output, cacheRead = 0, cacheWrite = 0) =>
  ({ requests, input_tokens: input, output_tokens: output, cache_read_tokens: cacheRead, cache_creation_tokens: cacheWrite })
const window = {
  total: t(10, 800, 100, 5000, 100), advisor: t(2, 60, 40), accounts: { 'a.json': t(10, 800, 100) },
  other: { sessions: 3, total: t(1, 10, 0), advisor: t(0, 0, 0) },
  sessions: [
    { id: 'aaaaaaaa-1111-2222-3333-444444444444', last: '2026-11-15T10:00:00Z', own: t(2, 100, 10), total: t(6, 500, 50, 4000, 50), advisor: t(2, 60, 40),
      advisor_models: { 'claude-fable-5-2': t(2, 60, 40) }, accounts: { 'a.json': t(4, 300, 30), 'b.json': t(2, 200, 20) }, models: {}, estimated: true,
      subagents: [{ id: 'bbbbbbbb-1111-2222-3333-444444444444', parent: 'aaaaaaaa-1111-2222-3333-444444444444', last: '2026-11-15T09:00:00Z',
        own: t(4, 400, 40), total: t(4, 400, 40), advisor: t(0, 0, 0), accounts: { 'a.json': t(4, 400, 40) }, models: {} }] },
    { id: '', last: '2026-11-15T08:00:00Z', own: t(3, 290, 40), total: t(3, 290, 40), advisor: t(0, 0, 0), accounts: { 'a.json': t(3, 290, 40) }, models: {} },
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
check('advisor use is its own figure', v.rows[0].advisor.requests === 2 && v.rows[0].advisor.tokens === 100 && v.rows[0].advisor.models[0] === 'claude-fable-5-2' && v.rows[1].advisor.requests === 0)
check('estimated rows are marked', v.rows[0].estimated && !v.rows[1].estimated)
check('the other row sums what was cut off', v.other.sessions === 3 && v.other.tokens === 10)
check('an empty window has no rows and no division by zero', (() => { const e = sessionsView({ total: t(0, 0, 0), sessions: [], other: { sessions: 0, total: t(0, 0, 0) } }); return e.rows.length === 0 && e.total.tokens === 0 })())
check('a window without sessions or other is read as empty', sessionsView({}).rows.length === 0)

// A chain deeper than the limit is not walked past it: its use is in the last row's total already.
const chain = (n) => ({ id: 'd' + n, own: t(1, 1, 0), total: t(10 - n, 10 - n, 0), advisor: t(0, 0, 0), accounts: {}, models: {}, subagents: n < 9 ? [chain(n + 1)] : [] })
check('depth is bounded', sessionsView({ total: t(10, 10, 0), sessions: [chain(0)] }).rows.length === 8)

// Every string comes from a client header or a model name: none may reach the page unescaped.
const hostile = '"><img src=x onerror=alert(1)>'
const bad = sessionsView({ total: t(1, 1, 1), accounts: {}, other: { sessions: 0, total: t(0, 0, 0) },
  sessions: [{ id: hostile, last: hostile, own: t(1, 1, 1), total: t(1, 1, 1), advisor: t(1, 1, 1), advisor_models: { [hostile]: t(1, 1, 1) }, accounts: { [hostile]: t(1, 1, 1) }, models: {} }] })
const html = sessionRowHtml(bad.rows[0], k => k)
check('hostile text is escaped in the row', !html.includes('<img') && html.includes('&lt;img') && html.split('&lt;img').length - 1 === 5)
check('the full id is escaped into the title', html.includes('title="&quot;&gt;&lt;img'))

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
