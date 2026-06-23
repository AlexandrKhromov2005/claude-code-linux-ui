import { writable, derived } from 'svelte/store';

// Core application state mirrored from the server
export const appState = writable(null);        // stateDTO
export const messages = writable([]);          // Msg[] for the current thread
export const streaming = writable(false);      // turn in progress
export const liveText = writable('');          // accumulating assistant text delta
export const liveTool = writable('');          // tool the agent is currently running
export const pendingApproval = writable(null); // approval_request payload | null
export const attachments = writable([]);       // { path, name }[] queued for next send
export const wsConnected = writable(false);

// Derived helpers
export const currentProject = derived(appState, $s => $s?.project ?? null);
export const currentThread = derived(appState, $s => $s?.thread ?? null);
export const mode = derived(appState, $s => $s?.mode ?? 'chat');
export const skipPerms = derived(appState, $s => $s?.skipPerms ?? false);
export const cost = derived(appState, $s => $s?.cost ?? 0);
