const $ = (sel) => document.querySelector(sel);

const state = {
  me: null,
  channels: [],
  channelId: null,
  users: [],
  online: new Set(),
  ws: null,
  wsRetry: 0,
  hasMore: {},
  nextBefore: {},
  typingTimers: {},
  lastTypingSent: 0,
  authMode: "login",
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
  if (state.channels.length) await selectChannel(state.channels[0].id);
  $("#composer-input").focus();
}

async function loadChannels() {
  state.channels = await api("/api/channels");
  const list = $("#channel-list");
  list.replaceChildren();
  for (const c of state.channels) {
    const btn = el("button", "channel-item");
    btn.type = "button";
    btn.append(el("span", "hash", "#"), el("span", "", c.name));
    btn.dataset.id = c.id;
    btn.onclick = () => selectChannel(c.id);
    list.appendChild(el("li")).appendChild(btn);
  }
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
  const box = $("#messages");
  state.channelId = id;
  [...document.querySelectorAll(".channel-item")].forEach((b) => {
    b.classList.toggle("active", Number(b.dataset.id) === id);
  });
  const ch = state.channels.find((c) => c.id === id);
  $("#chat-title").textContent = ch ? ch.name : "";
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
    renderMembers();
    const delay = Math.min(10000, 500 * 2 ** state.wsRetry);
    state.wsRetry++;
    setTimeout(openWS, delay);
  };
  ws.onopen = async () => {
    state.wsRetry = 0;
    setConn(true);
    await loadUsers();
    if (state.channelId != null) await selectChannel(state.channelId);
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
        renderMembers();
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
        loadChannels().then(() => {
          if (ev.id === state.channelId) {
            state.channelId = null;
            $("#messages").replaceChildren();
            $("#chat-title").textContent = "";
            if (state.channels.length) selectChannel(state.channels[0].id);
          }
        });
        break;
      case "typing":
        showTyping(ev.username, ev.channel_id);
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
    li.appendChild(el("span", "grow", "# " + c.name));
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
  showView(false);
  setAuthMode("login");
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
      await api("/api/channels", { method: "POST", body: JSON.stringify({ name: input.value.toLowerCase() }) });
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
