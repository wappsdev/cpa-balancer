// The Mythos/Fable share warning: one line per account the status lists, none without any. The
// text comes from the plugin; the page only escapes it. Names are made up.
import { load } from './extract.mjs'

const { fableWarningsHtml } = load({
  consts: ['esc'],
  fns: ['fableWarningsHtml'],
  returns: ['fableWarningsHtml'],
})

const fails = []
const check = (name, ok) => { if (!ok) fails.push(name) }

const line = n => ({ auth_file: n + '.json', name: n, util: 82, message: `Mythos/Fable share on ${n} at 82% — consider switching the advisor to a non-Fable model until the reset` })
const two = fableWarningsHtml([line('first'), line('second')])
check('one line per account', (two.match(/<p class="banner warn"/g) || []).length === 2)
check('the lines keep the order', two.indexOf('first') < two.indexOf('second'))
check('the message is shown', two.includes('Mythos/Fable share on first at 82%'))
check('the message is escaped', fableWarningsHtml([{ message: '<b>x</b>' }]).includes('&lt;b&gt;x&lt;/b&gt;'))
check('no warnings, no lines', fableWarningsHtml(undefined) === '' && fableWarningsHtml([]) === '')

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1) }
console.log('PASS')
