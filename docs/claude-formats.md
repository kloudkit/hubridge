# Claude Code formats the daemon reads

What the daemon reads from Claude Code, whether Claude Code documents it, and the version it
was last checked against. `internal/formats` holds the same lists, and
`hubridge doctor` compares them with the files on the machine: anything new or missing is
reported as drift. Pinned against Claude Code 2.1.318.

## Documented

| Source | What the daemon takes |
|---|---|
| `claude agents --json --all` | `sessionId`, `pid`, `kind` (`interactive`, `background`), `name`, `cwd`, `startedAt`; `status` (`busy`, `waiting`, `idle`) while the process lives; `waitingFor` (`permission prompt`, `input needed`, `sandbox request`, `worker request`, `dialog open`) while it waits; a background session's `id` and `state` (`working`, `blocked`, `done`, `failed`, `stopped`). The docs call it "the supported way to read session state from outside Claude Code". |
| `~/.claude/sessions/` | One file per running session, "to detect concurrent sessions and crashes". The folder is documented; the fields below are not. |

## Internal

These can change in any release. The daemon reads them only where nothing documented gives
the same thing, and `doctor` watches them.

| Source | Fields | Used for |
|---|---|---|
| `~/.claude/sessions/<pid>.json` | `pid`, `sessionId`, `procStart` (field 22 of `/proc/<pid>/stat`), `pidDomain` (`linux:<machine-id>:pid:[<inode>]`), `status` (`busy`, `shell`, `waiting`, `idle`; `shell` is a background shell still running and `--json` reports it as `busy`), `kind`, `entrypoint`, `name`, `version`, `cwd` | Liveness and status between `--json` runs. The file has no heartbeat and outlives a crash, so it counts only with the pid, `procStart` and `pidDomain` checks. |
| Transcript `assistant` | `message.id` with `requestId` (one reply, repeated per content block), `message.model` (`<synthetic>` is skipped), `message.usage`: `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, `cache_creation.ephemeral_5m_input_tokens` and `ephemeral_1h_input_tokens`, `server_tool_use.web_search_requests` and `web_fetch_requests`, `speed`; `requestedModel`; `isSidechain`; `isApiErrorMessage` with `quotaLimits` (`rateLimitType`, `resetsAt` in epoch seconds, `status`) | Tokens per day, hour and model; context fill; limit hits |
| Transcript `cost-state` | `totalCostUSD`, `totalLinesAdded`, `totalLinesRemoved`, `totalAPIDuration`, `totalToolDuration`, `modelUsage.<model>` (`inputTokens`, `outputTokens`, `cacheReadInputTokens`, `cacheCreationEphemeral5mInputTokens`, `cacheCreationEphemeral1hInputTokens`, `webSearchRequests`, `costUSD`) | Claude Code's own ledger, written when a run ends. It also counts compaction and other calls that write no reply, so it runs a few percent above the replies, and trails a session that went on. |
| Transcript `system` | `subtype` `turn_duration` (a turn ended), `compact_boundary` with `compactMetadata` (`trigger`, `preTokens`, `postTokens`) | Turn end without a registry; compactions and the context size after one |
| Transcript names and links | `ai-title.aiTitle`, `custom-title.customTitle`, `agent-name.agentName`, `last-prompt.lastPrompt`, `pr-link` (`prNumber`, `prUrl`, `prRepository`), `continued-in.continuedInSessionId`, and `cwd`, `version`, `gitBranch` on any entry | Titles, the excerpt, PR links, where a session moved, the folder, version and branch |
| Transcript files | `<project>/<session>.jsonl`; subagents under `<project>/<session>/subagents/`, workflows included, at any depth; `journal.jsonl` is skipped | Which session a file belongs to |

Entry types the daemon skips without decoding: `attachment`, `queue-operation`,
`permission-mode`, `mode`, `session-owner`, `tools-used`, `atis-latch`,
`file-history-snapshot`, `file-history-delta`, `api-request`, `api-request-blob`,
`api-request-shape`, `frame-link`, `fork-context-ref`, `summary`. A type outside both lists is
drift.

## When `doctor` reports drift

Run `hubridge doctor` after a Claude Code update. Drift names the field or value. Check it
against the CHANGELOG and the files, update `internal/formats` and this page, and
change the reader if the meaning moved.
