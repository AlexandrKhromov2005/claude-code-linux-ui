<script>
  import { onMount } from 'svelte';
  import './app.css';

  import { api } from './lib/api.js';
  import { connectWS } from './lib/ws.js';
  import { appState, messages, streaming, wsConnected, mode, cost, skipPerms } from './stores/state.js';

  import Sidebar from './lib/Sidebar.svelte';
  import MessageList from './lib/MessageList.svelte';
  import Composer from './lib/Composer.svelte';
  import ApproveModal from './lib/ApproveModal.svelte';
  import Settings from './lib/Settings.svelte';

  let settingsOpen = false;
  let modeWarning = '';

  onMount(async () => {
    try {
      const state = await api.getState();
      appState.set(state);
    } catch (err) {
      console.error('Failed to load state:', err);
    }
    connectWS();
  });

  async function toggleMode() {
    const next = $mode === 'chat' ? 'agent' : 'chat';
    try {
      const res = await api.setMode(next);
      appState.update(s => s ? { ...s, mode: res.mode } : s);
      modeWarning = res.warning || '';
    } catch {}
  }

  async function toggleSkip() {
    try {
      const res = await api.setSkipPerms(!$skipPerms);
      appState.update(s => s ? { ...s, skipPerms: res.skipPerms, mode: res.mode ?? s.mode } : s);
      modeWarning = res.warning || '';
    } catch {}
  }

  function dismissWarning() { modeWarning = ''; }

  function formatCost(c) {
    if (!c) return '$0.00';
    return `$${c.toFixed(4)}`;
  }
</script>

<div class="app-shell">
  <Sidebar />

  <main class="main">
    <!-- Header bar -->
    <header class="topbar">
      <div class="topbar-left">
        {#if $appState?.project}
          <span class="meta-item project-name">{$appState.project.name}</span>
          <span class="sep">·</span>
          <span class="meta-item dim">{$appState.project.model || 'default'}</span>
        {:else}
          <span class="meta-item dim">нет проекта</span>
        {/if}
        {#if $appState?.thread}
          <span class="sep">·</span>
          <span class="meta-item dim thread-title">{$appState.thread.title || 'Без названия'}</span>
        {/if}
      </div>

      <div class="topbar-right">
        <span class="meta-item cost" class:positive={$cost > 0}>{formatCost($cost)}</span>

        <!-- Mode toggle -->
        <button
          class="mode-toggle"
          class:agent={$mode === 'agent'}
          on:click={toggleMode}
          title="Переключить режим"
        >
          {$mode === 'agent' ? 'агент' : 'чат'}
        </button>

        <!-- Skip-permissions toggle: one switch for "act without asking" -->
        <button
          class="skip-toggle"
          class:active={$skipPerms}
          on:click={toggleSkip}
          title="Без подтверждений (--dangerously-skip-permissions): агент выполняет правки и команды без запроса. Сохраняется между сессиями. Опасно."
        >
          {$skipPerms ? '⚡ без подтверждений' : '🔒 спрашивать'}
        </button>

        <!-- WS indicator -->
        <span class="ws-dot" class:connected={$wsConnected} title={$wsConnected ? 'подключено' : 'отключено'}></span>

        <!-- Settings -->
        <button class="icon-btn" on:click={() => (settingsOpen = true)} title="Настройки">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="12" cy="12" r="3"/>
            <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/>
          </svg>
        </button>
      </div>
    </header>

    {#if modeWarning}
      <div class="warning-bar">
        <span>{modeWarning}</span>
        <button class="dismiss-btn" on:click={dismissWarning}>x</button>
      </div>
    {/if}

    <MessageList />
    <Composer />
  </main>
</div>

<ApproveModal />
<Settings bind:open={settingsOpen} />

<style>
  .app-shell {
    display: flex;
    height: 100vh;
    overflow: hidden;
  }

  .main {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-width: 0;
  }

  .topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 16px;
    height: 44px;
    border-bottom: 1px solid var(--border);
    background: var(--bg2);
    flex-shrink: 0;
    gap: 8px;
    overflow: hidden;
  }

  .topbar-left, .topbar-right {
    display: flex;
    align-items: center;
    gap: 6px;
    overflow: hidden;
  }

  .topbar-left { flex: 1; min-width: 0; }

  .meta-item {
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .project-name { font-weight: 600; }
  .dim { color: var(--text-dim); }
  .thread-title { max-width: 200px; }

  .sep { color: var(--border); user-select: none; }

  .cost {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-dim);
  }
  .cost.positive { color: var(--accent); }

  .mode-toggle {
    font-size: 12px;
    padding: 3px 10px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 12px;
    text-transform: lowercase;
    letter-spacing: 0.03em;
    transition: all 0.15s;
  }
  .mode-toggle:hover { border-color: var(--text-dim); color: var(--text); }
  .mode-toggle.agent {
    background: rgba(217,119,87,0.15);
    color: var(--accent);
    border-color: rgba(217,119,87,0.4);
  }

  .skip-toggle {
    font-size: 12px;
    padding: 3px 9px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 12px;
    letter-spacing: 0.02em;
    transition: all 0.15s;
  }
  .skip-toggle:hover { border-color: var(--red); color: var(--text); }
  .skip-toggle.active {
    background: rgba(224,92,92,0.18);
    color: var(--red);
    border-color: rgba(224,92,92,0.5);
    font-weight: 600;
  }

  .ws-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--border);
    flex-shrink: 0;
    transition: background 0.3s;
  }
  .ws-dot.connected { background: var(--green); }

  .icon-btn {
    background: none;
    color: var(--text-dim);
    padding: 4px;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid transparent;
    border-radius: var(--radius);
  }
  .icon-btn:hover { color: var(--text); border-color: var(--border); }

  .warning-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 16px;
    background: rgba(217,119,87,0.15);
    border-bottom: 1px solid rgba(217,119,87,0.3);
    font-size: 13px;
    color: var(--accent);
    flex-shrink: 0;
    gap: 8px;
  }

  .dismiss-btn {
    background: none;
    color: var(--accent);
    font-size: 12px;
    padding: 1px 5px;
    border: 1px solid rgba(217,119,87,0.4);
    border-radius: var(--radius);
    flex-shrink: 0;
  }
</style>
