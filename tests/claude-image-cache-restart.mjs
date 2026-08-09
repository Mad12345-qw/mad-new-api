import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import readline from 'node:readline'

const sourceDirectory = path.resolve(process.argv[2])
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'madapi-image-restart-'))
const installedDirectory = path.join(root, 'installed')
const cacheDirectory = path.join(installedDirectory, 'cache')
const saveDirectory = path.join(root, 'Pictures')
const imagePath = path.join(root, 'fixture.png')
const responsePath = path.join(root, 'response.json')
const serverPath = path.join(installedDirectory, 'server.mjs')
const widgetPath = path.join(installedDirectory, 'widget.html')

fs.mkdirSync(cacheDirectory, { recursive: true })
fs.writeFileSync(
  imagePath,
  Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
    'base64'
  )
)
fs.writeFileSync(responsePath, JSON.stringify({ data: [{ url: 'fixture://image' }] }))

function installAssets() {
  fs.mkdirSync(installedDirectory, { recursive: true })
  fs.copyFileSync(path.join(sourceDirectory, 'server.mjs'), serverPath)
  fs.copyFileSync(path.join(sourceDirectory, 'widget.html'), widgetPath)
}

function startServer() {
  const child = spawn(process.execPath, [serverPath], {
    env: {
      ...process.env,
      MADAPI_IMAGE_CACHE_DIR: cacheDirectory,
      MADAPI_IMAGE_SAVE_DIR: saveDirectory,
      MADAPI_IMAGE_TEST_FILE: imagePath,
      MADAPI_IMAGE_TEST_RESPONSE_JSON: responsePath,
    },
    stdio: ['pipe', 'pipe', 'inherit'],
  })
  const lines = readline.createInterface({ input: child.stdout })
  const pending = new Map()
  let nextId = 1
  lines.on('line', (line) => {
    const message = JSON.parse(line)
    const operation = pending.get(message.id)
    if (operation) {
      pending.delete(message.id)
      operation(message)
    }
  })
  return {
    request(method, params = {}) {
      const id = nextId++
      child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`)
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`${method} timed out`)), 5000)
        pending.set(id, (message) => {
          clearTimeout(timer)
          if (message.error) reject(new Error(message.error.message))
          else resolve(message.result)
        })
      })
    },
    async stop() {
      child.stdin.end()
      await new Promise((resolve) => {
        child.once('exit', resolve)
        setTimeout(() => {
          child.kill()
          resolve()
        }, 3000).unref()
      })
    },
  }
}

try {
  installAssets()
  const first = startServer()
  await first.request('initialize', { protocolVersion: '2025-03-26' })
  const generated = await first.request('tools/call', {
    name: 'generate_image',
    arguments: { prompt: 'Persistent image acceptance', size: '1024x1024' },
  })
  const imageId = generated.structuredContent.image_id
  await first.stop()

  const cachedFilesBeforeUpgrade = fs.readdirSync(cacheDirectory).sort()
  assert.ok(cachedFilesBeforeUpgrade.some((name) => name.endsWith('.png')))

  installAssets()
  assert.deepEqual(fs.readdirSync(cacheDirectory).sort(), cachedFilesBeforeUpgrade)

  const second = startServer()
  await second.request('initialize', { protocolVersion: '2025-03-26' })
  const reads = await Promise.all(
    Array.from({ length: 128 }, () =>
      second.request('resources/read', { uri: `image://madapi/${imageId}` })
    )
  )
  for (const image of reads) {
    assert.equal(image.contents[0].mimeType, 'image/png')
    assert.ok(Buffer.from(image.contents[0].blob, 'base64').length > 0)
  }
  await second.stop()
  console.log('Claude image cache restart acceptance passed (128 concurrent reads).')
} finally {
  fs.rmSync(root, { recursive: true, force: true })
}
