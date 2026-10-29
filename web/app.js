const $ = (sel) => document.querySelector(sel);

const STUN_SERVERS = [{ urls: ["stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"] }];

const state = {
  me: null,
  channels: [],
  channelId: null,
  users: [],
  online: new Set(),
  voice: [],
  ws: null,
  wsRetry: 0,
  hasMore: {},
  nextBefore: {},
  typingTimers: {},
  lastTypingSent: 0,
  authMode: "login",
};

const voice = {
  live: false,
  channelId: null,
  stream: null,
  muted: false,
  pcs: new Map(),
  audioEls: new Map(),
  pendingIce: new Map(),
  analysers: new Map(),
  ctx: null,
  speakBuf: null,
  speakEls: null,
  lastSpeakCheck: 0,
};

async function api(path, opts = {}) {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...opts });
  let data = null;
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) data = await res.json();
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}

function makeAvatar(name, cls) {
  const a = el("span", cls, name.slice(0, 2).toUpperCase());
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) % 360;
  a.style.background = `hsl(${h}, 55%, 45%)`;
  return a;
}

function fmtTime(ts) {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(new Date(ts * 1000));
}

function fmtDay(ts) {
  return new Intl.DateTimeFormat(undefined, { year: "numeric", month: "long", day: "numeric" }).format(new Date(ts * 1000));
}

function linkify(node, text) {
  for (const part of text.split(/(https?:\/\/[^\s]+)/)) {
    if (!part) continue;
    if (/^https?:\/\//.test(part)) {
      const a = document.createElement("a");
      a.href = part;
      a.textContent = part;
      a.target = "_blank";
      a.rel = "noreferrer noopener";
      node.appendChild(a);
    } else {
      node.appendChild(document.createTextNode(part));
    }
  }
}

function showView(chat) {
  $("#auth-view").hidden = chat;
  $("#chat-view").hidden = !chat;
}

function setAuthError(msg) {
  const p = $("#auth-error");
  if (!msg) {
    p.hidden = true;
    return;
  }
  p.textContent = msg;
  p.hidden = false;
}

async function refreshInviteField() {
  try {
    const s = await api("/api/setup");
    $("#invite-field").hidden = !(state.authMode === "register" && s.needs_invite);
  } catch {
    $("#invite-field").hidden = true;
  }
}

function setAuthMode(mode) {
  state.authMode = mode;
  const login = mode === "login";
  $("#tab-login").classList.toggle("active", login);
  $("#tab-register").classList.toggle("active", !login);
  $("#auth-submit").textContent = login ? "Log in" : "Create account";
  $("#auth-password").setAttribute("minlength", login ? "1" : "8");
  $("#auth-password").setAttribute("autocomplete", login ? "current-password" : "new-password");
  setAuthError(null);
  refreshInviteField();
}

async function submitAuth(e) {
  e.preventDefault();
  const btn = $("#auth-submit");
  btn.disabled = true;
  setAuthError(null);
  const username = $("#auth-username").value.trim();
  const password = $("#auth-password").value;
  const needInvite = !$("#invite-field").hidden;
  const invite = needInvite ? $("#auth-invite").value.trim() : "";
  if (state.authMode === "register" && needInvite && !invite) {
    btn.disabled = false;
    setAuthError("invite code required");
    return;
  }
  try {
    let me;
    if (state.authMode === "register") {
      me = await api("/api/register", { method: "POST", body: JSON.stringify({ username, password, invite }) });
    } else {
      me = await api("/api/login", { method: "POST", body: JSON.stringify({ username, password }) });
    }
    state.me = me;
    startChat();
  } catch (err) {
    setAuthError(err.message);
  } finally {
    btn.disabled = false;
  }
}

function meCardFill() {
  $("#me-name").textContent = state.me.username;
  const av = $("#me-avatar");
  av.textContent = state.me.username.slice(0, 2).toUpperCase();
  let h = 0;
  for (const ch of state.me.username) h = (h * 31 + ch.charCodeAt(0)) % 360;
  av.style.background = `hsl(${h}, 55%, 45%)`;
  $("#admin-btn").hidden = !state.me.is_admin;
}

