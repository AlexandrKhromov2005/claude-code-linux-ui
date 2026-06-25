import { api } from './api.js';
import {
  appState, messages, streaming, liveText, liveTool, turnThreadId,
  pendingApproval, wsConnected,
} from '../stores/state.js';
import { get } from 'svelte/store';

let ws = null;
let sendFn = null; // exposed so components can call ws.send

// onTurn reports whether the open thread is the one the in-flight turn belongs
// to. Turn-scoped UI updates are skipped otherwise so live output never leaks
// into other threads (the server persists the transcript to the right thread).
function onTurn() {
  const t = get(turnThreadId);
  return t != null && get(appState)?.thread?.id === t;
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

    case 'turn_end':
      // Finalize the live assistant message — but only into the thread the turn
      // belongs to; if the user switched away, the server has already persisted
      // it and it will load when they reopen that thread.
      const live = get(liveText);
      if (live && onTurn()) {
        messages.update(ms => [
          ...ms,
          { role: 'assistant', content: live, ts: new Date().toISOString() },
        ]);
      }
      liveText.set('');
      liveTool.set('');
      streaming.set(false);
      turnThreadId.set(null);
      break;

    case 'approval_request':
      pendingApproval.set(msg);
      break;

    case 'error':
      if (onTurn()) {
        messages.update(ms => [
          ...ms,
          { role: 'system', content: `Ошибка: ${msg.message}`, ts: new Date().toISOString() },
        ]);
      }
      liveText.set('');
      liveTool.set('');
      streaming.set(false);
      turnThreadId.set(null);
      break;
  }
}

function handleEvent(msg) {
  switch (msg.kind) {
    case 'text':
      streaming.set(true);
      liveText.update(t => t + (msg.text || ''));
      break;

    case 'tool_start':
      streaming.set(true);
      liveTool.set(msg.tool || 'tool');
      if (onTurn()) {
        messages.update(ms => [
          ...ms,
          { role: 'tool', content: msg.tool || '', ts: new Date().toISOString(), _transient: true },
        ]);
      }
      break;

    case 'system_init':
      // Carry the new session id onto the turn's thread only.
      if (onTurn()) {
        appState.update(s => s ? {
          ...s,
          thread: s.thread ? { ...s.thread, sessionId: msg.sessionId } : s.thread,
        } : s);
      }
      break;

    case 'result':
      liveTool.set('');
      appState.update(s => s ? {
        ...s,
        cost: msg.cost ?? s.cost,
        ctxUsed: msg.ctxUsed ?? s.ctxUsed,
        ctxWindow: msg.ctxWindow ?? s.ctxWindow,
        modelActual: msg.modelActual ?? s.modelActual,
      } : s);
      break;

    case 'retry':
      if (onTurn()) {
        messages.update(ms => [
          ...ms,
          { role: 'system', content: `Повтор попытки ${msg.attempt}...`, ts: new Date().toISOString() },
        ]);
      }
      break;

    case 'notice':
      if (onTurn()) {
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
      if (onTurn()) {
        messages.update(ms => [
          ...ms,
          { role: 'system', content: `Ошибка: ${msg.error || ''}`, ts: new Date().toISOString(), _error: true },
        ]);
      }
      liveText.set('');
      liveTool.set('');
      streaming.set(false);
      turnThreadId.set(null);
      break;
  }
}

export function sendMessage(text, attachmentPaths) {
  streaming.set(true);
  liveText.set('');
  liveTool.set('');
  // Bind this turn to the thread it was sent from, so its live output renders
  // only there even if the user switches threads mid-turn.
  turnThreadId.set(get(appState)?.thread?.id ?? null);
  // Add user message to local list immediately
  messages.update(ms => [
    ...ms,
    { role: 'user', content: text, attachments: attachmentPaths, ts: new Date().toISOString() },
  ]);
  sendFn?.({ type: 'send', text, attachments: attachmentPaths });
}

export function cancelTurn() {
  sendFn?.({ type: 'cancel' });
  streaming.set(false);
  liveText.set('');
  liveTool.set('');
  turnThreadId.set(null);
}

export function sendApproval(id, allow, rememberRule) {
  const msg = { type: 'approval', id, allow };
  if (allow && rememberRule) msg.rememberRule = rememberRule;
  sendFn?.(msg);
  pendingApproval.set(null);
}
