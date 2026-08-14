<script>
  // Supervised background jobs. These do not belong to a turn — they were
  // deliberately detached so they could outlive one — so the panel sits outside
  // the transcript and stays put while conversations come and go.
  import { onMount, onDestroy } from 'svelte';
  import { jobs, jobsClock } from '../stores/state.js';
  import { api } from './api.js';

  let expanded = false;
  let openLogs = null;   // job id whose log is open
  let logText = '';
  let logsBusy = false;
  let stopping = {};

  // The server sends a snapshot only when something changes, but a running job's
  // age moves every second regardless. This local tick advances the clock between
  // snapshots so the elapsed times do not sit frozen.
  let tick = 0;
  let timer;
  onMount(() => { timer = setInterval(() => (tick += 1), 1000); });
  onDestroy(() => clearInterval(timer));

  // Age everything against the server's clock, offset by how long ago we heard
  // from it — a browser across an SSH tunnel need not agree with the server about
  // what time it is.
  let baseClock = 0;
  let baseSeen = 0;
  $: if ($jobsClock && $jobsClock !== baseClock) {
    baseClock = $jobsClock;
    baseSeen = Date.now();
  }
  $: now = baseClock ? baseClock + (Date.now() - baseSeen) : Date.now();

  $: running = $jobs.filter(j => j.status === 'running');
  $: finished = $jobs.filter(j => j.status !== 'running');
  // Finished jobs are worth a glance but not permanent residence; only the most
  // recent few are listed once expanded.
  $: recentFinished = finished.slice(0, 5);

  function fmtDur(ms) {
    if (!ms || ms < 0) ms = 0;
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}с`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}м ${s % 60}с`;
    const h = Math.floor(m / 60);
    return `${h}ч ${m % 60}м`;
  }

  function elapsed(j) {
    // A running job's own elapsed value is as old as the last snapshot, so it is
    // advanced locally; a finished one is fixed and taken as given.
    if (j.status !== 'running') return fmtDur(j.elapsedMs);
    const started = new Date(j.startedAt).getTime();
    return fmtDur(now - started);
  }

  // How long a running job has been quiet. This is the real proof of life: the
  // process may be there while the work behind it has wedged.
  function quietFor(j) {
    if (j.status !== 'running' || !j.lastOutputAt) return 0;
    return Math.max(0, now - j.lastOutputAt);
  }

  const QUIET_MS = 120000;

  function statusLabel(s) {
    return {
      running: 'идёт',
      done: 'готово',
      failed: 'ошибка',
      stopped: 'остановлена',
      lost: 'потеряна',
    }[s] || s;
  }

  async function stop(id) {
    stopping = { ...stopping, [id]: true };
    try { await api.stopJob(id); } catch {}
    stopping = { ...stopping, [id]: false };
  }

  async function toggleLogs(id) {
    if (openLogs === id) { openLogs = null; logText = ''; return; }
    openLogs = id;
    logText = '';
    logsBusy = true;
    try {
      const res = await api.jobLogs(id);
      logText = res.logs || '(вывода нет)';
    } catch (err) {
      logText = 'Не удалось прочитать вывод: ' + (err?.message || err);
    }
    logsBusy = false;
  }

  // Reload an open log as the job writes more.
  $: if (openLogs && tick % 3 === 0) refreshOpenLog();
  let refreshing = false;
  async function refreshOpenLog() {
    if (refreshing || logsBusy) return;
    refreshing = true;
    try {
      const res = await api.jobLogs(openLogs);
      logText = res.logs || '(вывода нет)';
    } catch {}
    refreshing = false;
  }
</script>