async function startChat() {
  showView(true);
  meCardFill();
  await loadChannels();
  openWS();
  const firstText = state.channels.find((c) => c.type !== "voice");
  if (firstText) await selectChannel(firstText.id);
  $("#composer-input").focus();
}

async function loadChannels() {
  state.channels = await api("/api/channels");
  renderChannelList();
}

function renderChannelList() {
  const list = $("#channel-list");
  list.replaceChildren();
  const sorted = [...state.channels].sort((a, b) => {
    if (a.type === b.type) return a.id - b.id;
    return a.type === "text" ? -1 : 1;
  });
  for (const c of sorted) {
    const li = el("li");
    const btn = el("button", "channel-item");
    btn.type = "button";
    if (c.type === "voice") {
      btn.appendChild(svgSpeaker());
      btn.append(el("span", "", c.name));
      btn.onclick = () => toggleVoiceChannel(c.id);
      li.appendChild(btn);
      for (const p of state.voice) {
        if (p.channel_id !== c.id) continue;
        const row = el("div", "voice-member");
        const av = makeAvatar(p.username, "avatar-mini");
        av.dataset.speaker = p.user_id;
        if (p.user_id === state.me.id && voice.muted) av.classList.add("self-muted");
        row.appendChild(av);
        row.appendChild(el("span", "name", p.username));
        li.appendChild(row);
      }
    } else {
      btn.append(el("span", "hash", "#"), el("span", "", c.name));
      btn.dataset.id = c.id;
      btn.onclick = () => selectChannel(c.id);
      li.appendChild(btn);
    }
    list.appendChild(li);
  }
}

function svgSpeaker() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", "16");
  svg.setAttribute("height", "16");
  svg.setAttribute("class", "voice-ico");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("fill", "currentColor");
  path.setAttribute("d", "M3 9v6h4l5 5V4L7 9H3Zm13.5 3a4.5 4.5 0 0 0-2.5-4.03v8.05A4.47 4.47 0 0 0 16.5 12ZM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77Z");
  svg.appendChild(path);
  return svg;
}

async function loadUsers() {
  state.users = await api("/api/users");
  renderMembers();
}

function renderMembers() {
  const list = $("#member-list");
  list.replaceChildren();
  const sorted = [...state.users].sort((a, b) => {
    const oa = state.online.has(a.username) ? 0 : 1;
    const ob = state.online.has(b.username) ? 0 : 1;
    return oa - ob || a.username.localeCompare(b.username);
  });
  for (const u of sorted) {
    const online = state.online.has(u.username);
    const li = el("li", "member-item" + (online ? " online" : ""));
    li.appendChild(makeAvatar(u.username, "member-avatar"));
    li.appendChild(el("span", "name", u.username));
    if (u.is_admin) li.appendChild(el("span", "member-badge", "ADMIN"));
    list.appendChild(li);
  }
  $("#online-count").textContent = `${state.online.size} online`;
}

async function selectChannel(id) {
  const ch = state.channels.find((c) => c.id === id);
  if (!ch || ch.type === "voice") return;
  const box = $("#messages");
  state.channelId = id;
  [...document.querySelectorAll(".channel-item")].forEach((b) => {
    b.classList.toggle("active", Number(b.dataset.id) === id);
  });
  $("#chat-title").textContent = ch.name;
  const data = await api(`/api/channels/${id}/messages?limit=50`);
  state.hasMore[id] = data.has_more;
  state.nextBefore[id] = (data.messages[0] && data.messages[0].id) || 0;
  box.replaceChildren();
  const frag = document.createDocumentFragment();
  let prev = null;
  for (const m of data.messages.slice().reverse()) {
    frag.appendChild(messageNode(m, prev));
    prev = m;
  }
  box.appendChild(frag);
  box.scrollTop = box.scrollHeight;
  hideTyping(id);
}

