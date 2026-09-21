package acp

import "encoding/json"

// The ACP v1 subset xbin speaks (schema-v1.23.0). Field names are the
// wire's camelCase; enum strings are snake_case as the protocol defines.

const ProtocolVersion = 1

// Method names (schema/v1/meta.json).
const (
	MInitialize        = "initialize"
	MAuthenticate      = "authenticate"
	MSessionNew        = "session/new"
	MSessionPrompt     = "session/prompt"
	MSessionCancel     = "session/cancel"
	MSessionSetMode    = "session/set_mode"
	MSessionSetConfig  = "session/set_config_option"
	MSessionUpdate     = "session/update"
	MRequestPermission = "session/request_permission"
	MCancelRequest     = "$/cancel_request"
	MFsRead            = "fs/read_text_file"
	MFsWrite           = "fs/write_text_file"
	MTermCreate        = "terminal/create"
	MTermOutput        = "terminal/output"
	MTermWait          = "terminal/wait_for_exit"
	MTermKill          = "terminal/kill"
	MTermRelease       = "terminal/release"
	// xbin's own, daemon ⇄ host (never reaches the agent):
	MXbinSpawn = "_xbin/spawn"
	MXbinHello = "_xbin/hello"
	MXbinLog   = "_xbin/log"
)

type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Info              `json:"clientInfo,omitempty"`
}

type ClientCapabilities struct {
	FS       FSCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
}

type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod    `json:"authMethods,omitempty"`
	AgentInfo         *Info           `json:"agentInfo,omitempty"`
	Meta              map[string]any  `json:"_meta,omitempty"`
	Extra             map[string]any  `json:"-"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // "" (agent) | "terminal"
}

type SessionNewParams struct {
	Cwd        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type SessionNewResult struct {
	SessionID     string         `json:"sessionId"`
	Modes         *SessionModes  `json:"modes,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// ConfigOption is one session setting the agent exposes (model, effort,
// permission mode, …): a select with the current value and its choices.
// Set with session/set_config_option; config_option_update carries the
// refreshed list.
type ConfigOption struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	Category     string        `json:"category,omitempty"` // mode | model | thought_level | model_config | …
	Type         string        `json:"type"`               // select
	CurrentValue string        `json:"currentValue"`
	Options      []ConfigValue `json:"options,omitempty"`
}

type ConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SetConfigParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type SetConfigResult struct {
	ConfigOptions []ConfigOption `json:"configOptions"`
}

type ConfigOptionUpdate struct {
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// SessionInfoUpdate: the agent's own title for the session (auto-generated
// by most adapters after the first turn) and a bump time.
type SessionInfoUpdate struct {
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type SessionModes struct {
	CurrentModeID  string      `json:"currentModeId"`
	AvailableModes []ModeEntry `json:"availableModes"`
}

type ModeEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"` // text | image | audio | resource_link | resource
	Text string `json:"text,omitempty"`
	// the rest is passed through untouched when we relay a block
	Rest json.RawMessage `json:"-"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type PromptResult struct {
	StopReason string `json:"stopReason"` // end_turn | max_tokens | max_turn_requests | refusal | cancelled
}

type SessionIDParams struct {
	SessionID string `json:"sessionId"`
}

type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SessionUpdate is the params of session/update; Update carries the
// variant in sessionUpdate plus that variant's fields.
type SessionUpdate struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// Update variants (sessionUpdate values).
const (
	UpUserChunk      = "user_message_chunk"
	UpAgentChunk     = "agent_message_chunk"
	UpThoughtChunk   = "agent_thought_chunk"
	UpToolCall       = "tool_call"
	UpToolCallUpdate = "tool_call_update"
	UpPlan           = "plan"
	UpAvailableCmds  = "available_commands_update"
	UpCurrentMode    = "current_mode_update"
	UpConfigOption   = "config_option_update"
	UpSessionInfo    = "session_info_update"
	UpUsage          = "usage_update"
)

type UpdateEnvelope struct {
	SessionUpdate string `json:"sessionUpdate"`
}

type ChunkUpdate struct {
	Content   ContentBlock `json:"content"`
	MessageID string       `json:"messageId,omitempty"`
}

type ToolCallUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title,omitempty"`
	Kind       *string         `json:"kind,omitempty"`
	Status     *string         `json:"status,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Locations  json.RawMessage `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
}

type PlanUpdate struct {
	Entries json.RawMessage `json:"entries"`
}

type CurrentModeUpdate struct {
	CurrentModeID string `json:"currentModeId"`
}

type UsageUpdate struct {
	Used uint64          `json:"used"`
	Size uint64          `json:"size"`
	Cost json.RawMessage `json:"cost,omitempty"`
}

type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // selected | cancelled
	OptionID string `json:"optionId,omitempty"`
}

type CancelRequestParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

// Client-side (host-served) methods.

type FsReadParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type FsReadResult struct {
	Content string `json:"content"`
}

type FsWriteParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type TermCreateParams struct {
	SessionID       string   `json:"sessionId"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Env             []EnvVar `json:"env,omitempty"`
	Cwd             string   `json:"cwd,omitempty"`
	OutputByteLimit *int64   `json:"outputByteLimit,omitempty"`
}

type TermCreateResult struct {
	TerminalID string `json:"terminalId"`
}

type TermIDParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type ExitStatus struct {
	ExitCode *int    `json:"exitCode"`
	Signal   *string `json:"signal"`
}

type TermOutputResult struct {
	Output     string      `json:"output"`
	Truncated  bool        `json:"truncated"`
	ExitStatus *ExitStatus `json:"exitStatus,omitempty"`
}

// xbin's daemon → host first frame: what to run.
type SpawnParams struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Cwd  string   `json:"cwd"`
}

type HelloParams struct {
	Version string `json:"version"`
}

type LogParams struct {
	Text string `json:"text"`
}
