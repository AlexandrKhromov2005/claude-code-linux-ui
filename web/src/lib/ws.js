import { api } from './api.js';
import {
  appState, messages, liveByThread, agentsByThread, pendingApproval, wsConnected,
  connection, lastTurnUsage, jobs, jobsClock, setLive, appendLiveText, clearLive,
  liveFor, setAgents, clearAgents,
} from '../stores/state.js';
import { get } from 'svelte/store';

let ws = null;
let sendFn = null; // exposed so components can call ws.send

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

export function connectWS() {
  if (ws) return;

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws`, ['ccl-bearer', api.token]);

  ws.addEventListener('open', () => {
    wsConnected.set(true);
    // A fresh socket cannot resume a turn that was streaming to a previous
    // connection, so any leftover "streaming" slice is stale. Clear it so the
    // composer never stays locked behind a ghost spinner after a reconnect.
    // Dropping the socket cancels those turns server-side, so their subagents
    // are gone too and must not be left on screen looking alive.
    liveByThread.set({});
    agentsByThread.set({});
  });

  ws.addEventListener('close', () => {
    wsConnected.set(false);
    ws = null;
    // Reconnect after 3 s
    setTimeout(connectWS, 3000);
  });

  ws.addEventListener('error', () => {
    ws?.close();
  });

  ws.addEventListener('message', (ev) => {
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    handleMessage(msg);
  });

  sendFn = (obj) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(obj));
      return true;
    }
    return false;
  };
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

export function sendMessage(text, attachmentPaths) {
  const threadId = get(appState)?.thread?.id;
  if (!threadId) return; // no open thread to attach the turn to
  // Transmit first: if the socket is down the send is dropped, and faking a
  // spinner + user message here would lock the composer behind a turn that was
  // never dispatched. Surface the failure instead of swallowing it.
  if (!sendFn?.({ type: 'send', text, attachments: attachmentPaths })) {
    messages.update(ms => [
      ...ms,
      { role: 'system', content: 'Нет соединения с сервером — сообщение не отправлено. Обновите страницу.', ts: new Date().toISOString() },
    ]);
    return;
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
}

export function cancelTurn() {
  const threadId = get(appState)?.thread?.id;
  sendFn?.({ type: 'cancel', threadId });
  clearLive(threadId);
}

export function sendApproval(id, allow, rememberRule) {
  const msg = { type: 'approval', id, allow };
  if (allow && rememberRule) msg.rememberRule = rememberRule;
  sendFn?.(msg);
  pendingApproval.set(null);
}