async function loadOlder() {
  const id = state.channelId;
  if (id == null || !state.hasMore[id]) return;
  const data = await api(`/api/channels/${id}/messages?before=${state.nextBefore[id]}&limit=50`);
  state.hasMore[id] = data.has_more;
  if (data.messages.length) state.nextBefore[id] = data.messages[0].id;
  if (!data.messages.length) return;
  const box = $("#messages");
  const oldHeight = box.scrollHeight;
  const oldTop = box.scrollTop;
  const frag = document.createDocumentFragment();
  let prev = null;
  for (const m of data.messages) {
    frag.appendChild(messageNode(m, prev));
    prev = m;
  }
  box.prepend(frag);
  box.scrollTop = box.scrollHeight - oldHeight + oldTop;
}

function messageNode(m, prevMsg) {
  const group = el("div", "msg-group");
  group.dataset.id = m.id;
  group.dataset.userId = m.user_id;
  group.dataset.created = m.created_at;
  const newGroup = !prevMsg || prevMsg.user_id !== m.user_id || m.created_at - prevMsg.created_at > 300;
  if (newGroup) {
    group.classList.add("is-new");
    group.appendChild(makeAvatar(m.username, "msg-avatar"));
    const body = el("div", "msg-body");
    const head = el("div", "msg-head");
    head.appendChild(el("span", "msg-author", m.username));
    if (m.is_admin) head.appendChild(el("span", "msg-admin-badge", "ADMIN"));
    const today = fmtDay(Date.now() / 1000) === fmtDay(m.created_at);
    head.appendChild(el("span", "msg-time", today ? fmtTime(m.created_at) : `${fmtDay(m.created_at)} ${fmtTime(m.created_at)}`));
    body.appendChild(head);
    body.appendChild(contentNode(m));
    group.appendChild(body);
  } else {
    group.appendChild(el("span", "msg-avatar"));
    const body = el("div", "msg-body");
    body.appendChild(contentNode(m));
    group.appendChild(body);
  }
  const tools = el("div", "msg-tools");
  if (state.me && m.user_id === state.me.id) tools.appendChild(toolBtn("Edit", () => startEdit(m, group)));
  if (state.me && (m.user_id === state.me.id || state.me.is_admin)) tools.appendChild(toolBtn("Delete", () => delMessage(m)));
  if (tools.children.length) group.appendChild(tools);
  return group;
}

function toolBtn(label, fn) {
  const b = el("button", "icon-btn");
  b.type = "button";
  b.title = label;
  b.setAttribute("aria-label", label);
  b.appendChild(svgIcon(label));
  b.onclick = fn;
  return b;
}

function svgIcon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", "16");
  svg.setAttribute("height", "16");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("fill", "currentColor");
  if (name === "Edit") {
    path.setAttribute("d", "M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25ZM20.71 7.04a1 1 0 0 0 0-1.41l-2.34-2.34a1 1 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83Z");
  } else {
    path.setAttribute("d", "M6 19a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V7H6v12ZM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4Z");
  }
  svg.appendChild(path);
  return svg;
}

function contentNode(m) {
  const content = el("div", "msg-content");
  linkify(content, m.content);
  if (m.edited_at) content.appendChild(el("span", "msg-edited", "(edited)"));
  return content;
}

function groupById(id) {
  for (const g of document.querySelectorAll("#messages .msg-group")) {
    if (Number(g.dataset.id) === id) return g;
  }
  return null;
}

function lastGroupInfo() {
  const groups = document.querySelectorAll("#messages .msg-group");
  const g = groups[groups.length - 1];
  if (!g) return null;
  return { user_id: Number(g.dataset.userId), created_at: Number(g.dataset.created) };
}

function onMessageNew(m) {
  if (m.channel_id === state.channelId) {
    if (groupById(m.id)) return;
    const box = $("#messages");
    const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
    box.appendChild(messageNode(m, lastGroupInfo()));
    if (nearBottom || m.user_id === state.me.id) box.scrollTop = box.scrollHeight;
    else $("#jump-btn").hidden = false;
  }
  if (state.typingTimers[m.channel_id] && state.lastTypingUser() === m.username) hideTyping(m.channel_id);
}

