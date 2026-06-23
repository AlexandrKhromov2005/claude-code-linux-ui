<script>
  import { onMount } from 'svelte';
  import { appState, messages } from '../stores/state.js';
  import { api } from './api.js';

  let projects = [];
  let threads = [];
  let searchQuery = '';
  let searchResults = [];
  let searchDebounce;
  let projectsOpen = false;
  let newCwd = '';
  let connectError = '';
  let connecting = false;

  onMount(async () => {
    await loadProjects();
    await loadThreads();
  });

  appState.subscribe(async (s) => {
    if (s?.project) {
      await loadThreads();
    }
  });

  async function loadProjects() {
    try { projects = await api.getProjects(); } catch {}
  }

  async function loadThreads() {
    try { threads = await api.getThreads(); } catch {}
  }

  async function openProject(slug) {
    try {
      const state = await api.openProject(slug);
      appState.set(state);
      projectsOpen = false;
      await loadThreads();
    } catch (err) {
      console.error(err);
    }
  }

  async function connectDir() {
    const cwd = newCwd.trim();
    if (!cwd || connecting) return;
    connecting = true;
    connectError = '';
    try {
      const state = await api.useProject(cwd, 'agent');
      appState.set(state);
      newCwd = '';
      projectsOpen = false;
      await loadProjects();
      await loadThreads();
    } catch (err) {
      connectError = String(err?.message || err);
    } finally {
      connecting = false;
    }
  }

  async function openThread(id) {
    try {
      const thread = await api.openThread(id);
      messages.set(thread.messages || []);
      appState.update(s => s ? {
        ...s,
        thread: { id: thread.id, title: thread.title, updated: thread.updated, count: thread.messages?.length || 0, sessionId: thread.claude_session_id },
      } : s);
      searchQuery = '';
      searchResults = [];
    } catch (err) {
      console.error(err);
    }
  }

  async function newThread() {
    try {
      const state = await api.newThread();
      appState.set(state);
      messages.set([]);
      await loadThreads();
    } catch (err) {
      console.error(err);
    }
  }

  async function deleteThread(id, e) {
    e.stopPropagation();
    if (!confirm('Удалить тред?')) return;
    try {
      await api.deleteThread(id);
      await loadThreads();
      appState.update(s => {
        if (s?.thread?.id === id) return { ...s, thread: null };
        return s;
      });
    } catch (err) {
      console.error(err);
    }
  }

  function onSearchInput() {
    clearTimeout(searchDebounce);
    if (!searchQuery.trim()) { searchResults = []; return; }
    searchDebounce = setTimeout(doSearch, 280);
  }

  async function doSearch() {
    try {
      searchResults = await api.search(searchQuery);
    } catch { searchResults = []; }
  }

  function formatDate(ts) {
    try {
      const d = new Date(ts);
      const now = new Date();
      if (d.toDateString() === now.toDateString()) {
        return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
      }
      return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'short' });
    } catch { return ''; }
  }
</script>

