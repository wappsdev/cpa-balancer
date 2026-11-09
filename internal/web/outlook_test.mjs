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
check('pace line', lasts.lines.some(l => l.includes('11.4 Pro-plan points an hour over the week (23.1 while working, about 12 h a day)')))

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

if (fails.length) { console.log('FAIL: ' + fails.join(', ')); process.exit(1) }
console.log('PASS')