state.lastTypingUser = function () {
  const bar = $("#typing-bar");
  return bar.hidden ? null : bar.textContent.replace(" is typing...", "");
};

function onMessageEdit(m) {
  if (m.channel_id !== state.channelId) return;
  const g = groupById(m.id);
  if (!g) return;
  const content = g.querySelector(".msg-content");
  if (!content) return;
  content.replaceChildren();
  linkify(content, m.content);
  content.appendChild(el("span", "msg-edited", "(edited)"));
}

function onMessageDelete(id, channelId) {
  if (channelId !== state.channelId) return;
  const g = groupById(id);
  if (g) g.remove();
}

async function delMessage(m) {
  if (!confirm("Delete this message?")) return;
  try {
    await api(`/api/messages/${m.id}`, { method: "DELETE" });
  } catch (err) {
    alert(err.message);
  }
}

function startEdit(m, group) {
  const body = group.querySelector(".msg-body");
  const content = body.querySelector(".msg-content");
  if (!content) return;
  const ta = el("textarea", "msg-input-edit");
  ta.value = m.content;
  ta.rows = Math.min(6, m.content.split("\n").length + 1);
  const cancel = () => {
    ta.replaceWith(content);
    ta.removeEventListener("keydown", onKey);
  };
  const save = async () => {
    const val = ta.value.trim();
    if (!val || val === m.content) {
      cancel();
      return;
    }
    try {
      await api(`/api/messages/${m.id}`, { method: "PATCH", body: JSON.stringify({ content: val }) });
      cancel();
    } catch (err) {
      alert(err.message);
    }
  };
  const onKey = (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      save();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  };
  ta.addEventListener("keydown", onKey);
  content.replaceWith(ta);
  ta.focus();
  ta.setSelectionRange(ta.value.length, ta.value.length);
}

function showTyping(username, channelId) {
  if (channelId !== state.channelId || username === state.me.username) return;
  clearTimeout(state.typingTimers[channelId]);
  const bar = $("#typing-bar");
  bar.textContent = `${username} is typing...`;
  bar.hidden = false;
  state.typingTimers[channelId] = setTimeout(hideTyping, 3200, channelId);
}

function hideTyping(channelId) {
  clearTimeout(state.typingTimers[channelId]);
  $("#typing-bar").hidden = true;
}

function setConn(on) {
  const dot = $("#conn-dot");
  dot.classList.toggle("conn-dot--on", on);
  dot.classList.toggle("conn-dot--off", !on);
  dot.title = on ? "Connected" : "Reconnecting...";
}

