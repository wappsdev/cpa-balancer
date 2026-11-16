// Pulls pure pieces out of assets/app.js by name, so a test runs the page's own code without a
// browser. A function ends at the first "}" in column 0; a const is one line, or an object or arrow
// body that ends the same way.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL('assets/app.js', import.meta.url), 'utf8')

export const fn = name => {
  const m = src.match(new RegExp(`function ${name}\\([^)]*\\) \\{[\\s\\S]*?\\n\\}\\n`))
  if (!m) throw new Error('function not found: ' + name)
  return m[0]
}
export const cst = name => {
  const m = src.match(new RegExp(`const ${name} = (?:\\{[\\s\\S]*?\\n\\};|[^\\n]+)\\n`))
  if (!m) throw new Error('const not found: ' + name)
  return m[0]
}

// load builds the named pieces into one scope and returns the functions asked for. Extra stands
// for things the page gets from the browser (a stub for `when`, for example).
export const load = ({ consts = [], fns = [], extra = '', returns, params = {} }) => {
  const body = [extra, ...consts.map(cst), ...fns.map(fn)].join('\n')
  return new Function(...Object.keys(params), `${body}\nreturn { ${returns.join(', ')} }`)(...Object.values(params))
}
