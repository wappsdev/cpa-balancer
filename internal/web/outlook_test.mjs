// The panel's own outlookView() on what the balancer answers: the words for each verdict.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL("assets/app.js", import.meta.url), "utf8")
const fn = src.match(/function outlookView\(o\) \{[\s\S]*?\n\}\n/)[0]
const span = src.match(/function span\(seconds\) \{[\s\S]*?\n\}\n/)[0]
const outlookView = new Function(`${span}; ${fn}; return outlookView`)()
const pace = { per_hour: 11.42, active_per_hour: 23.12, hours_per_day: 11.9, source: 'measured' }
const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const lasts = outlookView({ percent: 53.3, verdict: 'lasts', lasts: 1.14, runs_out: '2026-11-12T04:00:00Z', horizon: '2026-11-18T12:00:00Z', pace })
check('lasts chip', lasts.chip.text === 'Lasts' && lasts.chip.tone === 'accent')
check('lasts headroom', lasts.lines[0].includes('114% of this pace'))
check('runs out line', lasts.lines.some(l => /^At this pace the room runs out .+ \(counting the resets before it\)\.$/.test(l)))
check('lasts past the horizon', outlookView({ percent: 5, verdict: 'lasts', lasts: 2, horizon: '2026-11-18T12:00:00Z', pace }).lines.some(l => /^At this pace the room lasts past .+ \(counting the resets before it\)\.$/.test(l)))
check('no room seconds', !lasts.lines.some(l => l.includes('room open now')))
check('pace line', lasts.lines.some(l => l.includes('11.4 Pro-plan points an hour, by the last 7 days, no method measured (23.1 while working, about 12 h a day)')))
const paceBy = basis => outlookView({ percent: 5, verdict: 'lasts', lasts: 2, pace: { ...pace, basis, active_per_hour: 0 } }).lines.at(-1)
check('pace line by typical', paceBy('typical') === 'Pace: 11.4 Pro-plan points an hour, by typical, a heavy but normal day.')
check('pace line by the fastest', paceBy('peak') === 'Pace: 11.4 Pro-plan points an hour, by peak, the fastest method (typical not measured).')

const tight = outlookView({ percent: 50, verdict: 'tight', lasts: 1.14, pace })
check('tight chip', tight.chip.text === 'Tight' && tight.lines[0].includes('up to 114%'))

const out = outlookView({ percent: 60, verdict: 'out', lasts: 0.86, runs_out:'2026-11-10T22:00:00Z', pace,
  dry: { seconds: 7.2 * 3600, refill: 'gmail', at: '2026-11-11T07:00:00Z', next: { lasts: false, seconds: 54000 } } })
check('out chip', out.chip.tone === 'bad')
check('out slower', out.lines[0].includes('stays out 7h 12m until gmail resets') && out.lines[0].includes('Work about 14% slower'))
check('out next', out.lines[1] === 'After that refill the room runs out again, for 15h 0m.')
check('refill lasts', outlookView({ percent: 1, verdict: 'out', lasts: 0.5, runs_out:'2026-11-10T22:00:00Z', dry: { seconds: 60, refill: 'x', at: '2026-11-11T07:00:00Z', next: { lasts: true } } }).lines[1] === 'The refill after that lasts at this pace.')
check('unknown', outlookView({ percent: 0, verdict: 'unknown' }).lines[0].startsWith('Not enough use'))
check('average pace', outlookView({ percent: 5, verdict: 'lasts', lasts: 10, pace: { per_hour: 3, source: 'average' } }).lines.some(l => l.includes("each window's own average")))
check('huge headroom', outlookView({ percent: 5, verdict: 'lasts', lasts: 10 }).lines[0].includes('1000%+'))

// The panel's methodsTable(): the week at each method's pace, for Σ, the account in use and the next ones.
const tableFn = src.match(/function methodsTable\(o, v, now, name\) \{[\s\S]*?\n\}\n/)[0]
const methodsTable = new Function(`${span}; ${tableFn}; return methodsTable`)()
const NOW = Date.parse('2026-11-12T12:00:00Z')
const at = h => new Date(NOW + h * 3600000).toISOString()
// two Pro accounts, 100 each once reserves are released: 200 points of room in all
const status = { final_cap: 100, decision: { main: { account: 'a' }, main_order: ['a', 'b'] },
  accounts: [{ auth_file: 'b', weight: 1, caps: { main: 85 } }, { auth_file: 'a', weight: 1, caps: { main: 85 } }] }