function openWS() {
  if (state.ws) {
    state.ws.onclose = null;
    state.ws.close();
    state.ws = null;
  }
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/api/ws`);
  state.ws = ws;
  setConn(true);
  ws.onclose = () => {
    setConn(false);
    state.online.clear();
    state.voice = [];
    renderMembers();
    renderChannelList();
    const delay = Math.min(10000, 500 * 2 ** state.wsRetry);
    state.wsRetry++;
    setTimeout(openWS, delay);
  };
  ws.onopen = async () => {
    state.wsRetry = 0;
    setConn(true);
    await loadUsers();
    if (state.channelId != null) await selectChannel(state.channelId);
    if (voice.live && voice.channelId != null) {
      ws.send(JSON.stringify({ t: "voice:join", channel_id: voice.channelId }));
    }
  };
  ws.onmessage = (e) => {
    let ev;
    try {
      ev = JSON.parse(e.data);
    } catch {
      return;
    }
    switch (ev.t) {
      case "presence":
        state.online = new Set(ev.online);
        state.voice = ev.voice || [];
        renderMembers();
        renderChannelList();
        syncVoicePeers();
        break;
      case "message:new":
        onMessageNew(ev.message);
        break;
      case "message:edit":
        onMessageEdit(ev.message);
        break;
      case "message:delete":
        onMessageDelete(ev.id, ev.channel_id);
        break;
      case "channel:new":
        loadChannels();
        break;
      case "channel:delete":
        if (ev.id === voice.channelId && voice.live) leaveVoice();
        loadChannels().then(() => {
          if (ev.id === state.channelId) {
            state.channelId = null;
            $("#messages").replaceChildren();
            $("#chat-title").textContent = "";
            const firstText = state.channels.find((c) => c.type !== "voice");
            if (firstText) selectChannel(firstText.id);
          }
        });
        break;
      case "typing":
        showTyping(ev.username, ev.channel_id);
        break;
      case "voice:signal":
        onVoiceSignal(ev.from, ev.data);
        break;
    }
  };
}

function sendTyping() {
  const now = Date.now();
  if (now - state.lastTypingSent < 2500) return;
  state.lastTypingSent = now;
  if (state.ws && state.ws.readyState === 1 && state.channelId != null) {
    state.ws.send(JSON.stringify({ t: "typing", channel_id: state.channelId }));
  }
}

function autosize() {
  const ta = $("#composer-input");
  ta.style.height = "auto";
  ta.style.height = Math.min(180, ta.scrollHeight) + "px";
}

async function submitMessage(e) {
  e.preventDefault();
  const ta = $("#composer-input");
  const content = ta.value.trim();
  if (!content) return;
  if (state.channelId == null) {
    alert("Create a channel first");
    return;
  }
  if ([...content].length > 4000) {
    alert("Message too long");
    return;
  }
  try {
    const m = await api(`/api/channels/${state.channelId}/messages`, { method: "POST", body: JSON.stringify({ content }) });
    onMessageNew(m);
    ta.value = "";
    autosize();
    $("#messages").scrollTop = $("#messages").scrollHeight;
  } catch (err) {
    alert(err.message);
  }
}

function openAdmin() {
  if (!state.me.is_admin) return;
  $("#admin-modal").hidden = false;
  renderAdmin();
}

function closeAdmin() {
  $("#admin-modal").hidden = true;
  $("#invite-result").hidden = true;
}

async function renderAdmin() {
  const chans = $("#admin-channels");
  chans.replaceChildren();
  for (const c of state.channels) {
    const li = el("li");
    li.appendChild(el("span", "grow", (c.type === "voice" ? "" : "# ") + c.name + (c.type === "voice" ? " (voice)" : "")));
    const del = el("button", "btn danger", "Delete");
    del.type = "button";
    del.onclick = async () => {
      if (!confirm(`Delete channel #${c.name} and all its messages?`)) return;
      try {
        await api(`/api/channels/${c.id}`, { method: "DELETE" });
        await loadChannels();
        renderAdmin();
      } catch (err) {
        alert(err.message);
      }
    };
    li.appendChild(del);
    chans.appendChild(li);
  }

  const invites = $("#admin-invites");
  invites.replaceChildren();
  try {
    for (const it of await api("/api/invites")) {
      const li = el("li");
      const left = el("span", "invite-pill grow");
      left.textContent = it.code;
      const usage = el("span", "invite-pill");
      usage.textContent = it.max_uses === 0 ? "unlimited" : `${it.uses}/${it.max_uses}`;
      if (it.max_uses !== 0 && it.uses >= it.max_uses) usage.classList.add("used");
      const del = el("button", "btn danger", "Revoke");
      del.type = "button";
      del.onclick = async () => {
        try {
          await api(`/api/invites/${it.code}`, { method: "DELETE" });
          renderAdmin();
        } catch (err) {
          alert(err.message);
        }
      };
      li.append(left, usage, del);
      invites.appendChild(li);
    }
  } catch (err) {
    alert(err.message);
  }

  const members = $("#admin-members");
  members.replaceChildren();
  for (const u of state.users) {
    const li = el("li");
    li.appendChild(makeAvatar(u.username, "member-avatar"));
    li.appendChild(el("span", "grow", u.username));
    li.appendChild(el("span", "invite-pill", u.is_admin ? "admin" : `id ${u.id}`));
    members.appendChild(li);
  }
}

