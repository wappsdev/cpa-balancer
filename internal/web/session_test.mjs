// Encodes a session the way the Management Center (CPA 8.0.5's bundled build) does, then runs the
// panel's own managementKey() on it.
import { readFileSync } from 'node:fs'
const src = readFileSync(new URL("assets/app.js", import.meta.url), "utf8")
const fn = src.match(/function managementKey\(\) \{[\s\S]*?\n\}/)[0]
const prefix = src.match(/const PREFIX = "([^"]+)";/)[1]
const store = {}
globalThis.localStorage = { getItem: k => store[k] ?? null }
globalThis.location = { host: '127.0.0.1:8317' }
Object.defineProperty(globalThis, 'navigator', { value: { userAgent: 'Mozilla/5.0 Test' } })
// the Management Center: enc::v2:: + base64(utf8(json) XOR utf8("cli-proxy-api-webui::secure-storage|<host>"))
const key = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}`)
const data = new TextEncoder().encode(JSON.stringify({ state: { apiBase: 'http://127.0.0.1:8317', managementKey: 'test-key-123', rememberPassword: true }, version: 0 }))
store['cli-proxy-auth'] = 'enc::v2::' + btoa(String.fromCharCode(...data.map((b, i) => b ^ key[i % key.length])))
const managementKey = new Function('PREFIX', `${fn}; return managementKey`)(prefix)
const got = managementKey()
console.log(got === 'test-key-123' ? 'PASS' : `FAIL: got ${got}`)
process.exit(got === 'test-key-123' ? 0 : 1)
