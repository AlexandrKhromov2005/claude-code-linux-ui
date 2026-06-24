<script>
  import { onMount } from 'svelte';
  import './app.css';

  import { api } from './lib/api.js';
  import { connectWS } from './lib/ws.js';
  import { appState, messages, streaming, wsConnected, mode, cost, skipPerms, effort } from './stores/state.js';

  import Sidebar from './lib/Sidebar.svelte';
  import MessageList from './lib/MessageList.svelte';
  import Composer from './lib/Composer.svelte';
  import ApproveModal from './lib/ApproveModal.svelte';
  import Settings from './lib/Settings.svelte';

  let settingsOpen = false;
  let modeWarning = '';

  const effortLevels = ['', 'low', 'medium', 'high', 'xhigh', 'max', 'ultracode'];
  let effortSel = '';
  $: effortSel = $effort || '';

  async function pickEffort() {
    try {
      const res = await api.setEffort(effortSel);
      appState.update(s => s ? { ...s, effort: res.effort } : s);
    } catch {}
  }

  function effortLabel(v) {
    return v === '' ? 'авто' : v;
  }

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

        <!-- Effort level -->
        <select
          class="effort-select"
          bind:value={effortSel}
          on:change={pickEffort}
          title="Уровень reasoning effort (--effort). «авто» — дефолт модели."
        >
          {#each effortLevels as lvl}
            <option value={lvl}>effort: {effortLabel(lvl)}</option>
          {/each}
        </select>

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
    padding: 0 18px;
    height: 50px;
    border-bottom: 1px solid var(--border-soft);
    background: var(--bg2);
    box-shadow: var(--shadow-sm);
    flex-shrink: 0;
    gap: 10px;
    overflow: hidden;
    z-index: 5;
  }

  .topbar-left, .topbar-right {
    display: flex;
    align-items: center;
    gap: 8px;
    overflow: hidden;
  }

  .topbar-left { flex: 1; min-width: 0; }

  .meta-item {
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .project-name { font-weight: 600; letter-spacing: -0.01em; }
  .dim { color: var(--text-dim); }
  .thread-title { max-width: 220px; }

  .sep { color: var(--border); user-select: none; opacity: 0.7; }

  .cost {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-dim);
    background: var(--bg3);
    border: 1px solid var(--border-soft);
    padding: 3px 9px;
    border-radius: 999px;
  }
  .cost.positive { color: var(--accent); border-color: var(--accent-border); background: var(--accent-soft); }

  .effort-select {
    font-size: 12px;
    height: 30px;
    padding: 0 8px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 999px;
    cursor: pointer;
    max-width: 150px;
    transition: border-color 0.15s, color 0.15s, box-shadow 0.15s;
  }
  .effort-select:hover { border-color: var(--text-dim); color: var(--text); }
  .effort-select:focus { outline: none; border-color: var(--accent); box-shadow: var(--ring); }

  .mode-toggle {
    font-size: 12px;
    height: 30px;
    padding: 0 13px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 999px;
    text-transform: lowercase;
    letter-spacing: 0.03em;
    transition: all 0.15s;
  }
  .mode-toggle:hover { border-color: var(--text-dim); color: var(--text); }
  .mode-toggle.agent {
    background: var(--accent-soft);
    color: var(--accent-strong);
    border-color: var(--accent-border);
    font-weight: 600;
  }

  .skip-toggle {
    font-size: 12px;
    height: 30px;
    padding: 0 11px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 999px;
    letter-spacing: 0.02em;
    transition: all 0.15s;
  }
  .skip-toggle:hover { border-color: var(--red); color: var(--text); }
  .skip-toggle.active {
    background: var(--red-soft);
    color: var(--red);
    border-color: rgba(226,96,96,0.5);
    font-weight: 600;
    box-shadow: 0 0 0 3px rgba(226,96,96,0.10);
  }

  .ws-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-mute);
    flex-shrink: 0;
    margin: 0 2px;
    transition: background 0.3s, box-shadow 0.3s;
  }
  .ws-dot.connected {
    background: var(--green);
    box-shadow: 0 0 0 3px var(--green-soft);
  }

  .icon-btn {
    background: none;
    color: var(--text-dim);
    width: 30px;
    height: 30px;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid transparent;
    border-radius: 999px;
  }
  .icon-btn:hover { color: var(--text); background: var(--bg3); border-color: var(--border); }

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
