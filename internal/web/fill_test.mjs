// What the page says about fill and about the room left unused at a weekly reset. Account names
// and folders are made up.
import { load } from './extract.mjs'

const { fillText, placementText, homeText, reasonText, wasteHtml } = load({
  consts: ['esc', 'REASONS', 'reasonText'],
  fns: ['fillText', 'placementText', 'homeInfo', 'homeText', 'wasteHtml'],
  returns: ['fillText', 'placementText', 'homeText', 'reasonText', 'wasteHtml'],
})

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }
const name = f => ({ 'a.json': 'first', 'b.json': 'second' }[f] || f)

const at = '2026-11-15T10:00:00Z'
const fill = { resets_at: '2026-11-15T20:00:00Z', room: 10.6 }
check('fill says when the account resets and its room, as it was at the placement', fillText(fill, at) === 'fill: resets in 10h with 11% room')
check('a placement by fill says so', placementText({ account: 'b.json', reason: 'fill', at, fill }, name).includes('fill: resets in 10h with 11% room'))
check('a placement by fill is not marked a fallback', !placementText({ account: 'b.json', reason: 'fill', at, fill }, name).includes('warn'))
check('a running session that stays where it started is said in words', reasonText('running') !== 'running')
check('other reasons are unchanged', placementText({ account: 'b.json', reason: 'project_home', at }, name).includes('project home'))

const aff = { folder_pins: [], homes: [{ project: '-work-alpha', account: 'b.json', since: at, fill }, { project: '-work-beta', account: 'a.json', since: at }] }
check('a home fill chose says why', homeText('-work-alpha', aff, name).includes('fill: resets in 10h with 11% room'))
check('a home placed by room says project home only', homeText('-work-beta', aff, name).includes('project home') && !homeText('-work-beta', aff, name).includes('fill'))

const accounts = [
  { auth_file: 'a.json', waste: { week: 400.4, month: 1200, resets: [
    { resets_at: '2026-11-14T10:00:00Z', util: 60, weight: 10, wasted: 400.4, observed_at: '2026-11-14T09:00:00Z' },
    { resets_at: '2026-11-07T10:00:00Z', util: 20, weight: 10, wasted: 799.6, observed_at: '2026-11-07T09:00:00Z' },
  ] } },
  { auth_file: 'b.json', waste: { week: 0, month: 30, resets: [{ resets_at: '2026-11-01T10:00:00Z', util: 70, weight: 1, wasted: 30, observed_at: '2026-11-01T08:00:00Z' }] } },
]
const html = wasteHtml(accounts, name)
check('each account is listed by its name', html.includes('first') && html.includes('second'))
check('the 7 and 30 day totals are in points', html.includes('>400<') && html.includes('>1200<') && html.includes('>30<'))
check('the last resets show their wasted points', html.includes('400 pts') && html.includes('800 pts'))
check('a reset says the last reading it is from', html.includes('60%'))
check('the totals of all accounts are summed', html.includes('>1230<'))
check('no reset seen yet is said', wasteHtml([{ auth_file: 'a.json', waste: { week: 0, month: 0, resets: [] } }], name).includes('No weekly reset'))
check('a view without waste says no reset was seen', wasteHtml([{ auth_file: 'a.json' }], name).includes('No weekly reset seen yet.'))
const idle = wasteHtml([{ auth_file: 'a.json', waste: { week: 500, month: 500, resets: [{ resets_at: '2026-11-14T10:00:00Z', util: 0, weight: 5, wasted: 500, estimated: true }] } }], name)
check('a week with no real reading is marked estimated, with its points', idle.includes('500 pts') && idle.includes('estimated') && !idle.includes('0%'))
check('a week with a reading is not marked estimated', !html.includes('estimated'))

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
