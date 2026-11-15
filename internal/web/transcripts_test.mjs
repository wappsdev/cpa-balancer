// The panel's own transcriptsView() / transcriptRowHtml() on what /transcripts answers: rows, shares,
// tokens by model, advisor tokens and escaping. Nothing here may carry an account.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL("assets/app.js", import.meta.url), "utf8")
const esc = src.match(/const esc = [^\n]+\n/)[0]
const num = src.match(/const num = [^\n]+\n/)[0]
const view = src.match(/function transcriptsView\(w\) \{[\s\S]*?\n\}\n/)[0]
const rowHtml = src.match(/function transcriptRowHtml\(r\) \{[\s\S]*?\n\}\n/)[0]
const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const { transcriptsView, transcriptRowHtml } = new Function(`${esc}; ${num}; ${when}; ${view}; ${rowHtml}; return { transcriptsView, transcriptRowHtml }`)()
const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const t = (requests, input, output, cacheRead = 0, cacheWrite = 0) =>
  ({ requests, input_tokens: input, output_tokens: output, cache_read_tokens: cacheRead, cache_creation_tokens: cacheWrite })
const A = 'aaaaaaaa-1111-4222-8333-444444444444'
const window = {
  total: t(10, 800, 100, 5000, 100),
  advisor: t(2, 700, 60),
  other: { sessions: 3, total: t(1, 10, 0), advisor: t(0, 0, 0) },
  sessions: [
    { id: A, title: 'Made-up session name', title_kind: 'custom', last: '2026-11-15T10:00:00Z', total: t(7, 500, 50, 4000, 50), models: { 'claude-opus-5-5': t(3, 300, 30), 'claude-haiku-5-5': t(4, 200, 20, 4000, 50) },
      advisor: t(2, 700, 60), advisor_models: { 'claude-fable-5-2': t(2, 700, 60) },
      subagents: [{ id: A + '/ab12cd34ef56', last: '2026-11-15T09:00:00Z', total: t(4, 200, 20, 4000, 50), models: { 'claude-haiku-5-5': t(4, 200, 20, 4000, 50) }, advisor: t(0, 0, 0) }] },
    { id: 'bbbbbbbb-1111-4222-8333-444444444444', last: '2026-11-14T08:00:00Z', total: t(3, 290, 40), models: { 'claude-opus-5-5': t(3, 290, 40) }, advisor: t(0, 0, 0) },
  ],
}
const v = transcriptsView(window)
check('window total counts input, output and cache writes', v.total.tokens === 1000 && v.total.requests === 10)
check('rows: a root, its subagent, the next root', v.rows.map(r => `${r.depth}:${r.id}`).join('|') === `0:${A}|1:${A}/ab12cd34ef56|0:bbbbbbbb-1111-4222-8333-444444444444`)
check('the short id is eight characters, a subagent shows its agent id', v.rows[0].label === 'aaaaaaaa' && v.rows[1].label === 'ab12cd34')
check('a root counts its family with the cache reads apart', v.rows[0].tokens === 600 && v.rows[0].requests === 7 && v.rows[0].cacheRead === 4000)
check('a session with a name carries it with its kind, a subagent and an unnamed session none', v.rows[0].title === 'Made-up session name' && v.rows[0].titleKind === 'custom' && !v.rows[1].title && !v.rows[2].title)
check('share is of the window total', v.rows[0].share === 60 && v.rows[2].share === 33)
check('models are listed largest first with their tokens', JSON.stringify(v.rows[0].models) === JSON.stringify([{ key: 'claude-opus-5-5', tokens: 330 }, { key: 'claude-haiku-5-5', tokens: 270 }]))
check('advisor tokens are their own figure, with the advisor model', v.rows[0].advisor.requests === 2 && v.rows[0].advisor.tokens === 760 && v.rows[0].advisor.models[0].key === 'claude-fable-5-2' && v.rows[2].advisor.requests === 0)
check('the window carries its advisor total apart', v.advisor.requests === 2 && v.advisor.tokens === 760)
check('the other row sums what was cut off', v.other.sessions === 3 && v.other.tokens === 10)
check('no row has an account', v.rows.every(r => !('accounts' in r)))
check('an empty answer has no rows and no division by zero', (() => { const e = transcriptsView({ total: t(0, 0, 0), sessions: [] }); return e.rows.length === 0 && e.total.tokens === 0 })())
check('an answer with nothing in it is read as empty', transcriptsView({}).rows.length === 0 && transcriptsView(null).rows.length === 0)
check('the truncated flag is passed on', transcriptsView({ truncated: true }).truncated === true && transcriptsView({}).truncated === false)

const html = transcriptRowHtml(v.rows[0])
check('a row shows the model, its tokens and the advisor', html.includes('claude-opus-5-5') && html.includes('claude-fable-5-2') && html.includes('title="' + A + '"'))
check('the name is the label, with the short id beside it', html.includes('class="stitle"') && html.includes('Made-up session name') && html.includes('>aaaaaaaa<') && html.includes('set with /rename'))
check('an unnamed row shows the short id alone', !transcriptRowHtml(v.rows[2]).includes('stitle') && transcriptRowHtml(v.rows[2]).includes('>bbbbbbbb<'))
check('a subagent row is nested', transcriptRowHtml(v.rows[1]).includes('class="sub"'))

// Every string comes from the store, which holds ids and model names from files on disk: none may reach the page unescaped.
const hostile = '"><img src=x onerror=alert(1)>'
const bad = transcriptsView({ total: t(1, 1, 1), sessions: [{ id: hostile, title: hostile, title_kind: hostile, last: hostile, total: t(1, 1, 1), models: { [hostile]: t(1, 1, 1) },
  advisor: t(1, 1, 1), advisor_models: { [hostile]: t(1, 1, 1) } }] })
const badHtml = transcriptRowHtml(bad.rows[0])
check('hostile text is escaped in the row', !badHtml.includes('<img') && badHtml.includes('&lt;img') && badHtml.split('&lt;img').length - 1 === 8)
check('the full id is escaped into the title', badHtml.includes('title="&quot;&gt;&lt;img'))
check('no attribute is single-quoted', !badHtml.includes("='"))

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