const week = { verdict: 'out', horizon: at(336), by_method: [
  { name: 'peak', per_hour: 2, verdict: 'out', lasts: 0.5, runs_out: at(15),
    dry: { seconds: 85 * 3600, refill: 'a', at: at(100), lasts: 0.4 },
    accounts: [{ key: 'a', handoff: at(10), margin: 1 }, { key: 'b', handoff: at(15), exhausts: true, margin: 1 }] },
  { name: 'typical', per_hour: 1.5, used: true, verdict: 'tight', lasts: 1.1, runs_out: at(20),
    accounts: [{ key: 'a', handoff: at(12), margin: 1 }, { key: 'b', handoff: at(20), exhausts: true, margin: 1 }] },
  { name: 'trend', per_hour: 1, verdict: 'lasts', lasts: 1.3, runs_out: at(30),
    accounts: [{ key: 'a', handoff: at(20), margin: 1 }, { key: 'b', handoff: at(30), exhausts: true, margin: 1 }] },
  { name: 'now', per_hour: 0.5, verdict: 'lasts', lasts: 3, accounts: [{ key: 'a', margin: 1.4 }, { key: 'b' }] },
] }
const t = methodsTable(week, status, NOW, f => f.toUpperCase())
check('columns in the methods\' order, the used one marked', JSON.stringify(t.columns) === JSON.stringify([{ name: 'peak', used: false }, { name: 'typical', used: true }, { name: 'trend', used: false }, { name: 'now', used: false }]))
check('rows: Σ, the account in use, the next ones', t.rows.map(r => `${r.kind}:${r.label}`).join(' ') === 'sum:Σ active:A next:B')
const cell = (r, c) => t.rows[r].cells[c]
// Σ's pace is in percent of all the room: 2 of 200 points an hour
check('Σ pace', cell(0, 0).pace === '1.0%/h' && cell(1, 0).pace === '2.0%/h' && cell(0, 3).pace === '0.3%/h')
check('Σ red where work stops within the week', cell(0, 0).flag === '⚑15h 0m' && cell(0, 0).stops && cell(0, 0).tone === 'bad')
check('Σ says the gap and the slowdown in its title', cell(0, 0).title.includes('3d 13h') && cell(0, 0).title.includes('60% slower'))
check('Σ yellow at typical, tight', cell(0, 1).pace === '0.8%/h' && cell(0, 1).flag === '⚑20h 0m' && !cell(0, 1).stops && cell(0, 1).tone === 'warn')
check('Σ green when it lasts but not at 1.4 times the pace', cell(0, 2).flag === '⚑1d 6h' && !cell(0, 2).stops && cell(0, 2).tone === 'ok')
check('Σ past the look-ahead', cell(0, 3).flag === '⚑>14d 0h' && cell(0, 3).tone === '')
check('a handoff is yellow', cell(1, 0).flag === '⚑10h 0m' && cell(1, 0).tone === 'warn' && !cell(1, 0).stops)
check('the run-out Σ shows red is red on its account', cell(2, 0).flag === '⚑15h 0m' && cell(2, 0).stops && cell(2, 0).tone === 'bad')
check('a run-out past the week stays yellow', cell(2, 2).stops && cell(2, 2).tone === 'warn')
check('a margin is a bare flag', cell(1, 3).flag === '⚑' && cell(1, 3).tone === 'ok')
check('no flag at all', cell(2, 3).flag === '—' && cell(2, 3).tone === '')
// a method the balancer did not measure keeps its column: a dash, and why in its title
const why = 'the last 24 h are not faster than the last 3 days'
const absent = methodsTable({ ...week, by_method: [week.by_method[0], week.by_method[1], { name: 'trend', reason: why }, week.by_method[3]] }, status, NOW, f => f)
check('an absent method keeps its column', JSON.stringify(absent.columns.map(c => c.name)) === '["peak","typical","trend","now"]' && absent.rows.every(r => r.cells.length === 4))
check('an absent method says why', absent.columns[2].reason === why && absent.rows.every(r => r.cells[2].flag === '—' && r.cells[2].pace === '' && r.cells[2].tone === '' && r.cells[2].title === why))
check('no methods, no table', methodsTable({ verdict: 'out' }, status, NOW, f => f) === null)
// under the table, how each method is worked out, and why an absent one is not there
check('notes say each method', t.notes?.length === 5 && t.notes[0] === 'peak: the heaviest 24 hours of the last 7 days, as if every day were like it'
  && t.notes[1] === "typical: a heavy but normal day — the 75th percentile of the last 7 days' 24-hour windows"
  && t.notes[2] === "trend: the last day's pace plus half of how far it exceeds the last 3 days' pace"
  && t.notes[3] === 'now: the pace while working in the last 5 hours × the hours worked per day'
  && t.notes[4] === 'Σ and the outlook above use typical (falling back to the fastest method when typical cannot be measured).')
check('a note says why a method is absent', absent.notes?.[2] === `trend: the last day's pace plus half of how far it exceeds the last 3 days' pace — not measured: ${why}`)

if (fails.length) { console.log('FAIL: ' + fails.join(', ')); process.exit(1) }
console.log('PASS')
