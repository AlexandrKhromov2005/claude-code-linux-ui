<script>
  import { onDestroy } from 'svelte';
  import { agents, serverSkew } from '../stores/state.js';

  // A subagent works on its own; between its reports nothing on this side
  // changes, so the panel keeps its own clock to age them by.
  let tick = Date.now();
  const timer = setInterval(() => { tick = Date.now(); }, 1000);
  onDestroy(() => clearInterval(timer));

  // Silence thresholds, in ms. Keep in step with AgentQuietAfter /
  // AgentSilentAfter in internal/core/agents.go, where the reasoning lives.
  const QUIET = 45000;
  const SILENT = 150000;

  function elapsed(a, now) {
    const end = a.status === 'running' ? now : (a.endedAt || now);
    return Math.max(0, end - (a.startedAt || end));
  }

  function silence(a, now) {
    if (a.status !== 'running' || !a.lastSeen) return 0;
    return Math.max(0, now - a.lastSeen);
  }

  // pulse classifies a running subagent by how long it has been quiet. Silence
  // is not death — one long tool call looks exactly like this — so it is
  // reported as the observation it is, and nothing stronger.
  function pulse(a, now) {
    if (a.status !== 'running') return a.status;
    const q = silence(a, now);
    if (q >= SILENT) return 'silent';
    if (q >= QUIET) return 'quiet';
    return 'running';
  }

  function verdict(a, now) {
    switch (pulse(a, now)) {
      case 'running': return `работает ${clock(elapsed(a, now))}`;
      case 'quiet':   return `тишина ${clock(silence(a, now))}`;
      case 'silent':  return `нет сигнала ${clock(silence(a, now))}`;
      case 'done':    return `готов за ${clock(elapsed(a, now))}`;
      case 'failed':  return 'ошибка';
      case 'aborted': return `прерван на ${clock(elapsed(a, now))}`;
      default:        return '';
    }
  }

  function mark(status) {
    switch (status) {
      case 'done':    return '✓';
      case 'failed':  return '✗';
      case 'aborted': return '⊘';
      default:        return '';
    }
  }

  // The rows are built in one reactive statement that names both the list and
  // the clock, because the ages have to keep moving between server updates and
  // Svelte only re-runs what visibly depends on the tick.
  function rows(list, now) {
    return list.map(a => ({
      id: a.id,
      type: a.type || 'agent',
      task: a.task || '',
      runs: a.runs,
      live: a.status === 'running',
      pulse: pulse(a, now),
      mark: mark(a.status),
      verdict: verdict(a, now),
      detail: detail(a),
    }));
  }

  function clock(ms) {
    const s = Math.round(ms / 1000);
    if (s < 60) return `${s}с`;
    const m = Math.floor(s / 60);
    return `${m}м ${String(s % 60).padStart(2, '0')}с`;
  }

  function tokens(n) {
    if (!n) return '';
    if (n < 1000) return `${n} тк`;
    return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k тк`;
  }

  // detail is the line under the title: what it is doing now while it runs,
  // what it got through once it has stopped.
  function detail(a) {
    const bits = [];
    if (a.status === 'running' && a.activity) bits.push(a.activity);
    if (a.tool) bits.push(a.tool);
    if (a.toolUses) bits.push(`${a.toolUses} вызов${plural(a.toolUses)}`);
    if (a.tokens) bits.push(tokens(a.tokens));
    return bits.join(' · ');
  }

  function plural(n) {
    const t = n % 10, h = n % 100;
    if (t === 1 && h !== 11) return '';
    if (t >= 2 && t <= 4 && (h < 12 || h > 14)) return 'а';
    return 'ов';
  }

  // summary counts the outcomes once nothing is running. The impersonal
  // "готово: 2" form is used because it reads correctly for any count.
  function summary(list) {
    const n = s => list.filter(a => a.status === s).length;
    return [
      [n('done'), 'готово'],
      [n('failed'), 'с ошибкой'],
      [n('aborted'), 'прервано'],
    ].filter(([c]) => c > 0).map(([c, label]) => `${label}: ${c}`).join(' · ');
  }

  $: view = rows($agents, tick + $serverSkew);
  $: running = view.filter(r => r.live).length;
  $: outcome = summary($agents);
</script>

{#if view.length}
  <div class="agent-panel">
    <div class="panel-head">
      <span class="panel-title">сабагенты</span>
      <span class="panel-count">
        {#if running}в работе: {running} из {view.length}{:else}{outcome}{/if}
      </span>
    </div>

    {#each view as r (r.id)}
      <div class="agent-row" class:ended={!r.live}>
        <span class="dot {r.pulse}" title={r.verdict}>{r.mark}</span>
        <div class="agent-body">
          <div class="agent-line">
            <span class="agent-type">{r.type}</span>
            {#if r.runs > 1}<span class="agent-runs" title="задач выдано этому агенту">×{r.runs}</span>{/if}
            <span class="agent-task">{r.task}</span>
            <span class="agent-verdict {r.pulse}">{r.verdict}</span>
          </div>
          {#if r.detail}
            <div class="agent-detail">{r.detail}</div>
          {/if}
        </div>
      </div>
    {/each}
  </div>
{/if}

<style>
  .agent-panel {
    width: 100%;
    max-width: 820px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 12px 9px;
    border: 1px solid var(--border);
    border-left: 2px solid var(--accent-dim);
    border-radius: var(--radius);
    background: var(--bg2);
  }

  .panel-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 4px;
  }

  .panel-title {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    color: var(--text-mute);
  }

  .panel-count {
    font-size: 11px;
    color: var(--text-dim);
  }

  .agent-row {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 3px 0;
  }

  .agent-row.ended { opacity: 0.62; }

  .agent-body { min-width: 0; flex: 1; }

  .agent-line {
    display: flex;
    align-items: baseline;
    gap: 7px;
    flex-wrap: wrap;
  }

  .agent-type {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--accent);
  }

  .agent-runs {
    font-family: var(--mono);
    font-size: 10px;
    color: var(--text-mute);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
  }

  .agent-task {
    font-size: 12px;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }

  .agent-verdict {
    font-size: 11px;
    color: var(--text-dim);
    white-space: nowrap;
  }
  .agent-verdict.running { color: var(--green); }
  .agent-verdict.quiet   { color: #d3a24a; }
  .agent-verdict.silent  { color: var(--red); }
  .agent-verdict.failed  { color: var(--red); }

  .agent-detail {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-mute);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* The dot carries the state at a glance: it beats while the subagent is
     talking to us, slows when it goes quiet, and stops when nothing arrives. */
  .dot {
    width: 10px;
    height: 10px;
    margin-top: 4px;
    border-radius: 50%;
    flex-shrink: 0;
    font-size: 9px;
    line-height: 10px;
    text-align: center;
  }
  .dot.running {
    background: var(--green);
    animation: agent-beat 1.1s ease-in-out infinite;
  }
  .dot.quiet {
    background: #d3a24a;
    animation: agent-beat 2.6s ease-in-out infinite;
  }
  .dot.silent {
    background: transparent;
    border: 2px solid var(--red);
  }
  .dot.done    { background: none; color: var(--green); }
  .dot.failed  { background: none; color: var(--red); }
  .dot.aborted { background: none; color: var(--text-mute); }

  @keyframes agent-beat {
    0%, 100% { transform: scale(0.72); opacity: 0.5; }
    50%      { transform: scale(1.1); opacity: 1; box-shadow: 0 0 7px currentColor; }
  }

  @media (prefers-reduced-motion: reduce) {
    .dot.running, .dot.quiet { animation: none; }
  }
</style>
