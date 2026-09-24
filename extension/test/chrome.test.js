// The claude-chrome route's page scripts, as the Go generator writes them
// (internal/history/testdata/chrome/*.js, kept identical to the generator
// by TestChromeScriptGolden). Each runs here in a fresh VM context with a
// mocked fetch and document, the way javascript_tool runs it in a tab.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const NONCE = '0123456789abcdef0123456789abcdef';
const TOKEN = 'secret-access-token-never-saved';
const scriptPath = (src) => fileURLToPath(new URL(`../../internal/history/testdata/chrome/${src}.js`, import.meta.url));
const script = (src) => readFileSync(scriptPath(src), 'utf8');
const fixture = (p) => JSON.parse(readFileSync(new URL('../../internal/history/testdata/' + p, import.meta.url)));

function json(body, status = 200, type = 'application/json') {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': type } });
}

function png(tag) {
  const b = new Uint8Array(64 + tag.length);
  b.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  b.set(new TextEncoder().encode(tag), 64);
  return b;
}

function image(tag) {
  const b = png(tag);
  return new Response(b, { status: 200, headers: { 'content-type': 'image/png', 'content-length': String(b.length) } });
}

// page runs src against route (url, init) => Response and returns the
// script's return value, the downloads it made and every fetch.
async function page(src, route) {
  const calls = [];
  const downloads = [];
  const blobs = new Map();
  class PageURL extends URL {
    static createObjectURL(b) {
      const u = `blob:https://page/${blobs.size}`;
      blobs.set(u, b);
      return u;
    }
    static revokeObjectURL() {}
  }
  const document = {
    body: { appendChild() {} },
    createElement(tag) {
      const el = {
        tag,
        style: {},
        click() {
          downloads.push({ tag: el.tag, name: el.download, blob: blobs.get(el.href) });
        },
        remove() {},
      };
      return el;
    },
  };
  const fetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });
    return (await route(String(url), init)) || json({ detail: 'not found' }, 404);
  };
  const ctx = { fetch, document, URL: PageURL, URLSearchParams, Blob, btoa, setTimeout: () => 0 };
  const result = await vm.runInNewContext(src, ctx, { filename: 'tincan-page.js' });
  const files = [];
  for (const d of downloads) files.push({ ...d, body: d.blob ? await d.blob.text() : null });
  return { result, files, calls };
}

test('generated scripts parse (node --check)', () => {
  for (const src of ['chatgpt', 'claude-ai']) {
    execFileSync(process.execPath, ['--check', scriptPath(src)]);
  }
});

const CG = 'https://chatgpt.com';
const CG_IDS = ['6a1f0c2e-1111-4a2b-9c3d-000000000001', '6a1f0c2e-1111-4a2b-9c3d-000000000002', '6a1f0c2e-1111-4a2b-9c3d-000000000003'];

function chatgptRoute(overrides = {}) {
  return (url, init) => {
    if (overrides[url]) return overrides[url]();
    if (url === `${CG}/api/auth/session`) return json({ accessToken: TOKEN });
    assert.equal(init.headers && init.headers.Authorization, 'Bearer ' + TOKEN, url);
    if (url === `${CG}/backend-api/conversations?offset=0&limit=50&order=updated`) return json(fixture('chatgpt/conversations.json'));
    for (const id of CG_IDS.slice(0, 2)) {
      if (url === `${CG}/backend-api/conversation/${id}`) return json(fixture(`chatgpt/conversation-${id}.json`));
    }
    let m = url.match(/^https:\/\/chatgpt\.com\/backend-api\/files\/(file-[A-Za-z0-9]+)\/download$/);
    if (m) return json({ status: 'success', download_url: `${CG}/backend-api/estuary/content?id=${m[1]}` });
    m = url.match(/^https:\/\/chatgpt\.com\/backend-api\/files\/download\/(file_[a-z0-9]+)\?conversation_id=([a-f0-9-]+)&inline=false$/);
    if (m) {
      assert.equal(m[2], CG_IDS[1]);
      return json({ status: 'success', download_url: `${CG}/backend-api/estuary/content?id=${m[1]}` });
    }
    m = url.match(/^https:\/\/chatgpt\.com\/backend-api\/estuary\/content\?id=(.+)$/);
    if (m) return image(m[1]);
    return null;
  };
}

