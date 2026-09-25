# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

itakeit is a Slack bot (Go, Socket Mode, SQLite via pure-Go `modernc.org/sqlite`, no cgo) that turns top-level messages in one channel into tasks claimed and status-tracked with reactions. User-facing behaviour and setup are in `README.md`; keep its Usage table in sync when behaviour changes.

## Reactions are the interface

Claims and statuses are set only by reactions on the task message, so anyone scanning the channel sees who owns what and where it stands without opening a thread. Do not add another way to claim or change a status (card buttons, selects, message shortcuts, slash commands, modals): each one changes state without leaving a reaction, and the channel stops showing the truth. Having the bot add reactions itself doesn't fix it either, because they show the bot as the reactor, not the owner. Card controls and message shortcuts were built and reverted for this reason, and bot-mirrored reactions were declined. Card interactivity is fine for things that are not claim or status, such as checklist ticks.

## Commands

- Test and build: `./build` (runs `go test ./...`, then builds `bin/itakeit`). The Dockerfile runs the same script, so a failing test fails the image build.
- Single test: `go test ./pkg/bot -run TestLifecycle -v`
- Run: `SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-... ./bin/itakeit -config config.yaml` (`-debug` logs raw Socket Mode traffic). Tokens come only from the environment; `config.yaml` is gitignored, `config.example.yaml` is the documented template.

## Architecture

Dependency direction: `cmd/itakeit` → `pkg/bot` → `pkg/store`, `pkg/config` → `pkg/task`.

- `pkg/task` is pure (no I/O). State transitions (`React`, `Reply`, `Stale`) mutate the task and return an `Effect`; `pkg/bot` turns the effect into Slack calls (re-render, ephemeral denial, ping reporter, ping owners). New behaviour goes in as a transition plus an effect, with the Slack side in `bot`. Card and board text rendering also lives here (`render.go`).
- `pkg/bot` runs every event and the reminder ticker on one goroutine, which is why nothing in the app locks. A separate `ackLoop` goroutine acks envelopes immediately and queues them (buffer 1024, drops when full). Do not add goroutines that touch the store or tasks.
- Slack is reached only through the `bot.API` interface. Adding a Slack call means extending that interface and `fakeAPI` in `pkg/bot/bot_test.go`.
- `pkg/store` holds only owners, statuses, timestamps, checklist ticks and the board message ts (in `kv`). The channel is the source of truth for text; missed messages are recovered lazily by `adopt` via conversation history when someone reacts.

Gotchas:
- The schema is `CREATE TABLE IF NOT EXISTS` with no migrations, and `Save` inserts positionally (`VALUES (?,?,...)`). Adding a column breaks existing databases and depends on column order.
- The board is posted as a placeholder and then filled by `UpdateMessage`, because a fresh post would notify every mentioned owner. Keep it that way.
- Cards and the board self-heal: an `UpdateMessage` error whose text is exactly `message_not_found` triggers a repost; any other error is only logged.
- A deleted task message arrives as `message_changed` with a `tombstone` subtype (it has the card as a reply), not as `message_deleted`. Both paths call `onDelete`.
- Reactions count only on the root task message, and the bot ignores its own messages by both user ID and bot ID. Skin-tone suffixes (`::skin-tone-N`) are stripped in `config.Action`.
- A present `emoji` block in config replaces the defaults entirely; `claim` is required and an emoji may map to only one action.
- Checklist items come from the task text; card ticks live in the `checks` table keyed by a hash of item text plus occurrence. Each item is its own single-option checkbox element (block ID `check:<ts>:<key>`), because a checkbox click reports the whole ticked set of its element and a multi-option group would let a stale view untick other people's items. Keep it one item per element. If Slack rejects the blocks (`invalid_blocks`), the card falls back to text only; other errors do not.

## Tests

Bot tests drive `Bot.Handle` with synthetic `slackevents` against `fakeAPI` (records calls, hands out sequential `900.N` timestamps, `missing` simulates deleted messages) and a temp SQLite file. Use `setup(t)`, `msg(...)` and `react(...)`; control time by overriding `b.now`.