async function logout() {
  try {
    if (voice.live) await leaveVoice();
    await api("/api/logout", { method: "POST" });
  } catch {}
  state.me = null;
  if (state.ws) {
    state.ws.onclose = null;
    state.ws.close();
    state.ws = null;
  }
  closeAdmin();
  $("#channel-list").replaceChildren();
  $("#messages").replaceChildren();
  $("#member-list").replaceChildren();
  $("#chat-title").textContent = "";
  state.channelId = null;
  state.online.clear();
  state.voice = [];
  showView(false);
  setAuthMode("login");
}

async function toggleVoiceChannel(cid) {
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    alert("Voice needs a secure context: open Roost over HTTPS, or on localhost. On plain LAN HTTP, browsers block the microphone.\n\nEasiest fix: run Tailscale on the NAS and use the https://...ts.net address, or flip your browser's 'treat insecure origin as secure' flag for this address.");
    return;
  }
  if (voice.live) {
    if (voice.channelId === cid) {
      leaveVoice();
      return;
    }
    await leaveVoiceInner(false);
  }
  voice.live = true;
  voice.channelId = cid;
  try {
    voice.stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (err) {
    voice.live = false;
    voice.channelId = null;
    alert("Microphone unavailable: " + err.name);
    renderChannelList();
    return;
  }
  const ch = state.channels.find((c) => c.id === cid);
  $("#voice-bar-name").textContent = ch ? ch.name : "";
  const selfAv = $("#voice-bar-avatar");
  selfAv.textContent = state.me.username.slice(0, 2).toUpperCase();
  selfAv.dataset.speaker = state.me.id;
  selfAv.style.background = hueFor(state.me.username);
  $("#voice-bar").hidden = false;
  syncVoicePeers();
  if (state.ws && state.ws.readyState === 1) {
    state.ws.send(JSON.stringify({ t: "voice:join", channel_id: cid }));
  }
  startSpeakLoop();
  renderChannelList();
}

function hueFor(name) {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return `hsl(${h}, 55%, 45%)`;
}

async function leaveVoice() {
  await leaveVoiceInner(true);
}

async function leaveVoiceInner(render) {
  if (!voice.live) return;
  if (state.ws && state.ws.readyState === 1) {
    state.ws.send(JSON.stringify({ t: "voice:leave" }));
  }
  voice.live = false;
  for (const id of [...voice.pcs.keys()]) dropPeer(id);
  if (voice.stream) {
    for (const t of voice.stream.getTracks()) t.stop();
    voice.stream = null;
  }
  for (const [, node] of voice.audioEls) {
    try {
      node.pause();
      node.srcObject = null;
      node.remove();
    } catch {}
  }
  voice.audioEls.clear();
  voice.pendingIce.clear();
  voice.analysers.clear();
  if (voice.ctx && voice.ctx.state === "running") {
    try {
      await voice.ctx.suspend();
    } catch {}
  }
  voice.channelId = null;
  $("#voice-bar").hidden = true;
  if (render) renderChannelList();
}

dropPeer = (id) => {
  const pc = voice.pcs.get(id);
  if (pc) {
    try {
      pc.close();
    } catch {}
    voice.pcs.delete(id);
  }
  const node = voice.audioEls.get(id);
  if (node) {
    try {
      node.pause();
      node.srcObject = null;
      node.remove();
    } catch {}
    voice.audioEls.delete(id);
  }
  voice.analysers.delete(id);
};

function syncVoicePeers() {
  if (!voice.live || voice.channelId == null) return;
  const peers = state.voice.filter((v) => v.channel_id === voice.channelId && v.user_id !== state.me.id);
  for (const p of peers) {
    if (voice.pcs.has(p.user_id)) continue;
    if (state.me.id < p.user_id) startOffer(p.user_id);
  }
  for (const id of [...voice.pcs.keys()]) {
    if (!peers.some((p) => p.user_id === id)) dropPeer(id);
  }
}