test('chatgpt: saves one nonce-named file and returns only "saved"', async () => {
  const { result, files, calls } = await page(script('chatgpt'), chatgptRoute());
  assert.equal(result, 'saved');
  assert.equal(files.length, 1);
  assert.equal(files[0].tag, 'a');
  assert.equal(files[0].name, `tincan-history-${NONCE}.json`);
  const out = JSON.parse(files[0].body);
  assert.equal(out.nonce, NONCE);
  assert.equal(out.source, 'chatgpt');
  assert.deepEqual(out.list, fixture('chatgpt/conversations.json'));
  assert.deepEqual(Object.keys(out.details).sort(), CG_IDS.slice(0, 2));
  assert.deepEqual(out.details[CG_IDS[0]], fixture(`chatgpt/conversation-${CG_IDS[0]}.json`));
  assert.deepEqual(out.missing, [CG_IDS[2]]);
  // Images newest first; the PDF attachment is never fetched.
  assert.deepEqual(Object.keys(out.files).sort(), ['file-Gen3rated456', 'file-Sk3tchAbc123', 'file_00000000abcd1234']);
  for (const [id, f] of Object.entries(out.files)) {
    assert.equal(f.mime, 'image/png');
    assert.deepEqual(new Uint8Array(Buffer.from(f.data, 'base64')), png(id));
  }
  assert.ok(!calls.some((c) => c.url.includes('Br1efDoc789')));
  assert.ok(!files[0].body.includes(TOKEN), 'the access token is never saved');
  assert.ok(calls.every((c) => c.init.method === 'GET'));
});

test('chatgpt: failures return a short code and save nothing', async () => {
  const cases = [
    [{ [`${CG}/api/auth/session`]: () => json({}) }, 'not_logged_in'],
    [{ [`${CG}/api/auth/session`]: () => json({}, 401) }, 'not_logged_in'],
    [{ [`${CG}/api/auth/session`]: () => new Response('<html>Just a moment</html>', { status: 403, headers: { 'content-type': 'text/html' } }) }, 'blocked'],
    [{ [`${CG}/backend-api/conversations?offset=0&limit=50&order=updated`]: () => json({}, 404) }, 'endpoint_changed'],
    [{ [`${CG}/backend-api/conversations?offset=0&limit=50&order=updated`]: () => json({}, 500) }, 'http_500'],
    [{ [`${CG}/backend-api/conversations?offset=0&limit=50&order=updated`]: () => Promise.reject(new TypeError('offline')) }, 'network'],
  ];
  for (const [overrides, code] of cases) {
    const { result, files } = await page(script('chatgpt'), chatgptRoute(overrides));
    assert.equal(result, code);
    assert.equal(files.length, 0, code);
  }
});

test('chatgpt: one conversation by id, 404 is not_found', async () => {
  const id = CG_IDS[2];
  const src = script('chatgpt').replace(/"list":50,"details":3,"id":""/, `"list":0,"details":0,"id":"${id}"`);
  assert.notEqual(src, script('chatgpt'));
  const { result, files, calls } = await page(src, chatgptRoute());
  assert.equal(result, 'not_found');
  assert.equal(files.length, 0);
  assert.ok(!calls.some((c) => c.url.includes('/backend-api/conversations')), 'no list in a conversation read');
});

const CA = 'https://claude.ai';
const ORG = '0rg00000-0000-4000-8000-000000000000';
const CA_IDS = ['c1a0d000-0000-4000-8000-000000000001', 'c1a0d000-0000-4000-8000-000000000002'];

function claudeRoute(overrides = {}) {
  return (url, init) => {
    if (overrides[url]) return overrides[url]();
    assert.equal(init.credentials, 'include');
    if (url === `${CA}/api/organizations`) return json(fixture('claudeai/organizations.json'));
    if (url === `${CA}/api/organizations/${ORG}/chat_conversations?limit=50`) return json(fixture('claudeai/chat_conversations.json'));
    for (const id of CA_IDS) {
      if (url === `${CA}/api/organizations/${ORG}/chat_conversations/${id}?tree=True&rendering_mode=messages&render_all_tools=true`) {
        return json(fixture(`claudeai/conversation-${id}.json`));
      }
    }
    const m = url.match(/^https:\/\/claude\.ai\/api\/0rg00000-0000-4000-8000-000000000000\/files\/([a-f0-9-]+)\/preview$/);
    if (m) return image(m[1]);
    return null;
  };
}

test('claude-ai: saves one nonce-named file and returns only "saved"', async () => {
  const { result, files } = await page(script('claude-ai'), claudeRoute());
  assert.equal(result, 'saved');
  assert.equal(files.length, 1);
  assert.equal(files[0].name, `tincan-history-${NONCE}.json`);
  const out = JSON.parse(files[0].body);
  assert.equal(out.source, 'claude-ai');
  assert.equal(out.nonce, NONCE);
  assert.deepEqual(out.list, fixture('claudeai/chat_conversations.json'));
  assert.deepEqual(Object.keys(out.details).sort(), CA_IDS);
  assert.deepEqual(out.missing, ['c1a0d000-0000-4000-8000-000000000003']);
  assert.deepEqual(Object.keys(out.files).sort(), ['f11e0000-0000-4000-8000-0000000000aa', 'f11e0000-0000-4000-8000-0000000000bb']);
  assert.deepEqual(new Uint8Array(Buffer.from(out.files['f11e0000-0000-4000-8000-0000000000aa'].data, 'base64')), png('f11e0000-0000-4000-8000-0000000000aa'));
});

test('claude-ai: no organization is not_logged_in', async () => {
  const { result, files } = await page(script('claude-ai'), claudeRoute({ [`${CA}/api/organizations`]: () => json([]) }));
  assert.equal(result, 'not_logged_in');
  assert.equal(files.length, 0);
});
