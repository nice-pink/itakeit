<script lang="ts">
  import manifest from '../../slack-app-manifest.yaml?raw'
  import Code from './lib/Code.svelte'
  import Demo from './lib/Demo.svelte'
  import { image, repo } from './lib/site'

  const features = [
    { icon: '📝', title: 'Every message is a task', text: 'Post an issue in the channel. The bot replies in its thread with a status card.' },
    { icon: '🙋', title: 'Claim with one reaction', text: 'React 🙋 to take it. Several people can own a task. Remove the reaction to hand it back.' },
    { icon: '🚧', title: 'Status by emoji', text: 'Owners react 👀 investigating, 🚧 in progress, ⛔ blocked or ✅ done. The latest reaction wins.' },
    { icon: '❓', title: 'Ask the reporter', text: '❓ pings the reporter in the thread. Their reply pings the owners and clears the status.' },
    { icon: '☑️', title: 'Checklists', text: 'Lines written as [ ] item in the issue become checkboxes on the status card. Anyone can tick them.' },
    { icon: '📌', title: 'A pinned board', text: 'One pinned message counts the open tasks in each status and lists the oldest with their owners, always current.' },
    { icon: '🔓', title: 'Status without claiming', text: 'Optional: with status_claims on, any status reaction makes you an owner, no 🙋 needed first.' },
    { icon: '⏰', title: 'Nudges for stale work', text: 'Owners who go quiet on a task get a reminder in the thread, repeated every configurable number of hours until they post or the status changes.' },
  ]

  const config = `channel: C0123456789\ndb_path: /data/itakeit.db`

  const run = `docker run -d --name itakeit --restart unless-stopped -e SLACK_BOT_TOKEN -e SLACK_APP_TOKEN -v "$PWD/config.yaml:/config/config.yaml:ro" -v itakeit-data:/data ${image}`

  const compose = `services:
  itakeit:
    image: ${image}
    restart: unless-stopped
    environment:
      SLACK_BOT_TOKEN: \${SLACK_BOT_TOKEN}
      SLACK_APP_TOKEN: \${SLACK_APP_TOKEN}
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - itakeit-data:/data

volumes:
  itakeit-data:`

  const usage = [
    ['anyone', 'posts a top-level message', 'new task, status card in its thread, board updated'],
    ['anyone', 'reacts 🙋 on the task', 'becomes an owner'],
    ['owner', 'removes 🙋', 'stops owning it. When the last owner leaves, the task is unclaimed again, unless it is done.'],
    ['owner', 'reacts 👀 / 🚧 / ⛔ / ✅', 'sets the status. ✅ removes the task from the board.'],
    ['owner', 'removes the current status reaction', 'status falls back to claimed'],
    ['owner', 'reacts ❓', 'reporter is pinged in the thread'],
    ['reporter', 'replies while ❓ is set', 'owners are pinged, status falls back to claimed'],
    ['owner', 'replies in the thread', 'counts as activity and resets the reminder clock'],
    ['non-owner', 'reacts with a status emoji', 'ignored, with a private hint to claim first. With status_claims: true it makes them an owner and sets the status.'],
    ['owner', 'removes their last 🙋 or status reaction, with status_claims: true', 'stops owning it'],
    ['anyone', 'ticks a checklist item on the card', 'card and board show progress. Ticking the last item pings the owners, or the reporter if unclaimed, to set ✅.'],
  ]
</script>

<header class="nav">
  <div class="wrap row">
    <a class="brand" href="#top"><img src="./turtle.png" alt="" /> itakeit</a>
    <nav>
      <a href="#how">How it works</a>
      <a href="#setup">Setup</a>
      <a href="#usage">Usage</a>
      <a href={repo}>GitHub</a>
    </nav>
  </div>
</header>

