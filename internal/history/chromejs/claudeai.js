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
