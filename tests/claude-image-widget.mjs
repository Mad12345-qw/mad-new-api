import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import vm from 'node:vm'

const widgetPath = path.resolve(process.argv[2])
const html = fs.readFileSync(widgetPath, 'utf8')
const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/)
assert.ok(scriptMatch, 'Widget script is missing.')

class FakeClassList {
  constructor() {
    this.values = new Set()
  }

  add(value) {
    this.values.add(value)
  }

  remove(value) {
    this.values.delete(value)
  }
}

class FakeElement {
  constructor(tagName = 'div') {
    this.tagName = tagName.toUpperCase()
    this.classList = new FakeClassList()
    this.listeners = new Map()
    this.children = []
    this.hidden = false
    this.disabled = false
    this.textContent = ''
    this.src = ''
    this.href = ''
    this.download = ''
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || []
    listeners.push(listener)
    this.listeners.set(type, listeners)
  }

  dispatch(type) {
    let prevented = false
    const event = {
      type,
      preventDefault() {
        prevented = true
      },
    }
    for (const listener of this.listeners.get(type) || []) listener(event)
    return prevented
  }

  click() {
    this.dispatch('click')
  }

  appendChild(child) {
    this.children.push(child)
  }

  remove() {}

  getBoundingClientRect() {
    return { width: 760, height: 520 }
  }
}

const elements = Object.fromEntries(
  ['preview', 'image', 'status', 'path', 'toolbar', 'open', 'download'].map(
    (id) => [id, new FakeElement(id === 'image' ? 'img' : 'div')]
  )
)
const body = new FakeElement('body')
const requests = []
const notifications = []
const windowListeners = new Map()

function dispatchWindowMessage(data) {
  for (const listener of windowListeners.get('message') || []) listener({ data })
}

const windowValue = {
  parent: {
    postMessage(message) {
      if (message.id) {
        requests.push(message)
        const result =
          message.method === 'ui/initialize'
            ? { hostCapabilities: { openLinks: {}, downloadFile: {} } }
            : {}
        setTimeout(() => dispatchWindowMessage({ id: message.id, result }), 0)
      } else {
        notifications.push(message)
      }
    },
  },
  addEventListener(type, listener) {
    const listeners = windowListeners.get(type) || []
    listeners.push(listener)
    windowListeners.set(type, listeners)
  },
  open() {},
}

const documentValue = {
  body,
  getElementById(id) {
    return elements[id]
  },
  createElement(tagName) {
    return new FakeElement(tagName)
  },
}

class FakeResizeObserver {
  observe() {}
}

vm.runInNewContext(scriptMatch[1], {
  console,
  document: documentValue,
  window: windowValue,
  ResizeObserver: FakeResizeObserver,
  Set,
  String,
  encodeURIComponent,
  setTimeout,
})

await new Promise((resolve) => setTimeout(resolve, 10))
const fixture = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64'
)
const encoded = fixture.toString('base64')
const sourceUrl = 'https://mad.example/image/test.png'
const savedPath = 'C:\\Users\\test\\Pictures\\MadAPI-image.png'

dispatchWindowMessage({
  method: 'ui/notifications/tool-input',
  params: { arguments: { prompt: 'Widget acceptance image', size: '1024x1024' } },
})
dispatchWindowMessage({
  method: 'ui/notifications/tool-result',
  params: {
    content: [{ type: 'text', text: 'Image generation completed.' }],
    structuredContent: {
      image_data: encoded,
      mime_type: 'image/png',
      source_url: sourceUrl,
      filename: 'MadAPI-image.png',
      saved_path: savedPath,
      save_error: '',
    },
  },
})
await new Promise((resolve) => setTimeout(resolve, 10))

assert.equal(elements.preview.hidden, false)
assert.equal(elements.toolbar.hidden, false)
assert.equal(elements.image.src, `data:image/png;base64,${encoded}`)
assert.match(elements.status.textContent, /saved automatically/i)
assert.equal(elements.path.textContent, savedPath)
assert.equal(elements.path.hidden, false)
assert.equal(elements.open.href, sourceUrl)

assert.equal(elements.open.dispatch('click'), true)
await new Promise((resolve) => setTimeout(resolve, 10))
const openRequest = requests.find((request) => request.method === 'ui/open-link')
assert.equal(openRequest.params.url, sourceUrl)

elements.download.dispatch('click')
await new Promise((resolve) => setTimeout(resolve, 10))
const downloadRequest = requests.find(
  (request) => request.method === 'ui/download-file'
)
assert.ok(downloadRequest, 'Download request was not sent.')
const embedded = downloadRequest.params.contents[0]
assert.equal(embedded.type, 'resource')
assert.equal(embedded.resource.mimeType, 'image/png')
assert.deepEqual(Buffer.from(embedded.resource.blob, 'base64'), fixture)
assert.ok(
  notifications.some(
    (notification) =>
      notification.method === 'ui/notifications/initialized'
  )
)

console.log('Claude image widget preview/open/download acceptance passed.')