function startOffer(peerId) {
  const pc = createPC(peerId);
  pc.createOffer()
    .then((offer) => pc.setLocalDescription(offer))
    .then(() => {
      if (state.ws && state.ws.readyState === 1) {
        state.ws.send(JSON.stringify({ t: "voice:signal", to: peerId, data: { type: "offer", sdp: pc.localDescription.sdp } }));
      }
    })
    .catch(() => dropPeer(peerId));
}

function createPC(peerId) {
  dropPeer(peerId);
  const pc = new RTCPeerConnection({ iceServers: STUN_SERVERS });
  voice.pcs.set(peerId, pc);
  if (voice.stream) {
    for (const t of voice.stream.getAudioTracks()) pc.addTrack(t, voice.stream);
  }
  pc.ontrack = (e) => attachRemote(peerId, e.streams[0]);
  pc.onicecandidate = (e) => {
    if (e.candidate && state.ws && state.ws.readyState === 1) {
      state.ws.send(JSON.stringify({ t: "voice:signal", to: peerId, data: { type: "ice", candidate: e.candidate } }));
    }
  };
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === "failed") {
      dropPeer(peerId);
      if (voice.live && state.me.id < peerId) setTimeout(() => {
        if (voice.live && !voice.pcs.has(peerId) && state.voice.some((v) => v.channel_id === voice.channelId && v.user_id === peerId)) startOffer(peerId);
      }, 1500);
    }
  };
  return pc;
}

function onVoiceSignal(from, data) {
  if (!voice.live || !data || !data.type) return;
  if (data.type === "offer") {
    if (voice.pcs.has(from)) {
      const pc = voice.pcs.get(from);
      if (pc.signalingState !== "stable") return;
    }
    const pc = createPC(from);
    pc.setRemoteDescription({ type: "offer", sdp: data.sdp })
      .then(() => {
        flushIce(from, pc);
        return pc.createAnswer();
      })
      .then((answer) => pc.setLocalDescription(answer))
      .then(() => {
        if (state.ws && state.ws.readyState === 1) {
          state.ws.send(JSON.stringify({ t: "voice:signal", to: from, data: { type: "answer", sdp: pc.localDescription.sdp } }));
        }
      })
      .catch(() => dropPeer(from));
  } else if (data.type === "answer") {
    const pc = voice.pcs.get(from);
    if (!pc || pc.signalingState !== "have-local-offer") return;
    pc.setRemoteDescription({ type: "answer", sdp: data.sdp })
      .then(() => flushIce(from, pc))
      .catch(() => dropPeer(from));
  } else if (data.type === "ice" && data.candidate) {
    const pc = voice.pcs.get(from);
    if (!pc) {
      const q = voice.pendingIce.get(from) || [];
      q.push(data.candidate);
      voice.pendingIce.set(from, q);
      return;
    }
    if (!pc.remoteDescription) {
      const q = voice.pendingIce.get(from) || [];
      q.push(data.candidate);
      voice.pendingIce.set(from, q);
      return;
    }
    pc.addIceCandidate(data.candidate).catch(() => {});
  }
}

function flushIce(from, pc) {
  const q = voice.pendingIce.get(from) || [];
  voice.pendingIce.delete(from);
  for (const c of q) pc.addIceCandidate(c).catch(() => {});
}

function attachRemote(peerId, stream) {
  let node = voice.audioEls.get(peerId);
  if (!node) {
    node = document.createElement("audio");
    node.autoplay = true;
    node.playsInline = true;
    document.body.appendChild(node);
    voice.audioEls.set(peerId, node);
  }
  node.srcObject = stream;
  const p = node.play();
  if (p) p.catch(() => {});
  if (voice.ctx && voice.ctx.state === "suspended") voice.ctx.resume().catch(() => {});
  const src = voice.ctx.createMediaStreamSource(stream);
  const an = voice.ctx.createAnalyser();
  an.fftSize = 512;
  src.connect(an);
  voice.analysers.set(peerId, an);
}

function startSpeakLoop() {
  if (!voice.ctx) voice.ctx = new (window.AudioContext || window.webkitAudioContext)();
  voice.ctx.resume().catch(() => {});
  const src = voice.ctx.createMediaStreamSource(voice.stream);
  const an = voice.ctx.createAnalyser();
  an.fftSize = 512;
  src.connect(an);
  voice.analysers.set(state.me.id, an);
  speakTick();
}

