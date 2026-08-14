import { api } from './api.js';
import {
  appState, messages, liveByThread, agentsByThread, pendingApproval, wsConnected,
  connection, connFailure, lastTurnUsage, jobs, jobsClock, setLive, appendLiveText,
  clearLive, liveFor, setAgents, clearAgents,
} from '../stores/state.js';
import { get } from 'svelte/store';

let ws = null;

// sendFrame writes to whichever socket is currently live, and reports whether it
// went out. It is a plain function rather than a closure rebuilt on every
// connect: the rebuilt version was null until the first connection and could be
// left behind by a stale reconnect, both of which read as "no connection" while
// one was in fact open.
function sendFrame(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
    return true;
  }
  return false;
}

// The tools that launch a subagent. The CLI renamed this one (Task → Agent), so
// both are recognised rather than whichever version happens to be installed.
const SUBAGENT_TOOLS = new Set(['Agent', 'Task']);

// viewing reports whether threadId is the thread the user is currently looking
// at. Turn output is stamped with its thread id by the server; we only touch the
// visible `messages`/appState for the open thread, while every thread's live
// stream keeps accumulating in liveByThread regardless of what is on screen.
function viewing(threadId) {
  return threadId && get(appState)?.thread?.id === threadId;
}

// classifyFailure asks the REST side why the socket is down. A 401 means the
// server is alive and refusing this page's token — almost always because the
// server was restarted and issued a new one, leaving tabs opened before it
// holding a dead link.
async function classifyFailure() {
  try {
    const res = await fetch('/api/state', { headers: { Authorization: `Bearer ${api.token}` } });
    if (res.status === 401 || res.status === 403) return 'auth';
    return res.ok ? null : 'down';
  } catch {
    return 'down'; // nothing answering at all
  }
}

export function connectWS() {
  if (ws) return;

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  // Every handler below is bound to this exact socket and checks that it is
  // still the current one before touching shared state.
  //
  // Without that, reconnecting corrupts itself. A retry can leave more than one
  // socket alive for a moment, and handlers that read the module-level `ws`
  // act on whichever socket is current rather than their own: a dying socket's
  // error handler closes its replacement, and its close handler sets the
  // reference to null even though the replacement is open and healthy. The
  // socket then keeps delivering frames — the UI carries on updating — while
  // every send fails, because the code believes there is no connection.
  const sock = new WebSocket(`${proto}://${location.host}/ws`, ['ccl-bearer', api.token]);
  ws = sock;
  const current = () => ws === sock;

  sock.addEventListener('open', () => {
    if (!current()) { sock.close(); return; }
    wsConnected.set(true);
    connFailure.set(null);
    // A fresh socket cannot resume a turn that was streaming to a previous
    // connection, so any leftover "streaming" slice is stale. Clear it so the
    // composer never stays locked behind a ghost spinner after a reconnect.
    // Dropping the socket cancels those turns server-side, so their subagents
    // are gone too and must not be left on screen looking alive.
    liveByThread.set({});
    agentsByThread.set({});
  });

  sock.addEventListener('close', () => {
    // A socket that has already been replaced must not report its death as the
    // current connection's, or it clears a live one.
    if (!current()) return;
    wsConnected.set(false);
    ws = null;
    // The WebSocket API never exposes the HTTP status behind a failed upgrade, so
    // a rejected token and an unreachable server arrive here identically. Ask
    // over REST, which does report it: the difference decides both the advice
    // given and whether retrying is worth anything at all.
    classifyFailure().then(kind => {
      connFailure.set(kind);
      // A rejected token will be rejected again for as long as this page lives —
      // it is the one in the address bar. Retrying forever just hides the real
      // problem behind a spinner.
      if (kind !== 'auth') setTimeout(connectWS, 3000);
    });
  });

  sock.addEventListener('error', () => {
    // Close this socket, never "the current one" — they are not always the same.
    sock.close();
  });

  sock.addEventListener('message', (ev) => {
    if (!current()) return; // frames from a socket already replaced
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    handleMessage(msg);
  });
}

