import { writable, derived, get } from 'svelte/store';

// Core application state mirrored from the server
export const appState = writable(null);        // stateDTO
export const messages = writable([]);          // Msg[] for the current thread
export const pendingApproval = writable(null); // approval_request payload | null
export const attachments = writable([]);       // { path, name }[] queued for next send
export const wsConnected = writable(false);

// liveByThread holds the in-flight turn state for each thread id:
//   { [threadId]: { streaming: bool, text: string, tool: string } }
// Turns in different threads run concurrently, so each one keeps its own live
// output here. The composer and message list read the *current* thread's slice
// through the derived stores below, which is why a turn in thread A never locks
// the input or leaks its stream into thread B.
export const liveByThread = writable({});

const EMPTY = { streaming: false, text: '', tool: '' };

// setLive merges a patch into one thread's live slice (no-op without an id).
export function setLive(threadId, patch) {
  if (!threadId) return;
  liveByThread.update(m => ({ ...m, [threadId]: { ...(m[threadId] || EMPTY), ...patch } }));
}

// appendLiveText appends a streaming delta and marks the thread as streaming.
export function appendLiveText(threadId, delta) {
  if (!threadId || !delta) return;
  liveByThread.update(m => {
    const cur = m[threadId] || EMPTY;
    return { ...m, [threadId]: { ...cur, streaming: true, text: cur.text + delta } };
  });
}

// clearLive drops a thread's live slice once its turn has ended.
export function clearLive(threadId) {
  if (!threadId) return;
  liveByThread.update(m => {
    if (!(threadId in m)) return m;
    const next = { ...m };
    delete next[threadId];
    return next;
  });
}

// liveFor returns a thread's current live slice (for non-reactive reads).
export function liveFor(threadId) {
  return get(liveByThread)[threadId] || EMPTY;
}

// Derived helpers
export const currentProject = derived(appState, $s => $s?.project ?? null);
export const currentThread = derived(appState, $s => $s?.thread ?? null);
export const mode = derived(appState, $s => $s?.mode ?? 'chat');
export const skipPerms = derived(appState, $s => $s?.skipPerms ?? false);
export const effort = derived(appState, $s => $s?.effort ?? '');
export const cost = derived(appState, $s => $s?.cost ?? 0);

// Current-thread live state. The composer locks and the message list streams
// based on these, so only the thread whose turn is running is affected.
export const streaming = derived(
  [appState, liveByThread],
  ([$s, $m]) => !!$m[$s?.thread?.id]?.streaming,
);
export const liveText = derived(
  [appState, liveByThread],
  ([$s, $m]) => $m[$s?.thread?.id]?.text || '',
);
export const liveTool = derived(
  [appState, liveByThread],
  ([$s, $m]) => $m[$s?.thread?.id]?.tool || '',
);

// onTurnThread is true when the open thread has an in-flight turn — i.e. the
// live output on screen belongs to the thread being viewed. Equivalent to
// `streaming` now that streaming is itself current-thread-scoped.
export const onTurnThread = streaming;

// Set of thread ids with a turn in flight, for the sidebar "running" marker.
export const streamingThreads = derived(liveByThread, $m => {
  const ids = new Set();
  for (const id in $m) if ($m[id]?.streaming) ids.add(id);
  return ids;
});
