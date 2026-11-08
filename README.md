# KloudKIT • hubridge

> 🌉 Bridges a machine's Claude Code sessions to hubd

hubridge watches Claude Code on one machine and streams its sessions, live state and usage
to [hubd](https://github.com/kloudkit/hubd). It also adds hubd's hooks and MCP server to
Claude Code. It speaks hubd's client protocol, whose contract lives in
[kloudkit/hubd](https://github.com/kloudkit/hubd/tree/main/protocol).

## Install

hubridge is installed by the hubd it connects to. Settings › Access makes a one-time code
and shows the command:

```sh
curl -fsSL '<hubd>/install?code=<code>' | sh                              # hubd's bundled build
curl -fsSL '<hubd>/install?code=<code>' | sh -s -- --version latest       # the newest GitHub release
curl -fsSL '<hubd>/install?code=<code>' | sh -s -- --version v0.1.0       # one GitHub release
```

The installer needs `sh`, `curl` or `wget`, and `sha256sum` or `shasum`. It always installs
hubd's bundled build first, so a machine that reaches only hubd can install:

1. downloads `hubridge-<os>-<arch>` from hubd and checks it against hubd's `checksums.txt`;
2. installs it as `<home>/bin/hubridge`, keeping the previous one as `hubridge.prev`;
3. runs `hubridge enroll` with the code (or `--token-file`, `HUBD_TOKEN`, else the installed
   token);
4. runs `hubridge update`, with `--version` when given;
5. runs `hubridge setup` and `hubridge supervise --detach`.

Rerunning it with no code keeps the installed token and the saved channel. Options:
`--home <dir>`, `--machine <name>`, `--token-file <path>`, `--version <channel>`.

## Channels and updates

The channel is where hubridge takes its updates from. It is saved in `config.json`, sent to
hubd in `hello`, and printed by `hubridge version`.

| Channel | Source | Updates itself |
|---|---|---|
| `bundled` (default) | hubd's `/install` | follows hubd's bundled build up and down |
| `latest` | the newest GitHub release | only to a newer release |
| `v0.1.0`, any tag | that GitHub release | never |

`hubridge update` installs the newest build of the saved channel; `--version <channel>`
switches to another channel and saves it. Before installing anything, hubridge:

- checks the release's `manifest.json` against `manifest.json.sig` with the ed25519 key built
  into it;
- stops when hubd's `GET /api/client/hello` speaks another protocol than the release;
- checks the binary's sha256 against the manifest.

Outside a pinned tag, the daemon also updates itself at start, every 6 hours, and right after
hubd refuses it. A failed check is logged and retried; `HUBRIDGE_NO_AUTOUPDATE=1` turns it
off, and a `dev` build never updates itself. The supervisor restarts on a new binary within
15 seconds, and rolls back to `hubridge.prev` after 3 crashes in 5 minutes.

## Commands

| Command | Does |
|---|---|
| `run` | watch transcripts and stream them to hubd (the default) |
| `enroll` | `--url <hubd>` with `--code <code>` or `--token-file <path>`, and `--machine <name>` |
| `setup` | add the hooks and the `hubd` MCP server to Claude Code, and wrap its status line; `--remove` undoes it |
| `hook` | `session-start`, `notification` or `stop`, run by Claude Code |
| `supervise` | the restart loop for `run`; `--detach` backgrounds it |
| `mcp-headers` | print the `Authorization` header for Claude Code's MCP `headersHelper` |
| `statusline` | run by Claude Code as its status line: keep the usage reading, then run the original |
| `doctor` | check the Claude Code formats hubridge reads against the pinned ones (`--days 7`) |
| `update` | install the newest build of the saved channel; `--version` switches channel |
| `version` | print the version and the channel |

Every command takes `--home <dir>`.

`setup` merges SessionStart, Notification and Stop hooks into Claude Code's `settings.json`
(honouring `CLAUDE_CONFIG_DIR`) and registers a user-scope MCP server `hubd` through
`claude mcp add-json`. Its `headersHelper` runs `hubridge mcp-headers`, so no token is
written to Claude's config.

## Client home

One folder: `--home`, else `HUBD_HOME`, else `~/.config/hubd`. Mode 0700, files 0600.

| File | Holds |
|---|---|
| `token` | the token, one line |
| `config.json` | `{ "url": "https://<hubd>", "machine": "<name>", "channel": "bundled" }` |
| `bin/hubridge`, `bin/hubridge.prev` | the installed binary and the one before it |
| `sessions/<session id>` | `{ "pid": <claude pid> }`, written by the SessionStart hook; the daemon adds `"started"` (the process start time, epoch ms, as it reads it) on first read |
| `queue/` | unacknowledged messages, one file each |
| `usage.json` | the latest usage reading, from the status line or the probe |
| `statusline.json` | the original `statusLine` object while hubridge wraps it; removed by `setup --remove` |
| `supervisor.lock`, `daemon.lock` | advisory `flock` locks; the kernel releases them when the process dies |

## Usage readings

When Claude Code's settings have a `statusLine` of type `command`, `setup` replaces its
command with `hubridge statusline --home <home>` and keeps the original object in
`statusline.json`. The wrapper reads the status-line JSON from stdin, writes its
`rate_limits` (percent, divided by 100) to `usage.json` when they change or the last reading
is a minute old, then runs the original command through `sh -c` with the same stdin and
passes its output and exit code through. With no status line configured, none is added. The
daemon checks the setting at start and every 10 minutes, and wraps it again when another
tool rewrote it.

While a session is live and the last reading is over 15 minutes old, the daemon probes every
5 minutes: `claude -p "reply ok" --output-format stream-json --verbose --model haiku
--no-session-persistence --setting-sources project --strict-mcp-config`, run in the client
home, so no hook or MCP server loads, and reads the `rate_limit_event`. Every new reading is
sent to hubd as a `usage` message.

What hubridge reads from Claude Code, and whether Claude Code documents it, is in
[docs/claude-formats.md](docs/claude-formats.md).

## Develop and release

```sh
go test ./...
scripts/protocol-check.sh   # protocol/client.schema.json against the tag in protocol/TAG
```

A `v*` tag releases: CI checks the tag's CHANGELOG section, GoReleaser builds a draft release,
`go run ./tools/release manifest` writes and signs `manifest.json` with the
`KEY__HUBRIDGE_RELEASE` secret, and the release is published as the latest.