<main id="top">
  <section class="hero wrap">
    <div class="pitch">
      <img class="turtle" src="./turtle.png" alt="itakeit pixel turtle" width="400" height="259" />
      <h1>Task tracking in Slack threads. <span>Dead simple.</span></h1>
      <p class="lead"><b>itakeit</b> turns every message in one Slack channel into a task. People take it with 🙋, set the status with reactions, and a pinned board shows what is open. The work is tracked where you already talk about it.</p>
      <div class="cta">
        <a class="btn" href="#setup">Set it up</a>
        <a class="btn ghost" href={repo}>View on GitHub</a>
      </div>
      <p class="small">Self-hosted. One Docker container, one SQLite file, no public URL.</p>
    </div>
    <div class="demo">
      <Demo />
      <p class="small center">Try it: click the reactions.</p>
    </div>
  </section>

  <section id="how" class="band">
    <div class="wrap">
      <h2>How it works</h2>
      <p class="sub">One dedicated channel. No forms, no extra tool, no context switch.</p>
      <div class="grid">
        {#each features as f (f.title)}
          <article class="card">
            <div class="icon">{f.icon}</div>
            <h3>{f.title}</h3>
            <p>{f.text}</p>
          </article>
        {/each}
      </div>
    </div>
  </section>

  <section id="setup" class="wrap setup">
    <h2>Setup</h2>
    <p class="sub">About ten minutes. You need a Slack workspace where you can create apps and a machine that runs Docker.</p>

    <ol class="steps">
      <li>
        <h3>Create the Slack app</h3>
        <p>Open <a href="https://api.slack.com/apps">api.slack.com/apps</a>, choose <b>Create New App</b>, then <b>From an app manifest</b>, pick your workspace and paste this manifest into the YAML tab.</p>
        <details>
          <summary>Show slack-app-manifest.yaml</summary>
          <Code code={manifest.trim()} label="slack-app-manifest.yaml" />
        </details>
        <p>It requests only <code>channels:history</code>, <code>groups:history</code>, <code>chat:write</code>, <code>reactions:read</code> and <code>pins:write</code>, and turns on Interactivity for the checklist checkboxes. Socket Mode delivers the clicks, so no request URL is needed.</p>
      </li>
      <li>
        <h3>Get the two tokens</h3>
        <p>Under <b>Basic Information → App-Level Tokens</b>, generate a token with the scope <code>connections:write</code>. That <code>xapp-…</code> token is <code>SLACK_APP_TOKEN</code>.</p>
        <p>Under <b>Install App</b>, install the app to your workspace and copy the <b>Bot User OAuth Token</b>. That <code>xoxb-…</code> token is <code>SLACK_BOT_TOKEN</code>.</p>
      </li>
      <li>
        <h3>Prepare the channel</h3>
        <p>Create a channel such as <code>#itakeit</code> (public or private), invite the bot with <code>/invite @itakeit</code>, and copy the channel ID from <b>channel name → About</b>. It looks like <code>C0123456789</code>.</p>
      </li>
      <li>
        <h3>Write config.yaml</h3>
        <p>Two lines are enough. Every other setting has a default. Emoji, reminder interval, board size and <code>status_claims</code> (let anyone set a status without claiming first) are documented in <a href="{repo}/blob/main/config.example.yaml">config.example.yaml</a>.</p>
        <Code code={config} label="config.yaml" />
        <p>The container runs as uid 65532, so the file must be readable by others: <code>chmod 644 config.yaml</code>.</p>
      </li>
      <li>
        <h3>Run the container</h3>
        <p>Export both tokens in your shell, then start the container from the directory that holds <code>config.yaml</code>.</p>
        <Code code={'export SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-...'} label="shell" />
        <Code code={run} label="shell" />
        <details>
          <summary>Prefer Docker Compose?</summary>
          <p>Put the tokens in a <code>.env</code> file next to it and run <code>docker compose up -d</code>.</p>
          <Code code={compose} label="compose.yaml" />
        </details>
        <p>The log shows <code>authenticated</code> and <code>connected to slack</code>, and a board appears pinned in the channel. Post a message and react 🙋.</p>
      </li>
    </ol>

    <div class="note">
      <b>Run exactly one instance per channel.</b> The container makes only outbound connections and needs no port. State lives in the <code>itakeit-data</code> volume, so it survives restarts and upgrades.
    </div>
  </section>

  <section id="usage" class="band">
    <div class="wrap">
      <h2>Usage</h2>
      <p class="sub">Reactions count only on the task message itself. Skin tone variants count as the base emoji.</p>
      <div class="table">
        <table>
          <thead><tr><th>Who</th><th>Does</th><th>Effect</th></tr></thead>
          <tbody>
            {#each usage as [who, does, effect] (does)}
              <tr><td>{who}</td><td>{does}</td><td>{effect}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  </section>
</main>

<footer>
  <div class="wrap row">
    <span><img src="./turtle.png" alt="" /> itakeit</span>
    <div>Need more? <a href="https://betimation.com">Betimation</a> is advanced and fun team task tracking.</div>
    <div>built by <a href="https://nice.pink">nice-pink</a></div>
  </div>
</footer>

<style>
  .wrap { max-width: 1120px; margin: 0 auto; padding: 0 16px; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
  .small { font-size: 0.85rem; color: var(--muted); }
  .center { text-align: center; }

  .nav { position: sticky; top: 0; z-index: 10; background: color-mix(in srgb, var(--bg) 92%, transparent); backdrop-filter: blur(6px); border-bottom: 2px solid var(--ink); }
  .nav .row { min-height: 60px; }
  .brand { display: flex; align-items: center; gap: 0.5rem; font: 700 1.15rem var(--mono); color: var(--ink); text-decoration: none; }
  .brand img, footer img { width: auto; height: 34px; image-rendering: pixelated; vertical-align: middle; }
  nav { display: flex; gap: 1.1rem; flex-wrap: wrap; }
  nav a { color: var(--ink); text-decoration: none; font-weight: 600; font-size: 0.95rem; }
  nav a:hover { color: var(--green); }

  .hero { display: grid; grid-template-columns: 1.05fr 1fr; gap: 3rem; align-items: center; padding-top: 3rem; padding-bottom: 4rem; }
  .turtle { width: 170px; height: auto; image-rendering: pixelated; margin: 0 0 1rem -8px; }
  h1 { font: 800 clamp(2.3rem, 5.5vw, 3.8rem)/1.05 var(--sans); letter-spacing: -0.03em; margin: 0 0 1rem; }
  h1 span { color: var(--green); display: block; }
  .lead { font-size: 1.15rem; color: var(--muted); margin: 0 0 1.5rem; max-width: 34rem; }
  .cta { display: flex; gap: 0.8rem; flex-wrap: wrap; margin-bottom: 0.8rem; }
  .btn { display: inline-block; padding: 0.75rem 1.3rem; font-weight: 700; text-decoration: none; background: var(--green); color: #fff; border: 2px solid var(--ink); box-shadow: 4px 4px 0 var(--ink); transition: transform 0.1s, box-shadow 0.1s; }
  .btn:hover { transform: translate(-2px, -2px); box-shadow: 6px 6px 0 var(--ink); }
  .btn:active { transform: translate(2px, 2px); box-shadow: 2px 2px 0 var(--ink); }
  .btn.ghost { background: var(--panel); color: var(--ink); }
  .demo { min-width: 0; }

  h2 { font: 800 clamp(1.8rem, 4vw, 2.5rem)/1.1 var(--sans); letter-spacing: -0.02em; margin: 0 0 0.4rem; }
  .sub { color: var(--muted); margin: 0 0 2rem; }
  section { scroll-margin-top: 70px; }

  .band { background: var(--green-soft); border-top: 2px solid var(--ink); border-bottom: 2px solid var(--ink); padding: 4rem 0; }
  .grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1.2rem; }
  .card { background: var(--panel); border: 2px solid var(--ink); box-shadow: 4px 4px 0 var(--ink); padding: 1.2rem 1.3rem; }
  .card h3 { margin: 0.4rem 0 0.3rem; font-size: 1.1rem; }
  .card p { margin: 0; color: var(--muted); font-size: 0.95rem; }
  .icon { font-size: 1.7rem; }

  .setup { padding-top: 4rem; padding-bottom: 4rem; }
  .steps { list-style: none; counter-reset: step; padding: 0; margin: 0; max-width: 820px; }
  .steps > li { counter-increment: step; position: relative; padding: 0 0 1.5rem 3.6rem; border-left: 2px dashed var(--line); margin-left: 1.2rem; }
  .steps > li:last-child { border-left-color: transparent; }
  .steps > li::before { content: counter(step); position: absolute; left: -1.25rem; top: -0.2rem; width: 2.5rem; height: 2.5rem; display: grid; place-items: center; font: 700 1.1rem var(--mono); background: var(--green); color: #fff; border: 2px solid var(--ink); box-shadow: 3px 3px 0 var(--ink); }
  .steps h3 { margin: 0 0 0.4rem; font-size: 1.2rem; }
  .steps p { margin: 0 0 0.6rem; }
  .steps > li { min-width: 0; }
  details { margin: 0.4rem 0 0.8rem; }
  summary { cursor: pointer; font-weight: 600; color: var(--green-dark); }
  .note { max-width: 820px; background: #fff3e6; border: 2px solid var(--ink); border-left: 8px solid var(--orange); padding: 1rem 1.2rem; }

  .table { overflow-x: auto; background: var(--panel); border: 2px solid var(--ink); box-shadow: 4px 4px 0 var(--ink); }
  table { width: 100%; border-collapse: collapse; font-size: 0.95rem; }
  th, td { text-align: left; padding: 0.65rem 0.9rem; border-bottom: 1px solid var(--line); vertical-align: top; }
  th { background: var(--ink); color: #fff; font-weight: 600; }
  td:first-child { font-family: var(--mono); font-size: 0.85rem; white-space: nowrap; color: var(--green-dark); }
  tr:last-child td { border-bottom: 0; }


  footer { border-top: 2px solid var(--ink); padding: 1.2rem 0; font-size: 0.9rem; }
  footer span { font: 700 1rem var(--mono); }

  @media (max-width: 860px) {
    .hero { grid-template-columns: 1fr; gap: 2rem; padding-top: 1.5rem; }
    .turtle { width: 120px; }
    .grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    nav { gap: 0.8rem; }
    nav a { font-size: 0.85rem; }
  }
  @media (max-width: 520px) {
    nav a:not(:last-child):not([href="#setup"]) { display: none; }
    .steps > li { padding-left: 2.2rem; margin-left: 1.2rem; }
    .grid { grid-template-columns: minmax(0, 1fr); }
  }
</style>
