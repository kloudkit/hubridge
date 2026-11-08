package doctor

const SeenIn = "2.1.318"

var EntryTypes = set(
	"user", "assistant", "system", "attachment", "summary",
	"ai-title", "custom-title", "agent-name", "last-prompt",
	"pr-link", "continued-in", "cost-state", "queue-operation",
	"permission-mode", "mode", "session-owner", "tools-used", "atis-latch",
	"file-history-snapshot", "file-history-delta",
	"api-request", "api-request-blob", "api-request-shape",
	"frame-link", "fork-context-ref",
)

var SystemSubtypes = set(
	"turn_duration", "compact_boundary", "local_command", "informational",
	"away_summary", "api_error", "model_refusal_fallback", "model_refusal_no_fallback",
)

var UsageFields = []string{
	"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
	"cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens",
	"server_tool_use.web_search_requests", "server_tool_use.web_fetch_requests", "speed",
}

var LedgerFields = []string{
	"totalCostUSD", "totalLinesAdded", "totalLinesRemoved", "totalAPIDuration", "totalToolDuration", "modelUsage",
}

var RegistryFields = []string{
	"pid", "sessionId", "cwd", "startedAt", "procStart", "version", "kind", "entrypoint",
	"pidDomain", "name", "status", "updatedAt", "statusUpdatedAt",
}

var AgentFields = []string{"cwd", "kind", "startedAt"}

var JobStates = set("working", "blocked", "done", "failed", "stopped")

var WaitingFor = set(
	"permission prompt", "input needed", "sandbox request", "worker request", "dialog open", "goal proposal",
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
