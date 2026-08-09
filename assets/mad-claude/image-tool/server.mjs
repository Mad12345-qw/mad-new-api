import { createHash } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";

const SERVER_NAME = "madapi-image";
const SERVER_VERSION = "2.2.0";
const MODEL = "gpt-image-2";
const UI_URI = "ui://madapi-image/image-viewer.html";
const IMAGE_URI_PREFIX = "image://madapi/";
const MAX_IMAGE_BYTES = 20 * 1024 * 1024;
const MAX_CACHE_BYTES = 256 * 1024 * 1024;
const MAX_CACHE_AGE_MS = 24 * 60 * 60 * 1000;
const IMAGE_ID_PATTERN = /^[0-9a-f]{64}$/;

function configLibrary() {
  if (process.env.MADAPI_CLAUDE_CONFIG_LIBRARY) {
    return process.env.MADAPI_CLAUDE_CONFIG_LIBRARY;
  }
  if (process.platform === "darwin") {
    return path.join(
      os.homedir(),
      "Library",
      "Application Support",
      "Claude-3p",
      "configLibrary",
    );
  }
  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData) throw new Error("LOCALAPPDATA is unavailable");
  return path.join(localAppData, "Claude-3p", "configLibrary");
}

function gatewayConfig() {
  const library = configLibrary();
  const meta = JSON.parse(
    fs.readFileSync(path.join(library, "_meta.json"), "utf8"),
  );
  const configId = String(meta.appliedId || "").trim();
  if (!configId) throw new Error("Claude Gateway appliedId is missing");
  const config = JSON.parse(
    fs.readFileSync(path.join(library, `${configId}.json`), "utf8"),
  );
  const baseUrl = String(config.inferenceGatewayBaseUrl || "")
    .trim()
    .replace(/\/+$/, "");
  const apiKey = String(config.inferenceGatewayApiKey || "").trim();
  if (!baseUrl || !apiKey) {
    throw new Error("Claude Gateway URL or API key is missing");
  }
  return { baseUrl, apiKey };
}

function cacheDirectory() {
  if (process.env.MADAPI_IMAGE_CACHE_DIR) {
    fs.mkdirSync(process.env.MADAPI_IMAGE_CACHE_DIR, { recursive: true });
    return process.env.MADAPI_IMAGE_CACHE_DIR;
  }
  const root =
    process.platform === "darwin"
      ? path.join(os.homedir(), "Library", "Caches", "MadAPI")
      : path.join(process.env.LOCALAPPDATA || os.homedir(), "MadAPI");
  const directory = path.join(root, "claude-image-tool", "cache");
  fs.mkdirSync(directory, { recursive: true });
  return directory;
}

function saveDirectory() {
  const directory =
    process.env.MADAPI_IMAGE_SAVE_DIR || path.join(os.homedir(), "Pictures");
  fs.mkdirSync(directory, { recursive: true });
  return path.resolve(directory);
}

function mimeTypeFor(data, headerValue = "") {
  const declared = String(headerValue).split(";", 1)[0].trim().toLowerCase();
  if (
    ["image/png", "image/jpeg", "image/webp", "image/gif"].includes(declared)
  ) {
    return declared;
  }
  if (
    data.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))
  ) {
    return "image/png";
  }
  if (data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff)
    return "image/jpeg";
  if (
    data.subarray(0, 4).toString() === "RIFF" &&
    data.subarray(8, 12).toString() === "WEBP"
  ) {
    return "image/webp";
  }
  if (["GIF87a", "GIF89a"].includes(data.subarray(0, 6).toString()))
    return "image/gif";
  throw new Error("generated URL did not return a supported image type");
}

function extensionFor(mimeType) {
  return {
    "image/png": ".png",
    "image/jpeg": ".jpg",
    "image/webp": ".webp",
    "image/gif": ".gif",
  }[mimeType];
}

