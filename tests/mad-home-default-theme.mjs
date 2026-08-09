import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'

const source = fs.readFileSync(process.argv[2], 'utf8')
const listeners = new Map()
const storage = new Map()
let reloads = 0

const context = {
  Date: { now: () => 1786269600000 },
  document: {
    cookie: '',
    documentElement: {
      classList: { add() {}, remove() {} },
    },
  },
  window: {
    addEventListener(name, listener) {
      listeners.set(name, listener)
    },
    location: { reload() { reloads += 1 } },
    sessionStorage: {
      getItem(key) { return storage.get(key) || null },
      setItem(key, value) { storage.set(key, value) },
    },
  },
}

vm.runInNewContext(source, context)
assert.ok(listeners.has('vite:preloadError'))
assert.ok(listeners.has('unhandledrejection'))

let prevented = 0
listeners.get('unhandledrejection')({
  preventDefault() { prevented += 1 },
  reason: new Error('Failed to fetch dynamically imported module'),
})
assert.equal(prevented, 1)
assert.equal(reloads, 1)

listeners.get('unhandledrejection')({
  preventDefault() { prevented += 1 },
  reason: new Error('Invalid API key'),
})
assert.equal(prevented, 1)
assert.equal(reloads, 1)

listeners.get('vite:preloadError')({ preventDefault() { prevented += 1 } })
assert.equal(prevented, 2)
assert.equal(reloads, 1)

console.log('MadAPI stale frontend asset recovery acceptance passed.')
