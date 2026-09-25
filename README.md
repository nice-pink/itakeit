<p align="center"><img src="assets/pixel_turtle.png" alt="itakeit pixel turtle" width="200"></p>

# I take it

Task tracking in Slack threads. Dead simple.

A self-hosted Slack bot that turns every message in one dedicated channel into a task. People claim it and set its status with reactions. Homepage: [itakeit.nice.pink](https://itakeit.nice.pink).

- Post an issue in the channel and it becomes a task. The bot replies in its thread with a status card.
- React 🙋 (`:raising_hand:`) on the issue to take it. Several people can own one task. Remove the reaction to hand it back.
- Owners set the status with reactions: 👀 investigating, 🚧 in progress, ❓ needs info, ⛔ blocked, ✅ done.
- ❓ pings the reporter in the thread. When the reporter replies, the bot pings the owners and clears the status.
- A pinned board message in the channel lists every open task with its status and owners.
- Owners who stay silent on a task they are working on get a reminder in the thread, repeated every `stale_after_hours` until an owner posts in the thread or the status changes.

It uses Socket Mode, so it needs no public URL, ingress or TLS. It runs as a single process with a SQLite file.

## Setup

### 1. Create the Slack app

1. Go to https://api.slack.com/apps, choose **Create New App**, then **From an app manifest**, and pick your workspace.
2. Paste the contents of `slack-app-manifest.yaml` (YAML tab) and create the app.
3. Under **Basic Information → App-Level Tokens**, choose **Generate Token and Scopes**, name it `socket`, add the scope `connections:write` and generate it. Copy the `xapp-...` token. This is `SLACK_APP_TOKEN`.
4. Under **Install App**, install the app to the workspace. Copy the **Bot User OAuth Token** (`xoxb-...`). This is `SLACK_BOT_TOKEN`.

The manifest requests these bot scopes:

| Scope | Used for |
|---|---|
| `channels:history`, `groups:history` | receiving messages in a public or private channel, and looking up messages posted before the bot was running |
| `chat:write` | status cards, the board, pings, private "only owners can…" hints |
| `reactions:read` | receiving reaction events |
| `pins:write` | pinning the board |

If you change scopes later, reinstall the app so they take effect.

### 2. Prepare the channel

1. Create the channel, e.g. `#itakeit`. Public or private both work.
2. Invite the bot: `/invite @itakeit`. It only sees channels it is a member of.
3. Copy the channel ID: open the channel name, then **About**, and find it at the bottom (`C0123456789`).

### 3. Configure

```
cp config.example.yaml config.yaml
```

Set `channel` to the ID from step 2. Every other field has a default, and all fields are explained in the example file. Tokens come only from the environment and never go into the config file.

### 4. Run locally

```
./build && SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-... ./bin/itakeit -config config.yaml
```

The log should show `authenticated` and then `connected to slack`, and a board message appears pinned in the channel. `-debug` logs the raw Socket Mode traffic.

### 5. Run in Docker

Pull the published image from GitHub Container Registry:

```
docker run -d --name itakeit --restart unless-stopped -e SLACK_BOT_TOKEN -e SLACK_APP_TOKEN -v "$PWD/config.yaml:/config/config.yaml:ro" -v itakeit-data:/data ghcr.io/nice-pink/itakeit:latest
```

`latest` follows `main`. Release tags `vX.Y.Z` also publish `X.Y.Z` and `X.Y`, and every build publishes `sha-<short>`. Images are built for linux/amd64 and linux/arm64.

Or build it yourself:

```
docker build -t itakeit . && docker run -d --name itakeit --restart unless-stopped -e SLACK_BOT_TOKEN -e SLACK_APP_TOKEN -v "$PWD/config.yaml:/config/config.yaml:ro" -v itakeit-data:/data itakeit
```

Set `db_path: /data/itakeit.db` in `config.yaml` so state survives container restarts. The runtime image runs as uid 65532 and creates `/data` owned by that user, so the named volume above is writable. If you bind-mount a host directory instead, make it writable for uid 65532. The bot makes only outbound connections, so it needs no port.

Run **exactly one instance** per channel. Two instances would both reply to every event and post duplicate cards.

## Usage

| Who | Does | Effect |
|---|---|---|
| anyone | posts a top-level message in the channel | new task, status card in its thread, board updated |
| anyone | reacts 🙋 on the task message | becomes an owner |
| owner | removes 🙋 | stops owning it. When the last owner leaves, the status resets to unclaimed, except that a done task stays done. |
| owner | reacts 👀 / 🚧 / ⛔ / ✅ | sets the status. The most recent reaction wins. ✅ removes the task from the board. |
| owner | removes the reaction for the current status | status falls back to claimed |
| owner | reacts ❓ | reporter is pinged in the thread |
| reporter | replies in the thread while ❓ is set | owners are pinged and the status falls back to claimed |
| owner | replies in the thread | counts as activity and resets the reminder clock |
| non-owner | reacts with a status emoji | ignored, and the user gets a private hint to claim first |

Reactions only count on the task message itself. Reactions on thread replies, the card or the board are ignored. Skin tone variants count as the base emoji.

Messages posted while the bot was offline are not lost. The first 🙋 or status reaction on such a message turns it into a task, but only 🙋 makes the reactor an owner. Reactions *removed* while the bot was offline are not replayed. Remove the emoji and add it again to resync.

Editing the task message updates its line on the board. Deleting it removes the task and its status card.

To reopen a done task that has no owners left, claim it with 🙋 and then set any other status.

Messages from integrations and webhooks count as tasks too, with the integration's name as the reporter. The bot can't @-mention them, so ❓ posts their name as plain text.

### Adapting it

- **Different emoji:** edit the `emoji` block. Custom workspace emoji work (e.g. `claim: [itakeit]`), and each action can list several emoji. If the block is present it replaces the defaults, so list every action you want.
- **Disabling an action:** leave it out of `emoji`. `claim` is required.
- **Reminders:** raise or lower `stale_after_hours`. Tasks that are blocked or waiting for info are never nudged.
- **Database cleanup:** set `done_retain_days` to delete done tasks from SQLite after that many days without activity, checked hourly. Defaults to 30. Set `-1` to keep them forever. Messages and cards stay in Slack, but a swept task is frozen: reactions on it are ignored, deleting it leaves its card, and it can't be reopened. Messages older than `done_retain_days` are also never adopted, so a message missed while the bot was offline that long is not picked up.

## How it works

```
cmd/itakeit     startup: config, tokens, auth test, Socket Mode client
pkg/config      YAML config, reaction -> action lookup
pkg/task        task model, state transitions, card/board rendering (pure, no I/O)
pkg/bot         Socket Mode event loop and Slack calls
pkg/store       SQLite persistence
```

- One goroutine handles every event and the reminder timer, so there is no locking in the application.
- A separate goroutine acknowledges each event the moment it arrives, so a slow Slack API call never delays an ack. Events still queued when the bot stops or crashes are not redelivered. Re-adding the reaction repairs it. A redelivered message never resets a task that already exists.
- Slack API calls time out after 15 s and are retried up to 3 times on rate limits.
- The board is posted as a placeholder and filled by an edit, because edits don't notify anyone, and reposting the board would otherwise ping every owner.
- The channel is the record of what was said. SQLite holds only owners, statuses, timestamps and the board message ID. Deleting the database loses claims and statuses but no conversation. The next start posts a fresh board, and tasks come back as people react.
- If someone deletes the board or a status card, the bot posts and pins a new one on the next change.

## Development

```
./build
```

This runs `go test ./...` and builds `bin/itakeit`. The bot tests drive the real event handlers against a fake Slack API and a temporary SQLite file.