async function fetchWithTimeout(url, options, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(url, { ...options, signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function downloadImage(imageUrl) {
  const fixturePath = process.env.MADAPI_IMAGE_TEST_FILE;
  if (fixturePath) {
    const data = fs.readFileSync(fixturePath);
    if (data.length > MAX_IMAGE_BYTES)
      throw new Error("fixture image exceeds 20 MB");
    return { mimeType: mimeTypeFor(data), data };
  }

  const response = await fetchWithTimeout(
    imageUrl,
    {
      headers: {
        Accept: "image/png,image/jpeg,image/webp,image/gif,*/*;q=0.8",
        "User-Agent": "Claude-MadAPI-Image/2.0",
      },
    },
    120000,
  );
  if (!response.ok)
    throw new Error(
      `generated image download returned HTTP ${response.status}`,
    );
  const declaredLength = Number(response.headers.get("content-length") || 0);
  if (declaredLength > MAX_IMAGE_BYTES)
    throw new Error("generated image exceeds 20 MB");
  const data = Buffer.from(await response.arrayBuffer());
  if (data.length > MAX_IMAGE_BYTES)
    throw new Error("generated image exceeds 20 MB");
  return {
    mimeType: mimeTypeFor(data, response.headers.get("content-type")),
    data,
  };
}

function imageIdFor(prompt, size) {
  return createHash("sha256")
    .update(`${prompt}\0${size}`, "utf8")
    .digest("hex");
}

function metadataPath(imageId) {
  return path.join(cacheDirectory(), `${imageId}.json`);
}

function writeMetadata(imageId, metadata) {
  const target = metadataPath(imageId);
  const temporary = `${target}.${process.pid}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(metadata), {
    encoding: "utf8",
    mode: 0o600,
  });
  fs.renameSync(temporary, target);
}

function markPending(imageId) {
  const directory = cacheDirectory();
  for (const extension of [".png", ".jpg", ".webp", ".gif"]) {
    fs.rmSync(path.join(directory, `${imageId}${extension}`), { force: true });
  }
  writeMetadata(imageId, {
    id: imageId,
    state: "pending",
    createdAt: Date.now(),
  });
}

function markFailed(imageId) {
  writeMetadata(imageId, {
    id: imageId,
    state: "failed",
    createdAt: Date.now(),
  });
}

function cleanCache() {
  const directory = cacheDirectory();
  const now = Date.now();
  const files = [];
  for (const name of fs.readdirSync(directory)) {
    const filePath = path.join(directory, name);
    const stat = fs.statSync(filePath);
    if (!stat.isFile()) continue;
    if (now - stat.mtimeMs > MAX_CACHE_AGE_MS) {
      fs.rmSync(filePath, { force: true });
      continue;
    }
    if (!name.endsWith(".json"))
      files.push({ filePath, size: stat.size, mtimeMs: stat.mtimeMs });
  }
  let total = files.reduce((sum, item) => sum + item.size, 0);
  for (const item of files.sort(
    (left, right) => left.mtimeMs - right.mtimeMs,
  )) {
    if (total <= MAX_CACHE_BYTES) break;
    fs.rmSync(item.filePath, { force: true });
    fs.rmSync(item.filePath.replace(/\.[^.]+$/, ".json"), { force: true });
    total -= item.size;
  }
}

function cacheImage(imageId, mimeType, data) {
  const target = path.join(
    cacheDirectory(),
    `${imageId}${extensionFor(mimeType)}`,
  );
  const temporary = `${target}.${process.pid}.tmp`;
  fs.writeFileSync(temporary, data, { mode: 0o600 });
  fs.renameSync(temporary, target);
  writeMetadata(imageId, {
    id: imageId,
    state: "ready",
    mimeType,
    filename: path.basename(target),
    createdAt: Date.now(),
  });
  cleanCache();
}

function cachedImage(imageId) {
  if (!IMAGE_ID_PATTERN.test(imageId)) throw new Error("invalid image id");
  const directory = cacheDirectory();
  const metadata = JSON.parse(fs.readFileSync(metadataPath(imageId), "utf8"));
  if (metadata.state === "failed") throw new Error("image generation failed");
  if (metadata.state !== "ready") throw new Error("image is not ready");
  const imagePath = path.resolve(directory, String(metadata.filename || ""));
  if (path.dirname(imagePath) !== path.resolve(directory))
    throw new Error("invalid cached image path");
  const stat = fs.statSync(imagePath);
  if (!stat.isFile()) throw new Error("cached image is not a file");
  if (stat.size > MAX_IMAGE_BYTES)
    throw new Error("cached image exceeds 20 MB");
  return { imagePath, mimeType: String(metadata.mimeType || "") };
}

function readCachedImage(imageId) {
  const { imagePath, mimeType } = cachedImage(imageId);
  const data = fs.readFileSync(imagePath);
  return { mimeType: mimeTypeFor(data, mimeType), data };
}

function safeFileStem(value) {
  const cleaned = String(value || "")
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, "-")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/[. ]+$/g, "")
    .slice(0, 80);
  return cleaned || "MadAPI-image";
}

function timestampForFile() {
  return new Date()
    .toISOString()
    .replace(/[-:]/g, "")
    .replace("T", "-")
    .slice(0, 15);
}

function saveImage(argumentsValue) {
  const imageId = String(argumentsValue.image_id || "").trim();
  const { imagePath, mimeType } = cachedImage(imageId);
  const extension = extensionFor(mimeType);
  if (!extension) throw new Error("cached image has an unsupported type");
  const directory = saveDirectory();
  const baseName = `${safeFileStem(argumentsValue.title)}-${timestampForFile()}-${imageId.slice(0, 8)}`;
  let destination = "";
  for (let attempt = 0; attempt < 1000; attempt += 1) {
    const suffix = attempt === 0 ? "" : `-${attempt}`;
    const candidate = path.join(directory, `${baseName}${suffix}${extension}`);
    try {
      fs.copyFileSync(imagePath, candidate, fs.constants.COPYFILE_EXCL);
      destination = candidate;
      break;
    } catch (error) {
      if (!error || error.code !== "EEXIST") throw error;
    }
  }
  if (!destination)
    throw new Error("unable to allocate a unique image filename");
  return {
    content: [{ type: "text", text: `Image saved to ${destination}` }],
    structuredContent: { image_id: imageId, saved_path: destination },
    isError: false,
  };
}

async function requestGeneration(prompt, size) {
  const fixtureResponsePath = process.env.MADAPI_IMAGE_TEST_RESPONSE_JSON;
  if (fixtureResponsePath)
    return JSON.parse(fs.readFileSync(fixtureResponsePath, "utf8"));

  const { baseUrl, apiKey } = gatewayConfig();
  const response = await fetchWithTimeout(
    `${baseUrl}/images/generations`,
    {
      method: "POST",
      headers: {
        Authorization: `Bearer ${apiKey}`,
        "x-api-key": apiKey,
        "Content-Type": "application/json",
        Accept: "application/json",
      },
      body: JSON.stringify({
        model: MODEL,
        prompt,
        n: 1,
        size,
        response_format: "url",
      }),
    },
    180000,
  );
  const body = await response.text();
  if (!response.ok)
    throw new Error(
      `MadAPI returned HTTP ${response.status}: ${body.slice(0, 1000)}`,
    );
  return JSON.parse(body);
}

async function generateImage(argumentsValue) {
  const prompt = String(argumentsValue.prompt || "").trim();
  if (!prompt) throw new Error("prompt is required");
  const size = String(argumentsValue.size || "1024x1024").trim();
  if (!["1024x1024", "1536x1024", "1024x1536"].includes(size)) {
    throw new Error("unsupported image size");
  }
  const imageId = imageIdFor(prompt, size);
  markPending(imageId);
  try {
    const result = await requestGeneration(prompt, size);
    const first = Array.isArray(result.data) ? result.data[0] : null;
    const imageUrl =
      first && typeof first.url === "string" ? first.url.trim() : "";
    if (!imageUrl) throw new Error("MadAPI did not return an image URL");
    const { mimeType, data } = await downloadImage(imageUrl);
    cacheImage(imageId, mimeType, data);
    const revisedPrompt = String(first.revised_prompt || "").trim();
    return {
      content: [
        {
          type: "text",
          text:
            "Image generation completed. The image is attached directly to this tool result." +
            (revisedPrompt ? `\nRevised prompt: ${revisedPrompt}` : ""),
        },
        {
          type: "image",
          data: data.toString("base64"),
          mimeType,
        },
      ],
      structuredContent: { model: MODEL, image_id: imageId, size, mimeType },
      isError: false,
    };
  } catch (error) {
    markFailed(imageId);
    throw error;
  }
}

function toolDefinition() {
  return {
    name: "generate_image",
    title: "Generate image",
    description:
      "Default image generation tool. Always use this tool when the user asks to generate, create, draw, design, render, or make an image. It calls MadAPI gpt-image-2 and attaches the generated image directly to the tool result.",
    inputSchema: {
      type: "object",
      properties: {
        prompt: {
          type: "string",
          description: "Detailed image generation prompt.",
        },
        size: {
          type: "string",
          enum: ["1024x1024", "1536x1024", "1024x1536"],
          default: "1024x1024",
        },
      },
      required: ["prompt"],
      additionalProperties: false,
    },
    annotations: {
      readOnlyHint: false,
      destructiveHint: false,
      idempotentHint: false,
      openWorldHint: true,
    },
    _meta: { ui: { resourceUri: UI_URI } },
  };
}

function saveToolDefinition() {
  return {
    name: "save_image",
    title: "Save generated image",
    description: "Save a generated MadAPI image to the local Pictures folder.",
    inputSchema: {
      type: "object",
      properties: {
        image_id: { type: "string", pattern: "^[0-9a-f]{64}$" },
        title: { type: "string" },
      },
      required: ["image_id"],
      additionalProperties: false,
    },
    annotations: {
      readOnlyHint: false,
      destructiveHint: false,
      idempotentHint: false,
      openWorldHint: false,
    },
    _meta: { ui: { visibility: ["app"] } },
  };
}

function readResource(uri) {
  if (uri === UI_URI) {
    return {
      contents: [
        {
          uri: UI_URI,
          mimeType: "text/html;profile=mcp-app",
          text: fs.readFileSync(
            new URL("./widget.html", import.meta.url),
            "utf8",
          ),
          _meta: { ui: { csp: { connectDomains: [], resourceDomains: [] } } },
        },
      ],
    };
  }
  if (uri.startsWith(IMAGE_URI_PREFIX)) {
    const { mimeType, data } = readCachedImage(
      uri.slice(IMAGE_URI_PREFIX.length),
    );
    return {
      contents: [{ uri, mimeType, blob: data.toString("base64") }],
    };
  }
  throw new Error("unknown resource");
}

async function handle(message) {
  const method = message.method;
  const id = message.id;
  if (method === "initialize") {
    return {
      jsonrpc: "2.0",
      id,
      result: {
        protocolVersion: message.params?.protocolVersion || "2025-03-26",
        capabilities: {
          tools: { listChanged: false },
          resources: { subscribe: false, listChanged: false },
        },
        serverInfo: { name: SERVER_NAME, version: SERVER_VERSION },
      },
    };
  }
  if (method === "ping") return { jsonrpc: "2.0", id, result: {} };
  if (method === "tools/list") {
    return {
      jsonrpc: "2.0",
      id,
      result: { tools: [toolDefinition(), saveToolDefinition()] },
    };
  }
  if (method === "tools/call") {
    const name = message.params?.name;
    if (name !== "generate_image" && name !== "save_image")
      throw new Error("unknown tool");
    return {
      jsonrpc: "2.0",
      id,
      result:
        name === "generate_image"
          ? await generateImage(message.params.arguments || {})
          : saveImage(message.params.arguments || {}),
    };
  }
  if (method === "resources/list") {
    return {
      jsonrpc: "2.0",
      id,
      result: {
        resources: [
          {
            uri: UI_URI,
            name: "MadAPI image viewer",
            description: "Displays a generated image inline in Claude.",
            mimeType: "text/html;profile=mcp-app",
          },
        ],
      },
    };
  }
  if (method === "resources/read") {
    return {
      jsonrpc: "2.0",
      id,
      result: readResource(String(message.params?.uri || "")),
    };
  }
  if (id === undefined || id === null) return null;
  return {
    jsonrpc: "2.0",
    id,
    error: { code: -32601, message: "Method not found" },
  };
}

function send(payload) {
  process.stdout.write(`${JSON.stringify(payload)}\n`);
}

const input = readline.createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});
for await (const rawLine of input) {
  const line = rawLine.trim();
  if (!line) continue;
  let id = null;
  try {
    const message = JSON.parse(line);
    id = message.id ?? null;
    const response = await handle(message);
    if (response) send(response);
  } catch (error) {
    if (id !== null) {
      send({
        jsonrpc: "2.0",
        id,
        error: {
          code: -32000,
          message: error instanceof Error ? error.message : String(error),
        },
      });
    }
  }
}