function speakTick() {
  if (!voice.live) return;
  const now = performance.now();
  if (now - voice.lastSpeakCheck > 100) {
    voice.lastSpeakCheck = now;
    if (!voice.speakBuf) voice.speakBuf = new Float32Array(512);
    for (const [id, analyser] of voice.analysers) {
      if (!analyser) continue;
      analyser.getFloatTimeDomainData(voice.speakBuf);
      let sum = 0;
      for (let i = 0; i < voice.speakBuf.length; i++) sum += voice.speakBuf[i] * voice.speakBuf[i];
      const rms = Math.sqrt(sum / voice.speakBuf.length);
      setSpeakerActive(id, rms > 0.045);
    }
  }
  requestAnimationFrame(speakTick);
}

function setSpeakerActive(id, active) {
  for (const node of document.querySelectorAll(`[data-speaker="${id}"]`)) {
    node.classList.toggle("speaking", active);
  }
}

function toggleMute() {
  if (!voice.live || !voice.stream) return;
  voice.muted = !voice.muted;
  for (const t of voice.stream.getAudioTracks()) t.enabled = !voice.muted;
  $("#voice-mute").classList.toggle("muted-btn", voice.muted);
  $("#voice-mute").title = voice.muted ? "Unmute" : "Mute";
  $("#voice-mute").setAttribute("aria-label", voice.muted ? "Unmute microphone" : "Mute microphone");
  renderChannelList();
}

function bindEvents() {
  $("#tab-login").onclick = () => setAuthMode("login");
  $("#tab-register").onclick = () => setAuthMode("register");
  $("#auth-form").onsubmit = submitAuth;
  $("#logout-btn").onclick = logout;
  $("#admin-btn").onclick = openAdmin;
  $("#admin-close").onclick = closeAdmin;
  $("#admin-modal").addEventListener("mousedown", (e) => {
    if (e.target === $("#admin-modal")) closeAdmin();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("#admin-modal").hidden) closeAdmin();
  });
  $("#admin-channel-form").onsubmit = async (e) => {
    e.preventDefault();
    const input = $("#admin-channel-name");
    try {
      await api("/api/channels", {
        method: "POST",
        body: JSON.stringify({ name: input.value.toLowerCase(), type: $("#admin-channel-type").value }),
      });
      input.value = "";
      await loadChannels();
      renderAdmin();
    } catch (err) {
      alert(err.message);
    }
  };
  $("#admin-invite-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      const inv = await api("/api/invites", { method: "POST", body: JSON.stringify({ max_uses: Number($("#admin-invite-uses").value) }) });
      $("#invite-result").hidden = false;
      $("#invite-result-code").textContent = inv.code;
      renderAdmin();
    } catch (err) {
      alert(err.message);
    }
  };
  $("#invite-copy").onclick = () => {
    navigator.clipboard.writeText($("#invite-result-code").textContent);
    $("#invite-copy").textContent = "Copied";
    setTimeout(() => ($("#invite-copy").textContent = "Copy"), 1500);
  };
  $("#composer").onsubmit = submitMessage;
  $("#composer-input").oninput = () => {
    autosize();
    sendTyping();
  };
  $("#composer-input").onkeydown = (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submitMessage(e);
    }
  };
  const box = $("#messages");
  box.onscroll = async () => {
    $("#jump-btn").hidden = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
    if (box.scrollTop < 60) await loadOlder();
  };
  $("#jump-btn").onclick = () => {
    box.scrollTop = box.scrollHeight;
    $("#jump-btn").hidden = true;
  };
  $("#voice-mute").onclick = toggleMute;
  $("#voice-leave").onclick = leaveVoice;
}

async function init() {
  bindEvents();
  setAuthMode("login");
  try {
    state.me = await api("/api/me");
    startChat();
  } catch {
    showView(false);
  }
}

init();
