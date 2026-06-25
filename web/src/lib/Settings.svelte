<script>
  import { appState } from '../stores/state.js';
  import { api } from './api.js';

  export let open = false;

  let memory = '';
  let memoryLoading = false;
  let exportMsg = '';
  let budgetInput = '';
  let saving = {};

  let autoMem = '';
  let autoEnabled = true;

  $: if (open) {
    loadMemory();
    loadAutoMemory();
    budgetInput = String($appState?.budget ?? 0);
  }

  async function loadAutoMemory() {
    try {
      const r = await api.getAutoMemory();
      autoMem = r.content || '';
      autoEnabled = !!r.enabled;
    } catch {}
  }

  async function toggleAutoMemory() {
    try {
      const r = await api.setAutoMemory({ enabled: !autoEnabled });
      autoEnabled = !!r.enabled;
    } catch {}
  }

  async function clearAutoMemory() {
    if (!confirm('Очистить накопленную память проекта?')) return;
    try {
      const r = await api.setAutoMemory({ clear: true });
      autoMem = r.content || '';
    } catch {}
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

      <!-- Cross-thread auto memory -->
      <section class="memory-section">
        <h3>Память между тредами (авто)</h3>
        <div class="row">
          <button class="toggle-btn" class:on={autoEnabled} on:click={toggleAutoMemory}>
            <span class="toggle-knob"></span>
            {autoEnabled ? 'Включено' : 'Выключено'}
          </button>
          <button class="save-btn ghost" on:click={clearAutoMemory} disabled={!autoMem}>Очистить</button>
        </div>
        <p class="hint">После каждого хода Claude кратко резюмирует ключевые факты сюда (дешёвой моделью), и эта память подмешивается во все треды проекта.</p>
        {#if autoMem}
          <pre class="auto-mem">{autoMem}</pre>
        {:else}
          <p class="hint">Пока пусто — наполнится по ходу диалогов.</p>
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

  .toggle-btn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    background: var(--bg3);
    color: var(--text-dim);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 5px 12px 5px 6px;
    font-size: 13px;
  }
  .toggle-knob {
    width: 22px;
    height: 14px;
    border-radius: 999px;
    background: var(--border);
    position: relative;
    transition: background 0.15s;
    flex-shrink: 0;
  }
  .toggle-knob::after {
    content: '';
    position: absolute;
    top: 2px; left: 2px;
    width: 10px; height: 10px;
    border-radius: 50%;
    background: var(--text-dim);
    transition: transform 0.15s, background 0.15s;
  }
  .toggle-btn.on { color: var(--accent); border-color: var(--accent-border); }
  .toggle-btn.on .toggle-knob { background: var(--accent-soft); }
  .toggle-btn.on .toggle-knob::after { transform: translateX(8px); background: var(--accent); }

  .save-btn.ghost {
    background: none;
    color: var(--text-dim);
    border: 1px solid var(--border);
  }
  .save-btn.ghost:hover:not(:disabled) { background: var(--bg3); color: var(--text); }

  .auto-mem {
    font-family: var(--mono);
    font-size: 12px;
    background: var(--bg3);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 10px 12px;
    max-height: 200px;
    overflow-y: auto;
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--text);
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