function handleMessage(msg) {
  switch (msg.type) {
    case 'state':
      appState.set(msg.state);
      if (msg.state?.connection) connection.set(msg.state.connection);
      break;

    case 'connection':
      // Live VPN/API health push from the server's background probe.
      if (msg.status) connection.set(msg.status);
      break;

    case 'jobs':
      // Full snapshot of supervised background jobs, so it replaces the list.
      jobs.set(msg.jobs || []);
      if (msg.now) jobsClock.set(msg.now);
      break;

    case 'event':
      handleEvent(msg);
      break;

    case 'turn_end': {
      // Finalize the live assistant text into the message list, but only if the
      // user is viewing that thread; otherwise the server has persisted it and
      // it will load when they reopen the thread. Either way, drop the live slice.
      const tid = msg.threadId;
      const live = liveFor(tid).text;
      if (live && viewing(tid)) {
        messages.update(ms => [
          ...ms,
          { role: 'assistant', content: live, ts: new Date().toISOString() },
        ]);
      }
      clearLive(tid);
      break;
    }

    case 'approval_request':
      pendingApproval.set(msg);
      break;

    case 'error':
      // A turn-level error from the server. With a threadId it belongs to one
      // turn; without one (e.g. no open project) it refers to the send that just
      // failed, so clear the current thread's optimistic streaming state.
      handleTurnError(msg.threadId, msg.message);
      break;
  }
}

function handleEvent(msg) {
  const tid = msg.threadId;
  switch (msg.kind) {
    case 'text':
      appendLiveText(tid, msg.text || '');
      break;

    case 'tool_start':
      setLive(tid, { streaming: true, tool: msg.tool || 'tool' });
      // Launching a subagent is not a line in the transcript: the panel below
      // says who was launched and what became of them, in more detail than a
      // bare tool name ever could.
      if (viewing(tid) && !SUBAGENT_TOOLS.has(msg.tool)) {
        messages.update(ms => [
          ...ms,
          { role: 'tool', content: msg.tool || '', ts: new Date().toISOString(), _transient: true },
        ]);
      }
      break;

    case 'agents':
      // A full snapshot of the turn's subagents, so it replaces rather than merges.
      setAgents(tid, msg.agents, msg.now);
      break;

    case 'system_init':
      // Carry the new session id onto the turn's thread, if it is the open one.
      if (viewing(tid)) {
        appState.update(s => s ? {
          ...s,
          thread: s.thread ? { ...s.thread, sessionId: msg.sessionId } : s.thread,
        } : s);
      }
      break;

    case 'result': {
      // A result ends a reply, not necessarily the turn: a subagent reports back
      // after the model has already answered, and the model answers again on the
      // same stream. Each reply is closed off here — as the server does when it
      // persists them — so two replies do not run together into one paragraph.
      const done = liveFor(tid).text || msg.text || '';
      if (done && viewing(tid)) {
        messages.update(ms => [
          ...ms,
          { role: 'assistant', content: done, ts: new Date().toISOString() },
        ]);
      }
      setLive(tid, { text: '', tool: '' });
      // Cost/context/model/usage are session-global; update regardless of thread.
      appState.update(s => s ? {
        ...s,
        cost: msg.cost ?? s.cost,
        ctxUsed: msg.ctxUsed ?? s.ctxUsed,
        ctxWindow: msg.ctxWindow ?? s.ctxWindow,
        modelActual: msg.modelActual ?? s.modelActual,
        usage: msg.usage ?? s.usage,
      } : s);
      if (msg.turnUsage) lastTurnUsage.set(msg.turnUsage);
      break;
    }

    case 'retry':
      if (viewing(tid)) {
        messages.update(ms => [
          ...ms,
          { role: 'system', content: `Повтор попытки ${msg.attempt}...`, ts: new Date().toISOString() },
        ]);
      }
      break;

    case 'notice':
      if (viewing(tid)) {
        messages.update(ms => [
          ...ms,
          { role: 'system', content: msg.text || '', ts: new Date().toISOString() },
        ]);
      }
      break;

    case 'rate_limit':
      appState.update(s => {
        if (!s) return s;
        const limits = (s.limits || []).filter(l => l.type !== msg.limitType);
        limits.push({ type: msg.limitType, resetsAt: msg.limitResets, status: msg.limitStatus });
        limits.sort((a, b) => (a.type === 'five_hour' ? 0 : 1) - (b.type === 'five_hour' ? 0 : 1));
        return { ...s, limits };
      });
      break;

    case 'error':
      handleTurnError(tid, msg.error || '');
      break;
  }
}

