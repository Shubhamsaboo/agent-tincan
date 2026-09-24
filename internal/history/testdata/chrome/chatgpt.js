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

  // chatgpt.com: the access token from /api/auth/session stays in this
  // scope, used only for the Authorization header, and is never saved.
  out.source = 'chatgpt';
  const ORIGIN = 'https://chatgpt.com';

  async function auth() {
    const s = await getJSON(ORIGIN + '/api/auth/session', {}, false);
    if (!s || typeof s.accessToken !== 'string' || s.accessToken === '') throw new Fail('not_logged_in');
    return { headers: { Authorization: 'Bearer ' + s.accessToken } };
  }

  function pointerID(p) {
    for (const scheme of ['file-service://', 'sediment://']) {
      if (typeof p === 'string' && p.startsWith(scheme) && ID_RE.test(p.slice(scheme.length))) return p.slice(scheme.length);
    }
    return '';
  }

  // imageIDs lists the image file ids on a conversation's current branch,
  // newest message first.
  function imageIDs(d) {
    const ids = [];
    const seen = new Set();
    const mapping = d && d.mapping && typeof d.mapping === 'object' ? d.mapping : {};
    let node = d && d.current_node;
    while (typeof node === 'string' && node !== '' && !seen.has(node) && Object.hasOwn(mapping, node)) {
      seen.add(node);
      const n = mapping[node] || {};
      const m = n.message;
      if (m && !(m.metadata && m.metadata.is_visually_hidden_from_conversation)) {
        const parts = m.content && Array.isArray(m.content.parts) ? m.content.parts : [];
        for (const part of parts) {
          if (part && typeof part === 'object' && part.content_type === 'image_asset_pointer') {
            const id = pointerID(part.asset_pointer);
            if (id) ids.push(id);
          }
        }
        const atts = m.metadata && Array.isArray(m.metadata.attachments) ? m.metadata.attachments : [];
        for (const a of atts) {
          if (a && typeof a.mime_type === 'string' && a.mime_type.startsWith('image/') && typeof a.id === 'string' && ID_RE.test(a.id)) ids.push(a.id);
        }
      }
      node = n.parent;
    }
    return ids;
  }

  // download resolves a download_url: chatgpt.com with the session, signed
  // file URLs without credentials, anything else refused.
  function download(raw) {
    let u;
    try {
      u = new URL(raw, ORIGIN);
    } catch {
      return null;
    }
    if (u.protocol !== 'https:' || u.username || u.password || u.port) return null;
    if (u.hostname === 'chatgpt.com') return { url: u.href, session: true };
    if (u.hostname.endsWith('.oaiusercontent.com')) return { url: u.href, session: false };
    return null;
  }

  async function read() {
    const a = await auth();
    let ids = P.id ? [P.id] : [];
    if (P.list > 0) {
      out.list = await getJSON(ORIGIN + '/backend-api/conversations?offset=0&limit=' + P.list + '&order=updated', a, false);
      ids = validIDs(out.list && out.list.items, 'id', P.details);
    }
    await details(ids, (id) => getJSON(ORIGIN + '/backend-api/conversation/' + encodeURIComponent(id), a, true));
    if (P.images > 0) {
      const pairs = [];
      for (const id of ids) {
        for (const f of imageIDs(out.details[id])) pairs.push([f, id]);
      }
      await images(pairs, async (id, conv) => {
        let metaURL;
        if (id.startsWith('file-')) {
          metaURL = ORIGIN + '/backend-api/files/' + encodeURIComponent(id) + '/download';
        } else {
          const q = new URLSearchParams();
          q.set('conversation_id', conv);
          q.set('inline', 'false');
          metaURL = ORIGIN + '/backend-api/files/download/' + encodeURIComponent(id) + '?' + q;
        }
        const meta = await getJSON(metaURL, a, true);
        if (!meta || meta.status !== 'success' || typeof meta.download_url !== 'string') throw new Fail('endpoint_changed');
        const dl = download(meta.download_url);
        if (!dl) throw new Fail('endpoint_changed');
        return getImage(dl.url, dl.session ? a : { credentials: 'omit' });
      });
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
