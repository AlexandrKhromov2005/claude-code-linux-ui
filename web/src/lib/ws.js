import { api } from './api.js';
import {
  appState, messages, liveByThread, pendingApproval, wsConnected,
  setLive, appendLiveText, clearLive, liveFor,
} from '../stores/state.js';
import { get } from 'svelte/store';

let ws = null;
let sendFn = null; // exposed so components can call ws.send

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
    }
  };
}

function handleMessage(msg) {
  switch (msg.type) {
    case 'state':
      appState.set(msg.state);
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
      if (viewing(tid)) {
        messages.update(ms => [
          ...ms,
          { role: 'tool', content: msg.tool || '', ts: new Date().toISOString(), _transient: true },
        ]);
      }
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

    case 'result':
      setLive(tid, { tool: '' });
      // Cost/context/model are session-global; update regardless of thread.
      appState.update(s => s ? {
        ...s,
        cost: msg.cost ?? s.cost,
        ctxUsed: msg.ctxUsed ?? s.ctxUsed,
        ctxWindow: msg.ctxWindow ?? s.ctxWindow,
        modelActual: msg.modelActual ?? s.modelActual,
      } : s);
      break;

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
  // Bind this turn's live state to the thread it is sent from, so its output
  // renders only there even if the user switches threads mid-turn.
  setLive(threadId, { streaming: true, text: '', tool: '' });
  // Add user message to the visible list immediately (current thread == threadId).
  messages.update(ms => [
    ...ms,
    { role: 'user', content: text, attachments: attachmentPaths, ts: new Date().toISOString() },
  ]);
  sendFn?.({ type: 'send', text, attachments: attachmentPaths });
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
