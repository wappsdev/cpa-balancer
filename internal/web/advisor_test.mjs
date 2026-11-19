// The advisor pin card: the beta that marks a request, the model whose accounts serve it, those
// accounts by their short names and states, and the warning when all of them are out. Names are
// made up.
import { load } from './extract.mjs'

const { advisorPinHtml, ADVISOR_OUT } = load({
  consts: ['esc', 'EXTRA_STATE', 'ADVISOR_OUT'],
  fns: ['advisorPinHtml'],
  returns: ['advisorPinHtml', 'ADVISOR_OUT'],
})

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }
const name = f => ({ 'g.json': 'second', 'x.json': 'third' }[f] || f)

const pin = { model: 'claude-mythos-9-2', display_name: 'Claude Extra 2', beta_prefix: 'advisor-tool-', all_out: false, accounts: [
  { auth_file: 'x.json', state: 'ready' }, { auth_file: 'g.json', state: 'full' },
] }
const html = advisorPinHtml(pin, name)
check('the card names the beta and the model', html.includes('advisor-tool-') && html.includes('claude-mythos-9-2'))
check('the accounts show in order with their states', html.indexOf('third') < html.indexOf('second') && html.includes('<b>2</b> second · full</span>'))
check('a full account is marked bad', html.includes('chip bad'))
check('no warning while an account is ready', !html.includes(ADVISOR_OUT))
check('the warning says to move the advisor to Fable for now', ADVISOR_OUT === "Mythos danışman: CVP hesaplarının hepsi dolu; danışmanı geçici olarak Fable'a çevirin")
const out = advisorPinHtml({ ...pin, all_out: true, accounts: [{ auth_file: 'x.json', state: 'held back' }, { auth_file: 'g.json', state: 'full' }] }, name)
check('every account out gives the warning', out.includes(ADVISOR_OUT.replace("'", '&#39;')) || out.includes(ADVISOR_OUT))
check('no pin, no card', advisorPinHtml(null, name) === '' && advisorPinHtml(undefined, name) === '')

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
