import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import readline from 'node:readline'

const serverPath = path.resolve(process.argv[2])
const widgetPath = path.join(path.dirname(serverPath), 'widget.html')
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'madapi-image-mcp-'))
const cache = path.join(root, 'cache')
const saved = path.join(root, 'Pictures')
const imagePath = path.join(root, 'fixture.png')
const responsePath = path.join(root, 'response.json')
fs.writeFileSync(
  imagePath,
  Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
)
fs.writeFileSync(responsePath, JSON.stringify({ data: [{ url: 'fixture://image' }] }))

const child = spawn(process.execPath, [serverPath], {
  env: {
    ...process.env,
    MADAPI_IMAGE_CACHE_DIR: cache,
    MADAPI_IMAGE_SAVE_DIR: saved,
    MADAPI_IMAGE_TEST_FILE: imagePath,
    MADAPI_IMAGE_TEST_RESPONSE_JSON: responsePath,
  },
  stdio: ['pipe', 'pipe', 'inherit'],
})
const lines = readline.createInterface({ input: child.stdout })
const pending = new Map()
lines.on('line', (line) => {
  const message = JSON.parse(line)
  const operation = pending.get(message.id)
  if (operation) {
    pending.delete(message.id)
    operation(message)
  }
})

let nextId = 1
function request(method, params = {}) {
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
}

try {
  const initialized = await request('initialize', { protocolVersion: '2025-03-26' })
  assert.equal(initialized.serverInfo.name, 'madapi-image')
  const tools = await request('tools/list')
  assert.equal(tools.tools.length, 2)
  assert.equal(tools.tools[0].name, 'generate_image')
  assert.equal(tools.tools[1].name, 'save_image')
  assert.deepEqual(tools.tools[1]._meta.ui.visibility, ['app'])
  assert.equal(tools.tools[0]._meta.ui.resourceUri, 'ui://madapi-image/image-viewer.html')
  assert.match(tools.tools[0].description, /Always use this tool/)

  const resources = await request('resources/list')
  assert.equal(resources.resources[0].mimeType, 'text/html;profile=mcp-app')
  const widget = await request('resources/read', { uri: resources.resources[0].uri })
  assert.equal(widget.contents[0].mimeType, 'text/html;profile=mcp-app')
  assert.match(widget.contents[0].text, /id="preview"/)
  assert.match(widget.contents[0].text, /ui\/notifications\/tool-result/)
  assert.match(widget.contents[0].text, /image_data/)
  assert.match(widget.contents[0].text, /ui\/open-link/)
  assert.match(widget.contents[0].text, /ui\/download-file/)
  assert.match(widget.contents[0].text, /link\.download/)
  assert.doesNotMatch(widget.contents[0].text, /resources\/read/)
  assert.doesNotMatch(widget.contents[0].text, /tools\/call/)
  assert.doesNotMatch(widget.contents[0].text, /save_image/)
  assert.doesNotMatch(widget.contents[0].text, /!\[[^\]]*\]\(https?:/)

  const prompt = 'MCP protocol acceptance image'
  const generated = await request('tools/call', {
    name: 'generate_image',
    arguments: { prompt, size: '1024x1024' },
  })
  assert.equal(generated.isError, false)
  assert.equal(generated.structuredContent.model, 'gpt-image-2')
  assert.equal(generated.content.some((item) => item.type === 'image'), false)
  assert.equal(generated.structuredContent.mime_type, 'image/png')
  assert.deepEqual(
    Buffer.from(generated.structuredContent.image_data, 'base64'),
    fs.readFileSync(imagePath)
  )
  assert.equal(generated.structuredContent.source_url, 'fixture://image')
  assert.equal(path.dirname(generated.structuredContent.saved_path), saved)
  assert.ok(fs.existsSync(generated.structuredContent.saved_path))
  assert.deepEqual(
    fs.readFileSync(generated.structuredContent.saved_path),
    fs.readFileSync(imagePath)
  )
  const image = await request('resources/read', {
    uri: `image://madapi/${generated.structuredContent.image_id}`,
  })
  assert.equal(image.contents[0].mimeType, 'image/png')
  assert.ok(Buffer.from(image.contents[0].blob, 'base64').length > 0)
  assert.ok(fs.readdirSync(cache).some((name) => name.endsWith('.png')))
  const savedImage = await request('tools/call', {
    name: 'save_image',
    arguments: {
      image_id: generated.structuredContent.image_id,
      title: 'MCP protocol acceptance image',
    },
  })
  assert.equal(savedImage.isError, false)
  assert.equal(path.dirname(savedImage.structuredContent.saved_path), saved)
  assert.ok(fs.existsSync(savedImage.structuredContent.saved_path))
  assert.deepEqual(
    fs.readFileSync(savedImage.structuredContent.saved_path),
    fs.readFileSync(imagePath)
  )
  console.log('Claude image MCP acceptance passed.')
} finally {
  child.kill()
  fs.rmSync(root, { recursive: true, force: true })
}
