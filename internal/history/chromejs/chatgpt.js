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
