<script>
  import { appState } from '../stores/state.js';
  import { api } from './api.js';

  export let open = false;

  let memory = '';
  let memoryLoading = false;
  let exportMsg = '';
  let budgetInput = '';
  let saving = {};

  $: if (open) {
    loadMemory();
    budgetInput = String($appState?.budget ?? 0);
  }

  async function loadMemory() {
    memoryLoading = true;
    try {
      const r = await api.getMemory();
      memory = r.content;
    } catch {} finally {
      memoryLoading = false;
    }
  }

  async function saveMemory() {
    saving.memory = true;
    try {
      await api.setMemory(memory);
    } catch {} finally {
      saving.memory = false;
    }
  }

  async function setTheme(name) {
    try {
      await api.setTheme(name);
      appState.update(s => s ? { ...s, theme: name } : s);
    } catch {}
  }

  async function saveBudget() {
    const usd = parseFloat(budgetInput);
    if (isNaN(usd) || usd < 0) return;
    saving.budget = true;
    try {
      await api.setBudget(usd);
      appState.update(s => s ? { ...s, budget: usd } : s);
    } catch {} finally {
      saving.budget = false;
    }
  }

  async function doExport() {
    exportMsg = '';
    try {
      const r = await api.exportThread();
      exportMsg = `Сохранено: ${r.path}`;
    } catch (err) {
      exportMsg = `Ошибка: ${err.message}`;
    }
  }
</script>

{#if open}
  <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
  <div class="overlay" role="dialog" aria-modal="true" tabindex="-1" on:click|self={() => (open = false)} on:keydown|self={(e) => e.key === 'Escape' && (open = false)}>
    <div class="panel">
      <div class="panel-header">
        <span>Настройки</span>
        <button class="close-btn" on:click={() => (open = false)}>x</button>
      </div>

      <!-- Theme -->
      <section>
        <h3>Тема</h3>
        <div class="theme-row">
          {#each ['dark', 'light', 'nord'] as t}
            <button
              class="theme-btn"
              class:active={$appState?.theme === t}
              on:click={() => setTheme(t)}
            >{t}</button>
          {/each}
        </div>
      </section>

      <!-- Budget -->
      <section>
        <h3>Лимит бюджета ($)</h3>
        <div class="row">
          <input type="number" min="0" step="0.5" bind:value={budgetInput} />
          <button class="save-btn" on:click={saveBudget} disabled={saving.budget}>
            {saving.budget ? '...' : 'Сохранить'}
          </button>
        </div>
        <p class="hint">0 — без предупреждения</p>
      </section>

      <!-- Export -->
      <section>
        <h3>Экспорт треда</h3>
        <button class="save-btn" on:click={doExport}>Экспортировать</button>
        {#if exportMsg}<p class="hint">{exportMsg}</p>{/if}
      </section>

      <!-- Memory -->
      <section class="memory-section">
        <h3>Память проекта</h3>
        {#if memoryLoading}
          <p class="hint">Загрузка...</p>
        {:else}
          <textarea bind:value={memory} rows="6" placeholder="Контекст, который добавляется к каждому треду проекта"></textarea>
          <button class="save-btn" on:click={saveMemory} disabled={saving.memory}>
            {saving.memory ? '...' : 'Сохранить'}
          </button>
        {/if}
      </section>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0,0,0,0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
  }

  .panel {
    background: var(--bg2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    width: min(480px, 92vw);
    max-height: 85vh;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 18px 12px;
    border-bottom: 1px solid var(--border);
    font-weight: 600;
    font-size: 14px;
  }

  .close-btn {
    background: none;
    color: var(--text-dim);
    font-size: 14px;
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .close-btn:hover { color: var(--text); }

  section {
    padding: 14px 18px;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  h3 {
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-dim);
  }

  .theme-row {
    display: flex;
    gap: 8px;
  }

  .theme-btn {
    background: var(--bg3);
    color: var(--text);
    border: 1px solid var(--border);
    font-size: 13px;
    padding: 5px 14px;
  }
  .theme-btn:hover { border-color: var(--text-dim); }
  .theme-btn.active { border-color: var(--accent); color: var(--accent); }

  .row {
    display: flex;
    gap: 8px;
    align-items: center;
  }

  .row input { width: 100px; }

  .save-btn {
    background: var(--accent);
    color: #fff;
    font-size: 13px;
    padding: 5px 14px;
    align-self: flex-start;
  }
  .save-btn:hover:not(:disabled) { background: var(--accent-dim); }

  .hint {
    font-size: 12px;
    color: var(--text-dim);
  }

  .memory-section textarea {
    width: 100%;
    resize: vertical;
    min-height: 100px;
    font-family: var(--mono);
    font-size: 12px;
    background: var(--bg3);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 8px 10px;
  }
</style>
