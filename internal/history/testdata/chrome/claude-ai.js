(async () => {
  'use strict';
  // Agent Tincan history read, run once by Claude in Chrome in a fresh tab.
  // Fixed code: the only inputs are the validated values in P. It fetches
  // with the page's own session, saves ONE file to the Chrome download
  // folder, and returns only "saved" or a short error code, never any
  // conversation content.
  const P = {"nonce":"0123456789abcdef0123456789abcdef","list":50,"details":3,"id":"","images":8,"max_image_bytes":10485760,"max_total_image_bytes":41943040};
  const ID_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;

  class Fail extends Error {
    constructor(code) {
      super(code);
      this.code = code;
    }
  }

  const out = { nonce: P.nonce, source: '', list: null, details: {}, missing: [], files: {} };

  async function send(url, init) {
    try {
      return await fetch(url, { method: 'GET', cache: 'no-store', credentials: 'include', ...init });
    } catch {
      throw new Fail('network');
    }
  }

  function mimeOf(res) {
    return (res.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
  }

  // check maps an HTTP status to an error code. notFound marks endpoints
  // where 404 means the item is gone rather than the API moved.
  function check(res, notFound) {
    if (res.ok) return;
    if (res.status === 401) throw new Fail('not_logged_in');
    if (res.status === 403) throw new Fail(mimeOf(res) === 'text/html' ? 'blocked' : 'not_logged_in');
    if (res.status === 404 || res.status === 410) throw new Fail(notFound ? 'not_found' : 'endpoint_changed');
    throw new Fail('http_' + res.status);
  }

  async function getJSON(url, init, notFound) {
    const res = await send(url, init);
    check(res, notFound);
    if (!mimeOf(res).includes('json')) throw new Fail('endpoint_changed');
    try {
      return await res.json();
    } catch {
      throw new Fail('endpoint_changed');
    }
  }

  function b64(bytes) {
    let s = '';
    for (let i = 0; i < bytes.length; i += 0x8000) {
      s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(s);
  }

  let imageTotal = 0;

  // getImage fetches one image within the per-image and total caps.
  async function getImage(url, init) {
    const res = await send(url, init);
    check(res, true);
    const declared = Number(res.headers.get('content-length') || 0);
    if (declared > P.max_image_bytes) throw new Fail('too_large');
    const mime = mimeOf(res);
    if (!mime.startsWith('image/') && mime !== 'application/octet-stream') throw new Fail('endpoint_changed');
    const bytes = new Uint8Array(await res.arrayBuffer());
    if (bytes.length === 0 || bytes.length > P.max_image_bytes) throw new Fail('too_large');
    if (imageTotal + bytes.length > P.max_total_image_bytes) throw new Fail('too_large');
    imageTotal += bytes.length;
    return { mime, data: b64(bytes) };
  }

  // details opens each conversation. In a list read a conversation that
  // vanished since the list is recorded as missing; a one-conversation
  // read fails with not_found.
  async function details(ids, open) {
    for (const id of ids) {
      try {
        out.details[id] = await open(id);
      } catch (e) {
        if (e instanceof Fail && e.code === 'not_found' && !P.id) {
          out.missing.push(id);
          continue;
        }
        throw e;
      }
    }
  }

  // images fetches image pointers newest first, [fileID, conversationID]
  // pairs, up to P.images. An image that fails is skipped.
  async function images(pairs, one) {
    const seen = new Set();
    for (const [id, conv] of pairs) {
      if (Object.keys(out.files).length >= P.images) break;
      if (seen.has(id)) continue;
      seen.add(id);
      try {
        out.files[id] = await one(id, conv);
      } catch (e) {
        if (!(e instanceof Fail)) throw e;
      }
    }
  }

  function validIDs(list, key, n) {
    const ids = [];
    for (const it of Array.isArray(list) ? list : []) {
      const id = it && it[key];
      if (typeof id === 'string' && ID_RE.test(id) && !ids.includes(id)) ids.push(id);
    }
    return ids.slice(0, n);
  }

  function save() {
    const blob = new Blob([JSON.stringify(out)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'tincan-history-' + P.nonce + '.json';
    a.style.display = 'none';
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
  }

  // claude.ai: the organization comes from /api/organizations; every
  // request uses the page's own session cookies.
  out.source = 'claude-ai';
  const ORIGIN = 'https://claude.ai';

  async function org() {
    const orgs = await getJSON(ORIGIN + '/api/organizations', {}, false);
    if (!Array.isArray(orgs)) throw new Fail('endpoint_changed');
    if (orgs.length === 0) throw new Fail('not_logged_in');
    const chat = orgs.find((o) => o && Array.isArray(o.capabilities) && o.capabilities.includes('chat')) || orgs[0];
    if (!chat || typeof chat.uuid !== 'string' || !ID_RE.test(chat.uuid)) throw new Fail('endpoint_changed');
    return chat.uuid;
  }

  // imageIDs lists the image file ids on a conversation's current branch,
  // newest message first.
  function imageIDs(c) {
    const msgs = c && Array.isArray(c.chat_messages) ? c.chat_messages.filter((m) => m && typeof m === 'object') : [];
    const byID = new Map();
    for (const m of msgs) {
      if (typeof m.uuid === 'string') byID.set(m.uuid, m);
    }
    let path = [];
    const seen = new Set();
    for (let m = byID.get(c && c.current_leaf_message_uuid); m && !seen.has(m.uuid); m = byID.get(m.parent_message_uuid)) {
      seen.add(m.uuid);
      path.push(m);
    }
    if (path.length === 0) path = msgs.slice().sort((x, y) => (Number(y.index) || 0) - (Number(x.index) || 0));
    const ids = [];
    for (const m of path) {
      const files = [].concat(Array.isArray(m.files) ? m.files : [], Array.isArray(m.files_v2) ? m.files_v2 : []);
      for (const f of files) {
        if (f && f.file_kind === 'image' && typeof f.file_uuid === 'string' && ID_RE.test(f.file_uuid)) ids.push(f.file_uuid);
      }
    }
    return ids;
  }

  async function read() {
    const o = await org();
    let ids = P.id ? [P.id] : [];
    if (P.list > 0) {
      const list = await getJSON(ORIGIN + '/api/organizations/' + o + '/chat_conversations?limit=' + P.list, {}, false);
      out.list = Array.isArray(list) ? list.slice(0, P.list) : list;
      ids = validIDs(out.list, 'uuid', P.details);
    }
    await details(ids, (id) =>
      getJSON(ORIGIN + '/api/organizations/' + o + '/chat_conversations/' + encodeURIComponent(id) + '?tree=True&rendering_mode=messages&render_all_tools=true', {}, true),
    );
    if (P.images > 0) {
      const pairs = [];
      for (const id of ids) {
        for (const f of imageIDs(out.details[id])) pairs.push([f, id]);
      }
      await images(pairs, (id) => getImage(ORIGIN + '/api/' + o + '/files/' + encodeURIComponent(id) + '/preview', {}));
    }
  }

  try {
    await read();
    save();
    return 'saved';
  } catch (e) {
    return e instanceof Fail ? e.code : 'script_error';
  }
})()
