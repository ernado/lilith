# lilith

Lilith is a Telegram bot that participates in group and private chats like a
regular member: it reads the conversation, keeps a running memory of each chat,
understands images and links, and replies in character via a large language
model. It is built on MTProto ([gotd](https://github.com/gotd/td)) and
authenticates with a bot token, with all state persisted in PostgreSQL.

## Features

- **Conversational replies** — answers when mentioned (`лилит`, `лиля`,
  `лилия`, or the bot's `@username`), when someone replies to it, in private
  chats, and occasionally on its own (~5% of messages).
- **Per-chat memory** — maintains a rolling set of notes summarizing each chat,
  used to ground replies. The last 150 messages are passed as context.
- **Conversation threading** — groups messages into logical threads so replies
  stay on-topic.
- **Idle messages** — writes unprompted after a random 30m–2h of inactivity.
- **Vision** — sees images sent to the chat. Images are hosted via a built-in
  static file server, and the most recent images are kept (older ones are
  elided).
- **Link understanding** — fetches and extracts the content of shared links via
  [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr) (handles JS
  rendering and bot-protection challenges).
- **Tools** — weather lookup, web search, and emoji reactions.
- **Commands** — `/start`, `/lobotomy` (clear a chat's memory), `/delete`
  (reply to a message to remove it from memory and the chat), `/model` (show or
  set the per-chat model).

## Architecture

The project follows an MVC-style layout (see [CLAUDE.md](CLAUDE.md)):

- **Models / contracts** live in the root package (`lilith.go`, `ai.go`,
  `db.go`, `filestore.go`, `scraper.go`, `weather.go`, `memory.go`). Cross-layer
  communication goes through these interfaces only.
- **Services** live under `internal/`:
  - `bot` — Telegram transport and message-handling orchestration.
  - `ai` — the LLM gateway (OpenRouter), prompt assembly, and the tool-call loop.
  - `db` — PostgreSQL persistence (squirrel + pgx). Schema in
    [internal/db/SCHEMA.md](internal/db/SCHEMA.md).
  - `memory` — note maintenance/summarization.
  - `scraper` — FlareSolverr-backed page fetching.
  - `static` — disk-backed file server for hosting images.
  - `weather`, `reaction`, `thread`, `prompt` — supporting packages.
- **Entry point** is `cmd/lilith`.

## Prerequisites

- Go 1.26+
- PostgreSQL
- Telegram API credentials (`APP_ID` / `APP_HASH`) and a bot token
- An [OpenRouter](https://openrouter.ai) API key
- Optional: a [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr)
  instance for link scraping
- Optional: a publicly reachable address for the static server (needed for image
  vision, so the model can fetch hosted images)

## Configuration

Configuration is via environment variables.

| Variable          | Required | Default                                                              | Description                                                          |
| ----------------- | -------- | -------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `BOT_TOKEN`       | yes      | —                                                                    | Telegram bot token.                                                 |
| `APP_ID`          | yes      | —                                                                    | Telegram API app ID.                                                |
| `APP_HASH`        | yes      | —                                                                    | Telegram API app hash.                                              |
| `AI_TOKEN`        | yes      | —                                                                    | OpenRouter API key.                                                 |
| `WEATHER_API_KEY` | yes      | —                                                                    | [Weatherstack](https://weatherstack.com) API key (weather tool).    |
| `DATABASE_URL`    | no       | `postgres://postgres:postgres@localhost:5432/lilith?sslmode=disable` | PostgreSQL connection string.                                       |
| `AI_MODEL`        | no       | `deepseek/deepseek-v4-flash`                                         | Default OpenRouter model.                                           |
| `STATIC_ADDR`     | no       | —                                                                    | Listen address for the image server. Set with `STATIC_URL` to enable. |
| `STATIC_URL`      | no       | —                                                                    | Public base URL the model uses to fetch hosted images.              |
| `SCRAPER_ADDR`    | no       | —                                                                    | FlareSolverr endpoint (e.g. `http://127.0.0.1:8191/v1`). Enables link scraping. |

The Telegram session is stored in `session.json` in the working directory.

## Running

Database migrations are embedded and applied automatically on startup.

```bash
# build
go build ./cmd/lilith

# run
go run ./cmd/lilith
```

Flags:

- `-f`, `--force-migration` — resolve a dirty migration state and exit.

## Development

```bash
# run the test suite
go test ./...
```

The `db` package tests spin up a real PostgreSQL via
[testcontainers](https://golang.testcontainers.org/), so a running Docker daemon
is required for them.

When changing the database schema, add a migration under
`internal/db/_migrations/` and update
[internal/db/SCHEMA.md](internal/db/SCHEMA.md).

## License

See [LICENSE](LICENSE).
