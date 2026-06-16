<script>
  import { pendingApproval } from '../stores/state.js';
  import { sendApproval } from './ws.js';

  let rememberInput = '';
  let showRemember = false;

  $: if ($pendingApproval) {
    rememberInput = $pendingApproval.toolName || '';
    showRemember = false;
  }

  function allow() {
    sendApproval($pendingApproval.id, true, null);
  }

  function allowRemember() {
    if (!showRemember) {
      showRemember = true;
      return;
    }
    sendApproval($pendingApproval.id, true, rememberInput);
    showRemember = false;
  }

  function deny() {
    sendApproval($pendingApproval.id, false, null);
    showRemember = false;
  }

  function previewKind(p) {
    return p?.kind || 'generic';
  }
</script>

{#if $pendingApproval}
  <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
  <div class="overlay" role="dialog" aria-modal="true" tabindex="-1" on:click|self={deny} on:keydown|self={(e) => e.key === 'Escape' && deny()}>
    <div class="modal">
      <div class="modal-header">
        <span class="tool-name">{$pendingApproval.toolName}</span>
        <span class="label">запрос разрешения</span>
      </div>

      <div class="preview">
        {#if previewKind($pendingApproval.preview) === 'command'}
          {#if $pendingApproval.preview.description}
            <p class="description">{$pendingApproval.preview.description}</p>
          {/if}
          <pre class="command-line">$ {$pendingApproval.preview.command}</pre>

        {:else if previewKind($pendingApproval.preview) === 'write' || previewKind($pendingApproval.preview) === 'edit'}
          {#if $pendingApproval.preview.path}
            <p class="file-path">{$pendingApproval.preview.path}</p>
          {/if}
          {#if $pendingApproval.preview.diff}
            <div class="diff">
              {#each $pendingApproval.preview.diff as line}
                <div class="diff-line diff-{line.op === '+' ? 'add' : 'del'}">
                  <span class="diff-op">{line.op}</span>{line.text}
                </div>
              {/each}
            </div>
          {/if}

        {:else}
          <pre class="raw">{$pendingApproval.preview?.raw || JSON.stringify($pendingApproval.input, null, 2)}</pre>
        {/if}
      </div>

      {#if showRemember}
        <div class="remember-row">
          <input
            type="text"
            bind:value={rememberInput}
            placeholder="правило (например: Bash)"
          />
        </div>
      {/if}

      <div class="actions">
        <button class="btn-allow" on:click={allow}>Разрешить</button>
        <button class="btn-remember" on:click={allowRemember}>
          {showRemember ? 'Подтвердить и запомнить' : 'Запомнить и разрешить'}
        </button>
        <button class="btn-deny" on:click={deny}>Отклонить</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0,0,0,0.65);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
  }

  .modal {
    background: var(--bg2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    width: min(640px, 92vw);
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .modal-header {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 14px 18px 10px;
    border-bottom: 1px solid var(--border);
  }

  .tool-name {
    font-family: var(--mono);
    font-size: 13px;
    color: var(--accent);
    font-weight: 600;
  }

  .label {
    font-size: 11px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .preview {
    padding: 14px 18px;
    overflow-y: auto;
    flex: 1;
    min-height: 0;
  }

  .description {
    color: var(--text-dim);
    margin-bottom: 8px;
    font-size: 13px;
  }

  .file-path {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-dim);
    margin-bottom: 8px;
  }

  .command-line {
    font-family: var(--mono);
    font-size: 13px;
    background: var(--bg3);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 10px 14px;
    overflow-x: auto;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
  }

  .diff {
    font-family: var(--mono);
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }

  .diff-line {
    padding: 1px 10px;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .diff-add { background: rgba(106,191,105,0.12); color: var(--green); }
  .diff-del { background: rgba(224,92,92,0.12);   color: var(--red); }

  .diff-op {
    display: inline-block;
    width: 14px;
    user-select: none;
    opacity: 0.7;
  }

  .raw {
    font-family: var(--mono);
    font-size: 12px;
    background: var(--bg3);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 10px 14px;
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .remember-row {
    padding: 0 18px 10px;
  }

  .remember-row input {
    width: 100%;
  }

  .actions {
    display: flex;
    gap: 8px;
    padding: 12px 18px 14px;
    border-top: 1px solid var(--border);
  }

  .btn-allow {
    background: rgba(106,191,105,0.18);
    color: var(--green);
    border: 1px solid rgba(106,191,105,0.35);
  }
  .btn-allow:hover { background: rgba(106,191,105,0.28); }

  .btn-remember {
    background: rgba(217,119,87,0.18);
    color: var(--accent);
    border: 1px solid rgba(217,119,87,0.35);
  }
  .btn-remember:hover { background: rgba(217,119,87,0.28); }

  .btn-deny {
    background: rgba(224,92,92,0.18);
    color: var(--red);
    border: 1px solid rgba(224,92,92,0.35);
    margin-left: auto;
  }
  .btn-deny:hover { background: rgba(224,92,92,0.28); }
</style>