function handleTurnError(threadId, text) {
  const tid = threadId || get(appState)?.thread?.id;
  if (viewing(tid) && text) {
    messages.update(ms => [
      ...ms,
      { role: 'system', content: `Ошибка: ${text}`, ts: new Date().toISOString(), _error: true },
    ]);
  }
  clearLive(tid);
}

// sendFailureText explains why a message could not be sent. Telling someone to
// reload is worse than useless when the token is the problem: it lives in this
// page's URL, so a reload sends the same rejected token again.
function sendFailureText(kind) {
  if (kind === 'auth') {
    return 'Сервер отклонил токен этой вкладки — скорее всего он был перезапущен и выдал новый. ' +
      'Обновление страницы не поможет: токен зашит в её адрес. ' +
      'Возьмите свежую ссылку из терминала, где запущен сервер, и откройте её в новой вкладке.';
  }
  // The overwhelmingly common cause is a server that is restarting — a rebuild
  // takes a few seconds — not one that has gone away. Saying "check it is
  // running" sent people looking for a problem that fixes itself, so the text
  // says what is actually true: the text is kept, and the socket comes back.
  return 'Связь с локальным сервером на секунду пропала — сообщение не отправлено. ' +
    'Текст сохранён в поле ввода: переподключение идёт автоматически, ' +
    'через пару секунд просто нажмите Enter ещё раз.';
}

// sendMessage dispatches a turn and reports whether it actually went out, so
// the composer can keep the text when it did not. Anything else throws away
// what someone just typed — and the moment it happens is a reconnect, when they
// are least likely to still have it.
export function sendMessage(text, attachmentPaths) {
  const threadId = get(appState)?.thread?.id;
  if (!threadId) {
    // Silently doing nothing here is how a message disappears with no
    // explanation at all — the case someone hits right after a restart, before
    // a project is open again.
    messages.update(ms => [
      ...ms,
      {
        role: 'system',
        content: 'Нет открытого проекта — сообщение отправлять некуда. ' +
          'Выберите проект в боковой панели, текст сохранён.',
        ts: new Date().toISOString(),
        _error: true,
      },
    ]);
    return false;
  }
  // Transmit first: if the socket is down the send is dropped, and faking a
  // spinner + user message here would lock the composer behind a turn that was
  // never dispatched. Surface the failure instead of swallowing it.
  if (!sendFrame({ type: 'send', text, attachments: attachmentPaths })) {
    messages.update(ms => [
      ...ms,
      { role: 'system', content: sendFailureText(get(connFailure)), ts: new Date().toISOString(), _error: true },
    ]);
    return false;
  }
  // Bind this turn's live state to the thread it is sent from, so its output
  // renders only there even if the user switches threads mid-turn. The previous
  // turn's subagents go with it — they belong to the exchange above.
  setLive(threadId, { streaming: true, text: '', tool: '' });
  clearAgents(threadId);
  // Add user message to the visible list immediately (current thread == threadId).
  messages.update(ms => [
    ...ms,
    { role: 'user', content: text, attachments: attachmentPaths, ts: new Date().toISOString() },
  ]);
  return true;
}

export function cancelTurn() {
  const threadId = get(appState)?.thread?.id;
  sendFrame({ type: 'cancel', threadId });
  clearLive(threadId);
}

export function sendApproval(id, allow, rememberRule) {
  const msg = { type: 'approval', id, allow };
  if (allow && rememberRule) msg.rememberRule = rememberRule;
  sendFrame(msg);
  pendingApproval.set(null);
}