<aside class="sidebar">
  <!-- Project header -->
  <div class="sidebar-header">
    <button class="project-btn" on:click={() => (projectsOpen = !projectsOpen)}>
      <span class="project-name">{$appState?.project?.name || 'Нет проекта'}</span>
      <svg class="chevron" class:open={projectsOpen} width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
        <polyline points="6 9 12 15 18 9"/>
      </svg>
    </button>

    {#if projectsOpen}
      <div class="project-dropdown">
        {#each projects as p}
          <button
            class="project-item"
            class:active={$appState?.project?.slug === p.slug}
            on:click={() => openProject(p.slug)}
          >
            <span class="p-name">{p.name}</span>
            <span class="p-cwd">{p.cwd}</span>
          </button>
        {/each}
        {#if projects.length === 0}
          <span class="empty-hint">Проектов нет</span>
        {/if}

        <div class="connect-dir">
          <span class="connect-label">Подключить директорию</span>
          <div class="connect-row">
            <input
              type="text"
              placeholder="/путь/к/проекту или ~/dir"
              bind:value={newCwd}
              on:keydown={(e) => e.key === 'Enter' && connectDir()}
            />
            <button class="connect-btn" on:click={connectDir} disabled={connecting || !newCwd.trim()}>
              {connecting ? '…' : 'OK'}
            </button>
          </div>
          {#if connectError}
            <span class="connect-error">{connectError}</span>
          {:else}
            <span class="connect-note">откроется в режиме агента (правки — через подтверждение)</span>
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <!-- Search -->
  <div class="search-box">
    <input
      type="text"
      placeholder="Поиск по тредам..."
      bind:value={searchQuery}
      on:input={onSearchInput}
    />
  </div>

  {#if searchResults.length > 0}
    <div class="search-results">
      {#each searchResults as r}
        <button class="search-result-item" on:click={() => openThread(r.ThreadID)}>
          <span class="sr-title">{r.Title}</span>
          <span class="sr-snippet">{r.Snippet}</span>
        </button>
      {/each}
    </div>
  {:else}
    <!-- Thread list -->
    <div class="thread-list-header">
      <span class="section-label">Треды</span>
      <button class="new-thread-btn" on:click={newThread} title="Новый тред">+ Новый тред</button>
    </div>

    <div class="thread-list">
      {#each threads as t}
        <button
          class="thread-item"
          class:active={$appState?.thread?.id === t.id}
          on:click={() => openThread(t.id)}
        >
          <div class="thread-title">{t.title || 'Без названия'}</div>
          <div class="thread-meta">
            <span class="thread-date">{formatDate(t.updated)}</span>
            <span class="thread-count">{t.count} сообщ.</span>
            <button
              class="thread-delete"
              on:click={(e) => deleteThread(t.id, e)}
              title="Удалить"
            >x</button>
          </div>
        </button>
      {:else}
        <div class="empty-hint">Нет тредов</div>
      {/each}
    </div>
  {/if}
</aside>

<style>
  .sidebar {
    width: 240px;
    min-width: 200px;
    max-width: 300px;
    display: flex;
    flex-direction: column;
    background: var(--bg2);
    border-right: 1px solid var(--border);
    overflow: hidden;
    flex-shrink: 0;
  }

  .sidebar-header {
    position: relative;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }

  .project-btn {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 14px;
    background: none;
    color: var(--text);
    border-radius: 0;
    font-size: 13px;
    font-weight: 600;
    text-align: left;
  }
  .project-btn:hover { background: var(--bg3); }

  .project-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .chevron {
    flex-shrink: 0;
    transition: transform 0.15s;
  }
  .chevron.open { transform: rotate(180deg); }

  .project-dropdown {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    background: var(--bg2);
    border: 1px solid var(--border);
    border-top: none;
    z-index: 10;
    max-height: 240px;
    overflow-y: auto;
  }

  .project-item {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    padding: 8px 14px;
    background: none;
    color: var(--text);
    border-radius: 0;
    font-size: 13px;
    text-align: left;
    gap: 2px;
  }
  .project-item:hover { background: var(--bg3); }
  .project-item.active { background: rgba(217,119,87,0.12); }

  .p-name { font-weight: 500; }
  .p-cwd  { font-size: 11px; color: var(--text-dim); font-family: var(--mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; width: 100%; }

  .connect-dir {
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 8px 14px 10px;
    border-top: 1px solid var(--border);
  }
  .connect-label {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-dim);
  }
  .connect-row { display: flex; gap: 6px; }
  .connect-row input {
    flex: 1;
    min-width: 0;
    font-size: 12px;
    padding: 5px 8px;
    font-family: var(--mono);
  }
  .connect-btn {
    flex-shrink: 0;
    background: var(--bg3);
    color: var(--accent);
    border: 1px solid rgba(217,119,87,0.35);
    border-radius: var(--radius);
    font-size: 12px;
    padding: 0 10px;
  }
  .connect-btn:hover:not(:disabled) { background: rgba(217,119,87,0.12); }
  .connect-btn:disabled { opacity: 0.5; cursor: default; }
  .connect-error { font-size: 11px; color: var(--red); word-break: break-word; }
  .connect-note { font-size: 10px; color: var(--text-dim); font-style: italic; }

  .search-box {
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }

  .search-box input {
    width: 100%;
    font-size: 13px;
    padding: 5px 9px;
  }

  .search-results {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .search-result-item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    padding: 8px 12px;
    background: none;
    color: var(--text);
    border-radius: 0;
    border-bottom: 1px solid var(--border);
    text-align: left;
    gap: 3px;
  }
  .search-result-item:hover { background: var(--bg3); }

  .sr-title { font-size: 13px; font-weight: 500; }
  .sr-snippet { font-size: 11px; color: var(--text-dim); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; width: 100%; }

  .thread-list-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 12px 4px;
    flex-shrink: 0;
  }

  .section-label {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-dim);
  }

  .new-thread-btn {
    background: none;
    color: var(--accent);
    font-size: 12px;
    padding: 2px 6px;
    border: 1px solid rgba(217,119,87,0.35);
    border-radius: var(--radius);
  }
  .new-thread-btn:hover { background: rgba(217,119,87,0.12); }

  .thread-list {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .thread-item {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    padding: 8px 12px;
    background: none;
    color: var(--text);
    border-radius: 0;
    border-bottom: 1px solid var(--border);
    text-align: left;
    gap: 3px;
    cursor: pointer;
  }
  .thread-item:hover { background: var(--bg3); }
  .thread-item.active { background: rgba(217,119,87,0.1); border-left: 2px solid var(--accent); }

  .thread-title {
    font-size: 13px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    width: 100%;
  }

  .thread-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
  }

  .thread-date, .thread-count {
    font-size: 11px;
    color: var(--text-dim);
  }

  .thread-delete {
    margin-left: auto;
    background: none;
    border: none;
    color: var(--text-dim);
    font-size: 11px;
    padding: 1px 4px;
    opacity: 0;
    transition: opacity 0.1s;
    border-radius: 3px;
  }
  .thread-item:hover .thread-delete { opacity: 0.6; }
  .thread-delete:hover { opacity: 1 !important; color: var(--red); }

  .empty-hint {
    padding: 14px 12px;
    font-size: 12px;
    color: var(--text-dim);
    font-style: italic;
  }
</style>