{#if $jobs.length > 0}
  <div class="jobs" class:has-running={running.length > 0}>
    <button class="jobs-head" on:click={() => (expanded = !expanded)}>
      <span class="chevron" class:open={expanded}>▸</span>
      {#if running.length > 0}
        <span class="pulse"></span>
        <span class="head-text">
          Фоновых задач: {running.length}
          {#if running.length === 1}
            — {running[0].description || running[0].command} · {elapsed(running[0])}
          {/if}
        </span>
      {:else}
        <span class="head-text dim">Фоновые задачи ({finished.length}) — все завершены</span>
      {/if}
    </button>

    {#if expanded || running.length > 0}
      <div class="job-list">
        {#each running as j (j.id)}
          <div class="job">
            <div class="job-row">
              <span class="badge running">{statusLabel(j.status)}</span>
              <span class="job-title" title={j.command}>{j.description || j.command}</span>
              <span class="job-time">{elapsed(j)}</span>
              <button class="job-btn" on:click={() => toggleLogs(j.id)}>
                {openLogs === j.id ? 'скрыть' : 'вывод'}
              </button>
              <button class="job-btn danger" disabled={stopping[j.id]} on:click={() => stop(j.id)}>
                {stopping[j.id] ? '…' : 'стоп'}
              </button>
            </div>
            {#if j.lastOutput}
              <div class="job-last" class:quiet={quietFor(j) > QUIET_MS}>
                {j.lastOutput}
                {#if quietFor(j) > QUIET_MS}
                  <span class="quiet-note">· тишина {fmtDur(quietFor(j))}</span>
                {/if}
              </div>
            {:else}
              <div class="job-last dim">вывода пока нет</div>
            {/if}
            {#if openLogs === j.id}
              <pre class="job-logs">{logsBusy ? 'загрузка…' : logText}</pre>
            {/if}
          </div>
        {/each}

        {#if expanded}
          {#each recentFinished as j (j.id)}
            <div class="job done">
              <div class="job-row">
                <span class="badge {j.status}">{statusLabel(j.status)}</span>
                <span class="job-title" title={j.command}>{j.description || j.command}</span>
                <span class="job-time">{elapsed(j)}</span>
                {#if j.status === 'failed'}<span class="job-code">код {j.exitCode}</span>{/if}
                <button class="job-btn" on:click={() => toggleLogs(j.id)}>
                  {openLogs === j.id ? 'скрыть' : 'вывод'}
                </button>
              </div>
              {#if openLogs === j.id}
                <pre class="job-logs">{logsBusy ? 'загрузка…' : logText}</pre>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  .jobs {
    border-bottom: 1px solid var(--border-soft);
    background: var(--bg2);
    flex-shrink: 0;
    font-size: 12px;
  }

  .jobs-head {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 18px;
    background: none;
    border: none;
    color: var(--text-dim);
    cursor: pointer;
    text-align: left;
    font-size: 12px;
  }
  .jobs.has-running .jobs-head { color: var(--text); }

  .chevron {
    display: inline-block;
    transition: transform 0.15s;
    font-size: 10px;
  }
  .chevron.open { transform: rotate(90deg); }

  /* A running job is the one thing here that must read as alive at a glance. */
  .pulse {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--green, #63c267);
    animation: job-pulse 1.4s ease-in-out infinite;
    flex-shrink: 0;
  }
  @keyframes job-pulse {
    0%, 100% { opacity: 1; transform: scale(1); }
    50% { opacity: 0.4; transform: scale(0.8); }
  }

  .head-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dim { color: var(--text-dim); }

  .job-list {
    padding: 0 18px 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .job {
    border-left: 2px solid var(--green, #63c267);
    padding-left: 9px;
  }
  .job.done { border-left-color: var(--border); }

  .job-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .badge {
    font-family: var(--mono);
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 999px;
    background: var(--bg3);
    color: var(--text-dim);
    flex-shrink: 0;
  }
  .badge.running { background: var(--green-soft); color: var(--green); }
  .badge.failed, .badge.lost { background: var(--red-soft); color: var(--red); }

  .job-title {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .job-time, .job-code {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-dim);
    flex-shrink: 0;
  }

  .job-btn {
    background: none;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text-dim);
    font-size: 11px;
    padding: 1px 7px;
    cursor: pointer;
    flex-shrink: 0;
  }
  .job-btn:hover:not(:disabled) { color: var(--text); }
  .job-btn.danger:hover:not(:disabled) { color: var(--red); border-color: var(--red); }
  .job-btn:disabled { opacity: 0.5; cursor: default; }

  .job-last {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-dim);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-top: 2px;
  }
  /* Silence is reported, never treated as death: the job may simply be busy. */
  .job-last.quiet { color: #e0a85c; }
  .quiet-note { opacity: 0.8; }

  .job-logs {
    margin: 6px 0 2px;
    padding: 8px 10px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 5px;
    font-family: var(--mono);
    font-size: 11px;
    line-height: 1.45;
    max-height: 260px;
    overflow: auto;
    white-space: pre;
    color: var(--text-dim);
  }
</style>
