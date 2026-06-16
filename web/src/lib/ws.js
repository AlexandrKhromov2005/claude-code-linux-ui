import { api } from './api.js';
import {
  appState, messages, streaming, liveText,
  pendingApproval, wsConnected,
} from '../stores/state.js';
import { get } from 'svelte/store';

let ws = null;
let sendFn = null; // exposed so components can call ws.send

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
      // Finalize the live assistant message
      const live = get(liveText);
      if (live) {
        messages.update(ms => [
          ...ms,
          { role: 'assistant', content: live, ts: new Date().toISOString() },
        ]);
        liveText.set('');
      }
      streaming.set(false);
      break;

    case 'approval_request':
      pendingApproval.set(msg);
      break;

    case 'error':
      messages.update(ms => [
        ...ms,
        { role: 'system', content: `Ошибка: ${msg.message}`, ts: new Date().toISOString() },
      ]);
      streaming.set(false);
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
      messages.update(ms => [
        ...ms,
        { role: 'tool', content: msg.tool || '', ts: new Date().toISOString(), _transient: true },
      ]);
      break;

    case 'system_init':
      // Update state with new model/session info via a state refresh; the event
      // itself just carries metadata so we store it as a system line.
      appState.update(s => s ? {
        ...s,
        thread: s.thread ? { ...s.thread, sessionId: msg.sessionId } : s.thread,
      } : s);
      break;

    case 'result':
      appState.update(s => s ? { ...s, cost: msg.cost ?? s.cost } : s);
      break;

    case 'retry':
      messages.update(ms => [
        ...ms,
        { role: 'system', content: `Повтор попытки ${msg.attempt}...`, ts: new Date().toISOString() },
      ]);
      break;

    case 'notice':
      messages.update(ms => [
        ...ms,
        { role: 'system', content: msg.text || '', ts: new Date().toISOString() },
      ]);
      break;

    case 'error':
      messages.update(ms => [
        ...ms,
        { role: 'system', content: `Ошибка: ${msg.error || ''}`, ts: new Date().toISOString(), _error: true },
      ]);
      streaming.set(false);
      break;
  }
}

export function sendMessage(text, attachmentPaths) {
  streaming.set(true);
  liveText.set('');
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
}

export function sendApproval(id, allow, rememberRule) {
  const msg = { type: 'approval', id, allow };
  if (allow && rememberRule) msg.rememberRule = rememberRule;
  sendFn?.(msg);
  pendingApproval.set(null);
}
