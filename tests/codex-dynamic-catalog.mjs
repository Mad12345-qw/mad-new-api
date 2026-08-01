import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import http from 'node:http'
import path from 'node:path'
import readline from 'node:readline'

const [codexPath, codexHome, fixturePath, expectedToken] = process.argv.slice(2)
assert(codexPath && codexHome && fixturePath && expectedToken, 'codex path, home, catalog fixture, and token are required')

const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'))
let catalogVersion = 1
let requestCount = 0
const requestWaiters = []

function catalog() {
  const model = structuredClone(fixture.models[0])
  model.slug = `madapi-dynamic-v${catalogVersion}`
  model.display_name = `MadAPI Dynamic V${catalogVersion}`
  model.comp_hash = `madapi-dynamic-v${catalogVersion}`
  return { models: [model] }
}

function notifyRequestWaiters() {
  for (let index = requestWaiters.length - 1; index >= 0; index -= 1) {
    const waiter = requestWaiters[index]
    if (requestCount >= waiter.expected) {
      clearTimeout(waiter.timer)
      requestWaiters.splice(index, 1)
      waiter.resolve()
    }
  }
}

function waitForRequestCount(expected) {
  if (requestCount >= expected) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timed out waiting for model request ${expected}`)), 15_000)
    requestWaiters.push({ expected, resolve, timer })
  })
}

const server = http.createServer((request, response) => {
  if (new URL(request.url, 'http://localhost').pathname !== '/models') {
    response.writeHead(404).end()
    return
  }
  assert.equal(request.headers.authorization, `Bearer ${expectedToken}`)
  requestCount += 1
  notifyRequestWaiters()
  response.writeHead(200, { 'content-type': 'application/json', etag: `"catalog-${catalogVersion}"` })
  response.end(JSON.stringify(catalog()))
})

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
const address = server.address()
assert(address && typeof address === 'object')
const baseUrl = `http://127.0.0.1:${address.port}`
const configPath = path.join(codexHome, 'config.toml')
const config = fs.readFileSync(configPath, 'utf8')
assert(config.includes('https://mad.myddns.me/codex/v1'))
fs.writeFileSync(configPath, config.replace('https://mad.myddns.me/codex/v1', baseUrl), 'utf8')

function startCodex() {
  const child = spawn(codexPath, ['app-server', '--stdio'], {
    env: { ...process.env, CODEX_HOME: codexHome },
    shell: process.platform === 'win32',
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  const pending = new Map()
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { stderr += chunk })
  const lines = readline.createInterface({ input: child.stdout })
  lines.on('line', (line) => {
    let message
    try { message = JSON.parse(line) } catch { return }
    const waiter = pending.get(String(message.id))
    if (waiter) {
      pending.delete(String(message.id))
      waiter.resolve(message)
    }
  })

  function request(id, method, params) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(String(id))
        reject(new Error(`timed out waiting for ${method}; stderr: ${stderr}`))
      }, 20_000)
      pending.set(String(id), {
        resolve: (message) => {
          clearTimeout(timer)
          resolve(message)
        },
      })
      child.stdin.write(`${JSON.stringify({ id, method, params })}\n`)
    })
  }

  return {
    child,
    request,
    notify(method, params = {}) {
      child.stdin.write(`${JSON.stringify({ method, params })}\n`)
    },
    async stop() {
      if (process.platform === 'win32' && child.pid) {
        await new Promise((resolve) => {
          spawn('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' })
            .once('exit', resolve)
        })
      } else {
        child.kill('SIGTERM')
      }
      if (child.exitCode === null) {
        await new Promise((resolve) => child.once('exit', resolve))
      }
      lines.close()
    },
  }
}

async function verifyVersion(expectedVersion, expectedRequestCount) {
  const app = startCodex()
  try {
    const initialized = await app.request(1, 'initialize', {
      clientInfo: { name: 'madapi_catalog_test', title: 'MadAPI Catalog Test', version: '1.0.0' },
    })
    assert(initialized.result, JSON.stringify(initialized))
    app.notify('initialized')
    await waitForRequestCount(expectedRequestCount)
    let modelIds = []
    for (let attempt = 0; attempt < 20; attempt += 1) {
      const listed = await app.request(2 + attempt, 'model/list', { limit: 100, includeHidden: false })
      assert(listed.result, JSON.stringify(listed))
      modelIds = listed.result.data.map((model) => model.model)
      if (modelIds.includes(`madapi-dynamic-v${expectedVersion}`)) return
      await new Promise((resolve) => setTimeout(resolve, 250))
    }
    assert.fail(modelIds.join(', '))
  } finally {
    await app.stop()
  }
}

try {
  await verifyVersion(1, 1)
  catalogVersion = 2
  await verifyVersion(2, 2)
  assert(requestCount >= 2, 'Codex did not refresh the remote catalog after restart')
  process.stdout.write('Dynamic model catalog refresh passed.\n')
} finally {
  server.close()
}
