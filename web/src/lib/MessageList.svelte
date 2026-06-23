<script>
  import { onMount, afterUpdate } from 'svelte';
  import { messages, liveText, streaming, liveTool } from '../stores/state.js';
  import { renderMarkdown } from './markdown.js';

  let listEl;

  afterUpdate(() => {
    if (listEl) {
      listEl.scrollTop = listEl.scrollHeight;
    }
  });

  function formatTime(ts) {
    try {
      return new Date(ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
    } catch {
      return '';
    }
  }

  function toolLabel(msg) {
    const meta = msg.tool_meta;
    if (!meta) return msg.content;
    const mark = meta.allow ? '✓' : '✗';
    const verdict = meta.allow ? 'разрешено' : 'отклонено';
    const target = meta.target ? ` ${meta.target}` : '';
    return `${mark} ${meta.tool || msg.content}${target} · ${verdict}`;
  }
</script>

<div class="message-list" bind:this={listEl}>
  {#each $messages as msg (msg.ts + msg.role + msg.content?.slice(0, 20))}
    {#if msg.role === 'user'}
      <div class="msg msg-user">
        <div class="msg-meta">
          <span class="role-label">вы</span>
          <span class="ts">{formatTime(msg.ts)}</span>
        </div>
        <div class="msg-body">
          <p class="user-text">{msg.content}</p>
          {#if msg.attachments?.length}
            <div class="attachments">
              {#each msg.attachments as a}
                <span class="attachment-tag">{a.split('/').pop()}</span>
              {/each}
            </div>
          {/if}
        </div>
      </div>

    {:else if msg.role === 'assistant'}
      <div class="msg msg-assistant">
        <div class="msg-meta">
          <span class="role-label accent">claude</span>
          <span class="ts">{formatTime(msg.ts)}</span>
        </div>
        <div class="msg-body markdown-body">
          {@html renderMarkdown(msg.content)}
        </div>
      </div>

    {:else if msg.role === 'tool'}
      <div class="msg msg-tool">
        <span class="tool-line">{toolLabel(msg)}</span>
      </div>

    {:else if msg.role === 'system'}
      <div class="msg msg-system" class:msg-error={msg._error}>
        <span>{msg.content}</span>
      </div>
    {/if}
  {/each}

  {#if $streaming}
    <div class="agent-activity" title="агент выполняет ход">
      <span class="heartbeat"></span>
      <span class="activity-text">
        {#if $liveTool}агент работает · <span class="activity-tool">{$liveTool}</span>{:else}агент думает…{/if}
      </span>
    </div>
  {/if}

  {#if $streaming && $liveText}
    <div class="msg msg-assistant">
      <div class="msg-meta">
        <span class="role-label accent">claude</span>
        <span class="ts live-dot"></span>
      </div>
      <div class="msg-body markdown-body">
        {@html renderMarkdown($liveText)}
        <span class="cursor"></span>
      </div>
    </div>
  {/if}
</div>

<style>
  .message-list {
    flex: 1;
    overflow-y: auto;
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .msg {
    display: flex;
    flex-direction: column;
    gap: 3px;
    max-width: 100%;
  }

  .msg-meta {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }

  .role-label {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-dim);
  }

  .role-label.accent { color: var(--accent); }

  .ts {
    font-size: 10px;
    color: var(--text-dim);
  }

  .live-dot::before {
    content: '';
    display: inline-block;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
    animation: blink 1s step-end infinite;
    vertical-align: middle;
  }

  @keyframes blink {
    0%, 100% { opacity: 1; }
    50%       { opacity: 0; }
  }

  .msg-body {
    background: var(--bg2);
    border-radius: var(--radius);
    padding: 8px 12px;
    line-height: 1.6;
    word-break: break-word;
  }

  .msg-user .msg-body {
    background: var(--bg3);
    border-left: 2px solid var(--border);
  }

  .user-text {
    white-space: pre-wrap;
  }

  .attachments {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 6px;
  }

  .attachment-tag {
    font-family: var(--mono);
    font-size: 11px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 1px 6px;
    color: var(--text-dim);
  }

  .msg-tool {
    padding: 2px 0;
  }

  .tool-line {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-dim);
  }

  .msg-system {
    font-size: 12px;
    color: var(--text-dim);
    font-style: italic;
    padding: 1px 0;
  }

  .msg-error {
    color: var(--red);
    font-style: normal;
  }

  .cursor {
    display: inline-block;
    width: 2px;
    height: 1em;
    background: var(--accent);
    vertical-align: text-bottom;
    margin-left: 1px;
    animation: blink 1s step-end infinite;
  }

  .thinking {
    letter-spacing: 3px;
    animation: blink 1s step-end infinite;
  }

  .agent-activity {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 10px;
    margin: 2px 0;
    border-radius: var(--radius);
    background: rgba(106,191,105,0.10);
    border: 1px solid rgba(106,191,105,0.28);
    width: fit-content;
  }

  .activity-text {
    font-size: 12px;
    color: var(--green);
  }

  .activity-tool {
    font-family: var(--mono);
    font-weight: 600;
  }

  .heartbeat {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--green);
    flex-shrink: 0;
    animation: heartbeat 1.1s ease-in-out infinite;
  }

  @keyframes heartbeat {
    0%, 100% { transform: scale(0.7); opacity: 0.55; }
    50%      { transform: scale(1.15); opacity: 1; box-shadow: 0 0 6px var(--green); }
  }

  /* Markdown output styles */
  .markdown-body :global(h1) { font-size: 1.25em; margin: 10px 0 6px; }
  .markdown-body :global(h2) { font-size: 1.1em;  margin: 10px 0 6px; }
  .markdown-body :global(h3) { font-size: 1em;    margin: 8px 0 4px; }
  .markdown-body :global(pre) { margin: 8px 0; }
  .markdown-body :global(ul)  { margin: 4px 0; padding-left: 1.4em; }
  .markdown-body :global(li)  { margin: 2px 0; }
  .markdown-body :global(p)   { margin: 3px 0; }
  .markdown-body :global(strong) { color: #e0e0e0; }
</style>
