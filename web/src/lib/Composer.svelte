<script>
  import { streaming, attachments } from '../stores/state.js';
  import { sendMessage, cancelTurn } from './ws.js';
  import { api } from './api.js';

  let text = '';
  let textareaEl;
  let uploading = false;
  let uploadError = '';

  function handleKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }

  function submit() {
    const t = text.trim();
    if (!t && $attachments.length === 0) return;
    if ($streaming) return;
    const paths = $attachments.map(a => a.path);
    sendMessage(t, paths);
    text = '';
    attachments.set([]);
  }

  async function uploadFiles(files) {
    if (!files?.length) return;
    uploading = true;
    uploadError = '';
    try {
      for (const file of files) {
        const result = await api.upload(file);
        attachments.update(list => [...list, { path: result.path, name: result.name }]);
      }
    } catch (err) {
      uploadError = err.message;
    } finally {
      uploading = false;
    }
  }

  function handleFileInput(e) {
    uploadFiles(e.target.files);
    e.target.value = '';
  }

  function handleDrop(e) {
    e.preventDefault();
    uploadFiles(e.dataTransfer.files);
  }

  function handlePaste(e) {
    const items = e.clipboardData?.items;
    if (!items) return;
    const files = [];
    for (const item of items) {
      if (item.kind === 'file') {
        const f = item.getAsFile();
        if (f) files.push(f);
      }
    }
    if (files.length) uploadFiles(files);
  }

  function removeAttachment(idx) {
    attachments.update(list => list.filter((_, i) => i !== idx));
  }
</script>

<div
  class="composer"
  role="region"
  aria-label="Поле ввода"
  on:dragover|preventDefault
  on:drop={handleDrop}
>
  {#if $attachments.length > 0}
    <div class="attach-tray">
      {#each $attachments as att, i}
        <span class="att-chip">
          <span class="att-name">{att.name}</span>
          <button class="att-remove" on:click={() => removeAttachment(i)} title="убрать">x</button>
        </span>
      {/each}
    </div>
  {/if}

  {#if uploadError}
    <div class="upload-error">{uploadError}</div>
  {/if}

  <div class="input-row">
    <textarea
      bind:this={textareaEl}
      bind:value={text}
      placeholder="Сообщение... (Enter — отправить, Shift+Enter — новая строка)"
      rows="3"
      disabled={$streaming}
      on:keydown={handleKeydown}
      on:paste={handlePaste}
    ></textarea>

    <div class="controls">
      <label class="attach-btn" title="Прикрепить файл" class:disabled={uploading}>
        <input
          type="file"
          multiple
          on:change={handleFileInput}
          style="display:none"
          disabled={uploading}
        />
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
        </svg>
      </label>

      {#if $streaming}
        <button class="btn-cancel" on:click={cancelTurn} title="Отменить">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor">
            <rect x="6" y="6" width="12" height="12" rx="1"/>
          </svg>
        </button>
      {:else}
        <button
          class="btn-send"
          on:click={submit}
          disabled={(!text.trim() && $attachments.length === 0) || uploading}
          title="Отправить"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
            <line x1="22" y1="2" x2="11" y2="13"/>
            <polygon points="22 2 15 22 11 13 2 9 22 2"/>
          </svg>
        </button>
      {/if}
    </div>
  </div>
</div>

<style>
  .composer {
    border-top: 1px solid var(--border);
    padding: 10px 16px 12px;
    background: var(--bg);
    flex-shrink: 0;
  }

  .attach-tray {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 8px;
  }

  .att-chip {
    display: flex;
    align-items: center;
    gap: 4px;
    background: var(--bg3);
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 2px 6px 2px 8px;
    font-size: 12px;
    font-family: var(--mono);
  }

  .att-name { color: var(--text-dim); max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  .att-remove {
    background: none;
    border: none;
    color: var(--text-dim);
    font-size: 11px;
    padding: 0 2px;
    line-height: 1;
    cursor: pointer;
    opacity: 0.6;
  }
  .att-remove:hover { opacity: 1; color: var(--red); }

  .upload-error {
    font-size: 12px;
    color: var(--red);
    margin-bottom: 6px;
  }

  .input-row {
    display: flex;
    gap: 8px;
    align-items: flex-end;
  }

  textarea {
    flex: 1;
    resize: none;
    min-height: 64px;
    max-height: 200px;
    font-size: 14px;
    line-height: 1.5;
    background: var(--bg2);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 8px 12px;
    outline: none;
    transition: border-color 0.15s;
  }

  textarea:focus { border-color: var(--accent); }
  textarea:disabled { opacity: 0.5; }

  .controls {
    display: flex;
    flex-direction: column;
    gap: 6px;
    flex-shrink: 0;
  }

  .attach-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    background: var(--bg2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text-dim);
    cursor: pointer;
    transition: color 0.15s, border-color 0.15s;
  }
  .attach-btn:hover { color: var(--text); border-color: var(--text-dim); }
  .attach-btn.disabled { opacity: 0.4; pointer-events: none; }

  .btn-send, .btn-cancel {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border-radius: var(--radius);
    border: 1px solid transparent;
  }

  .btn-send {
    background: var(--accent);
    color: #fff;
  }
  .btn-send:hover:not(:disabled) { background: var(--accent-dim); }

  .btn-cancel {
    background: rgba(224,92,92,0.18);
    color: var(--red);
    border-color: rgba(224,92,92,0.35);
  }
  .btn-cancel:hover { background: rgba(224,92,92,0.28); }
</style>
