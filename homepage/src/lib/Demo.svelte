<script lang="ts">
  type Status = 'investigating' | 'in_progress' | 'needs_info' | 'blocked' | 'done'

  const statuses: { key: Status; emoji: string; label: string }[] = [
    { key: 'investigating', emoji: '👀', label: 'investigating' },
    { key: 'in_progress', emoji: '🚧', label: 'in progress' },
    { key: 'needs_info', emoji: '❓', label: 'needs info' },
    { key: 'blocked', emoji: '⛔', label: 'blocked' },
    { key: 'done', emoji: '✅', label: 'done' },
  ]

  let claimed = $state(false)
  let reacted = $state<Status[]>([])
  let status = $state<Status | undefined>()
  let hint = $state('')

  // Mirrors Task.React in pkg/task: a new status reaction overwrites the status, removing the current one falls back
  // to claimed, removing any other changes nothing, and the last owner leaving clears the status unless it is done.
  let current = $derived(statuses.find((s) => s.key === status))
  let label = $derived(current ? `${current.emoji} ${current.label}` : claimed ? '🙋 claimed' : '⚪ unclaimed')

  function claim() {
    hint = ''
    claimed = !claimed
    if (!claimed && status !== 'done') status = undefined
  }

  function react(s: Status) {
    hint = ''
    if (!claimed) {
      // Task.React denies only an added status reaction from a non-owner; a removal is silent.
      if (reacted.includes(s)) reacted = reacted.filter((x) => x !== s)
      else hint = 'Only owners can set the status. React 🙋 first to take it.'
      return
    }
    if (reacted.includes(s)) {
      reacted = reacted.filter((x) => x !== s)
      if (status === s) status = undefined
    } else {
      reacted = [...reacted, s]
      status = s
    }
  }

  function reset() {
    claimed = false
    reacted = []
    status = undefined
    hint = ''
  }
</script>

<div class="slack" role="region" aria-label="Interactive example of a task in Slack">
  <div class="head"><span># itakeit</span><button type="button" class="reset" onclick={reset}>reset</button></div>

  <div class="msg">
    <div class="avatar a1">M</div>
    <div class="body">
      <div class="meta"><b>Maya</b> <span>10:42</span></div>
      <p>Checkout returns 500 for EU cards since this morning's deploy.</p>
      <div class="reactions">
        <button type="button" class:on={claimed} aria-pressed={claimed} onclick={claim} title="take it">🙋 <small>{claimed ? 1 : 0}</small></button>
        {#each statuses as s (s.key)}
          <button type="button" class:on={reacted.includes(s.key)} aria-pressed={reacted.includes(s.key)} onclick={() => react(s.key)} title={s.label}>{s.emoji} <small>{reacted.includes(s.key) ? 1 : 0}</small></button>
        {/each}
      </div>
      <div aria-live="polite">{#if hint}<p class="hint">Only visible to you: {hint}</p>{/if}</div>

      <div class="thread">
        <div class="msg">
          <div class="avatar bot"><img src="./turtle.png" alt="" /></div>
          <div class="body">
            <div class="meta"><b>itakeit</b> <span class="app">APP</span></div>
            <p aria-live="polite"><b>Status:</b> {label}<br /><b>Owners:</b> {claimed ? '@you' : 'nobody yet'}</p>
            <p class="legend">React on the message above: 🙋 take it · 👀 investigating · 🚧 in progress · ❓ needs info · ⛔ blocked · ✅ done. Status reactions count from owners only.</p>
          </div>
        </div>
        {#if status === 'needs_info'}
          <div class="msg">
            <div class="avatar bot"><img src="./turtle.png" alt="" /></div>
            <div class="body">
              <div class="meta"><b>itakeit</b> <span class="app">APP</span></div>
              <p><span class="mention">@Maya</span>: <span class="mention">@you</span> needs more details. Please reply in this thread.</p>
            </div>
          </div>
        {/if}
      </div>
    </div>
  </div>

  <div class="board">
    <div class="meta">📌 Pinned board</div>
    {#if status === 'done'}
      <p>I take it: 0 open. Nothing open. 🎉</p>
    {:else}
      <p><b>I take it: 1 open</b><br />{label.replace(' ', ' 1 ')}<br />• {label} &nbsp;Checkout returns 500 for EU cards… · {claimed ? '@you' : 'unclaimed'}</p>
    {/if}
  </div>
</div>

<style>
  .slack { background: #fff; color: #1d1c1d; border: 2px solid var(--ink); box-shadow: 6px 6px 0 var(--ink); font-size: 0.92rem; text-align: left; min-width: 0; }
  .head { display: flex; justify-content: space-between; align-items: center; padding: 0.55rem 0.9rem; border-bottom: 1px solid #e3e3e3; font-weight: 700; }
  .reset { font: 0.75rem var(--mono); background: none; border: 1px solid #ccc; padding: 0.2rem 0.5rem; cursor: pointer; color: #555; }
  .msg { display: flex; gap: 0.6rem; padding: 0.7rem 0.9rem 0.2rem; }
  .body { min-width: 0; flex: 1; }
  .body p { margin: 0.15rem 0 0.35rem; }
  .avatar { flex: none; width: 36px; height: 36px; display: grid; place-items: center; font-weight: 700; color: #fff; }
  .a1 { background: #c2548a; }
  .bot { background: #fff; border: 1px solid #e3e3e3; overflow: hidden; }
  .bot img { width: 34px; height: 34px; object-fit: contain; }
  .meta { font-size: 0.85rem; }
  .meta span { color: #666; font-size: 0.75rem; margin-left: 0.25rem; }
  .meta .app { background: #eee; padding: 0 0.25rem; font-size: 0.65rem; font-weight: 700; }
  .reactions { display: flex; flex-wrap: wrap; gap: 0.3rem; margin: 0.3rem 0 0.4rem; }
  .reactions button { font-size: 0.9rem; border: 1px solid #ddd; background: #f4f4f4; border-radius: 999px; padding: 0.15rem 0.55rem; cursor: pointer; transition: transform 0.1s; }
  .reactions button:hover { border-color: #999; transform: translateY(-1px); }
  .reactions button.on { background: #e3f1ff; border-color: #1d9bd1; }
  .reactions small { font-size: 0.75rem; color: #555; }
  .hint { font-size: 0.8rem; color: #616061; background: #f8f8f8; border-left: 3px solid #bbb; padding: 0.3rem 0.5rem; }
  .thread { border-left: 2px solid #e3e3e3; margin: 0.3rem 0 0.6rem; }
  .thread .msg { padding: 0.4rem 0.6rem 0.1rem; }
  .legend { font-style: italic; color: #616061; font-size: 0.8rem; }
  .mention { background: #e8f5fa; color: #1264a3; padding: 0 0.15rem; }
  .board { border-top: 1px dashed #ccc; padding: 0.6rem 0.9rem 0.7rem; background: #fbfaf5; }
  .board p { margin: 0.2rem 0 0; font-size: 0.85rem; }
</style>
