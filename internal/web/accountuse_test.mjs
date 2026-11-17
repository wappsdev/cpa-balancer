// What each project, worktree and session used of each account: the compact line under a row, the
// account card's list of projects, and the escaping of both. Account and folder names are made up.
import { load } from './extract.mjs'

const when = 'const when = t => `<span class="when">${esc(t)}</span>`;'
const { accountUseHtml, accountProjectsHtml, sessionsView, projectGroups, groupRowsHtml } = load({
  consts: ['esc', 'num', 'MAX_SESSION_DEPTH', 'REASONS', 'reasonText', 'folderLabel'],
  fns: ['accountUseHtml', 'accountProjectsHtml', 'sessionsView', 'projectGroups', 'homeInfo', 'toggleHtml', 'pinSelectHtml', 'placementText', 'homeText',
    'usedIn', 'listedUse', 'rowFigures', 'worktreeRowHtml', 'projectRowHtml', 'sessionRowHtml', 'subRowHtml', 'groupRowsHtml',
    'planShareText', 'ultraShareOf', 'meterTip', 'planCellHtml', 'modelLinesHtml'],
  extra: when,
  returns: ['accountUseHtml', 'accountProjectsHtml', 'sessionsView', 'projectGroups', 'groupRowsHtml'],
})

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }
const name = k => ({ 'w.json': 'wapps', 'g.json': 'gmail' }[k] || k)

const uses = {
  'g.json': { tokens5h: 0, tokens7d: 4000, share5h: null, share7d: 0.6 },
  'w.json': { tokens5h: 18000, tokens7d: 40000, share5h: 2.1, share7d: 3.4 },
}
const closed = accountUseHtml(uses, name)
check('a closed row shows the account names with the percent of each window', closed.includes('wapps</span> 5h ~2.1% · 7d ~3.4%') && closed.includes('gmail</span> 7d ~0.6%'))
check('a closed row shows no tokens', !closed.includes(' tok'))
check('the account that served most in the 7 days comes first', closed.indexOf('wapps') < closed.indexOf('gmail'))
check('a window the account served nothing in is left out', !closed.includes('gmail</span> 5h'))
const open = accountUseHtml(uses, name, true)
check('an open row adds the tokens of each window', open.includes('5h 18.0k tok ~2.1% · 7d 40.0k tok ~3.4%') && open.includes('7d 4.0k tok ~0.6%'))
check('a row that used no account has no line', accountUseHtml({}, name) === '' && accountUseHtml(undefined, name) === '' && accountUseHtml({ 'w.json': { tokens5h: 0, tokens7d: 0 } }, name) === '')
check('an account with no factor shows its tokens and a dash, never a number', accountUseHtml({ 'p.json': { tokens5h: 0, tokens7d: 5000, share5h: null, share7d: null } }, name, true).includes('7d 5.0k tok —'))
check('the key of accounts past the history cap has no percent', accountUseHtml({ '(other)': { tokens5h: 0, tokens7d: 900, share5h: null, share7d: null } }, name, true).includes('(other)</span> 7d 900 tok —'))
check('a value that is not a plain number is a dash', accountUseHtml({ 'w.json': { tokens5h: 1, tokens7d: 0, share5h: '<b>', share7d: null } }, name).includes('5h —'))

const hostile = k => `<img src=x onerror="a&b">${k}`
const evil = accountUseHtml({ 'w.json': { tokens5h: 1, tokens7d: 2, share5h: 1, share7d: 1 } }, hostile)
check('an account name is escaped', !evil.includes('<img') && evil.includes('&lt;img src=x onerror=&quot;a&amp;b&quot;&gt;'))

const account = { auth_file: 'w.json', by_project: [
  { project: '-work-alpha', tokens7d: 30000, share7d: 2.5 },
  { project: '-work-<b>x</b>', tokens7d: 9000, share7d: null },
  { project: '', tokens7d: 1000, share7d: 0.04 },
], more_projects: 2 }
const aff = { names: { '-work-alpha': 'alpha', '-work-<b>x</b>': '<b>x</b>"' } }
const list = accountProjectsHtml(account, aff)
check('the card lists each project with tokens and percent of the account', list.includes('alpha</span>') && list.includes('30.0k tok · ~2.5%'))
check('a project with no factor shows a dash and the group of no project is named', list.includes('9.0k tok · —') && list.includes('no project') && list.includes('1.0k tok · &lt;0.1%') && !list.includes('<0.1%'))
check('the cut is said', list.includes('+2 more'))
check('a folder label is escaped, in the text and in the title', !list.includes('<b>x') && list.includes('&lt;b&gt;x&lt;/b&gt;&quot;') && list.includes('title="-work-&lt;b&gt;x&lt;/b&gt;"'))
check('an account no project used has no list', accountProjectsHtml({ auth_file: 'w.json' }, aff) === '' && accountProjectsHtml({ by_project: [] }, aff) === '')

// the rows: a project, a worktree and a session carry the line
const A = 'aaaaaaaa-1111-2222-3333-444444444444'
const t = (requests, input) => ({ requests, input_tokens: input, output_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0 })
const byAccount = { 'w.json': { tokens5h: 100, tokens7d: 100, share5h: 1.5, share7d: 1.5 } }
const win = { total: t(2, 100), other: { sessions: 0, total: t(0, 0) }, sessions: [{ id: A, last: '2026-11-15T10:00:00Z', own: t(2, 100), total: t(2, 100), accounts: { 'w.json': t(2, 100) }, models: {}, by_account: byAccount }] }
const wtAff = {
  session_projects: { [A]: '-work-shop-work-feat' }, project_of: { '-work-shop-work-feat': '-work-shop', '-work-shop': '-work-shop' },
  projects: [{ project: '-work-shop', five_hour: { requests: 2, tokens: 100 }, week: { requests: 2, tokens: 100 }, accounts: byAccount,
    worktrees: [{ project: '-work-shop-work-feat', name: 'feat', path: '/x/feat', five_hour: { requests: 2, tokens: 100 }, week: { requests: 2, tokens: 100 }, accounts: byAccount }] }],
}
const v = sessionsView(win)
check('a session row keeps its use of each account', v.rows[0].byAccount['w.json'].share7d === 1.5)
const groups = projectGroups(v, wtAff, 'week')
check('a project and its worktree carry the balancer\'s split by account', groups[0].accounts['w.json'].tokens7d === 100 && groups[0].trees[0].accounts['w.json'].tokens7d === 100)
const ids = new Set()
const collapsed = groupRowsHtml(groups, ids, wtAff, name, [])
check('a collapsed project row shows the names with the percent only', collapsed.includes('wapps</span> 5h ~1.5% · 7d ~1.5%') && !collapsed.includes('tok ~'))
const everything = groupRowsHtml(groups, new Set(['p:-work-shop', 'w:-work-shop-work-feat']), wtAff, name, [])
check('an open project, worktree and session show the tokens too', (everything.match(/5h 100 tok ~1\.5%/g) || []).length === 3)

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
