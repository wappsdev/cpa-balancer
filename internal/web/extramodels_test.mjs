// The extra models card: each model with the accounts it is pinned to, by their short names, in the
// order they are tried, and a refusal mark when none is ready. Account names are made up.
import { load } from './extract.mjs'

const { extraModelsHtml } = load({
  consts: ['esc', 'EXTRA_STATE'],
  fns: ['extraModelsHtml'],
  returns: ['extraModelsHtml'],
})

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }
const name = f => ({ 'g.json': 'second', 'w.json': 'first' }[f] || f)

const models = [{ id: 'claude-extra-1', display_name: 'Claude Extra 1', accounts: [
  { auth_file: 'g.json', name: 'second', state: 'ready' },
  { auth_file: 'w.json', name: 'first', state: 'held back' },
] }]
const html = extraModelsHtml(models, name)
check('the model is named with its id', html.includes('Claude Extra 1') && html.includes('<code>claude-extra-1</code>'))
check('the accounts show by their short names, in order', html.indexOf('second') > 0 && html.indexOf('second') < html.indexOf('first'))
check('a ready account carries no state', html.includes('<b>1</b> second</span>'))
check('an account not ready says why', html.includes('<b>2</b> first · held back</span>') && html.includes('chip warn'))
check('a model with a ready account is not marked refused', !html.includes('refused'))

const down = extraModelsHtml([{ id: 'claude-extra-1', display_name: 'Claude Extra 1', accounts: [
  { auth_file: 'g.json', state: 'disabled' }, { auth_file: 'w.json', state: 'not loaded' }] }], name)
check('a model with no ready account is marked refused', down.includes('refused'))
check('disabled and not loaded accounts are marked bad', (down.match(/chip bad/g) || []).length === 3)

check('no models, no cards', extraModelsHtml([], name) === '' && extraModelsHtml(undefined, name) === '')

const evil = extraModelsHtml([{ id: '<i>', display_name: '<b>x</b>', accounts: [{ auth_file: 'q', state: '<s>' }] }], () => '<img>')
check('model, account and state are escaped', !evil.includes('<i>') && !evil.includes('<b>x') && !evil.includes('<img>') && !evil.includes('<s>'))

if (fails.length) { console.log('FAIL:\n  ' + fails.join('\n  ')); process.exit(1) }
console.log('PASS')
