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
	MSessionLoad       = "session/load"
	MSessionPrompt     = "session/prompt"
	MSessionCancel     = "session/cancel"
	MSessionSetMode    = "session/set_mode"
	MSessionSetConfig  = "session/set_config_option"
	MSessionUpdate     = "session/update"
	MRequestPermission = "session/request_permission"
	MElicitCreate      = "elicitation/create"
	MElicitComplete    = "elicitation/complete"
	MCancelRequest     = "$/cancel_request"
	MFsRead            = "fs/read_text_file"
	MFsWrite           = "fs/write_text_file"
	MTermCreate        = "terminal/create"
	MTermOutput        = "terminal/output"
	MTermWait          = "terminal/wait_for_exit"
	MTermKill          = "terminal/kill"
	MTermRelease       = "terminal/release"
	// xbin's own, daemon ⇄ host (never reaches the agent):
	MXbinSpawn  = "_xbin/spawn"
	MXbinHello  = "_xbin/hello"
	MXbinLog    = "_xbin/log"
	MXbinAttach = "_xbin/attach" // a prompt's file, dropped inside the sandbox by the host (a request)
	// the adapter's own (claude, codex): pushes the sign-in status
	MAuthStatus = "_auth/status_update"
)

type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Info              `json:"clientInfo,omitempty"`
}

type ClientCapabilities struct {
	FS       FSCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
	// Meta advertises the adapter extensions this client renders (toolmeta.go
	// clientMeta: terminal output on tool calls, subagent transcripts).
	Meta map[string]any `json:"_meta,omitempty"`
	// Elicitation: the agent may ask the user a form (elicit.go) — Claude's
	// AskUserQuestion is disallowed without it.
	Elicitation *ElicitationCaps `json:"elicitation,omitempty"`
}

type ElicitationCaps struct {
	Form *struct{} `json:"form,omitempty"`
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
	ProtocolVersion   int                `json:"protocolVersion"`
	AgentCapabilities *AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod       `json:"authMethods,omitempty"`
	AgentInfo         *Info              `json:"agentInfo,omitempty"`
	Meta              map[string]any     `json:"_meta,omitempty"`
	Extra             map[string]any     `json:"-"`
}

// AgentCapabilities is what the agent can do beyond the baseline. loadSession
// means session/load can reopen one of its earlier sessions by id — the
// resume path; without it a past session is read-only history.
type AgentCapabilities struct {
	LoadSession        bool                `json:"loadSession,omitempty"`
	PromptCapabilities *PromptCapabilities `json:"promptCapabilities,omitempty"`
}

// PromptCapabilities is what a prompt may carry beyond text and
// resource_link (which every agent takes): image blocks, audio blocks,
// embedded resources.
type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // "" (agent) | "terminal"
}

type SessionNewParams struct {
	Cwd        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers"`
	Meta       map[string]any `json:"_meta,omitempty"` // Provider.SessionMeta (e.g. claude's thinking display)
}

type SessionNewResult struct {
	SessionID     string         `json:"sessionId"`
	Modes         *SessionModes  `json:"modes,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// SessionLoadParams reopens an earlier session of the agent's (its own id,
// the same cwd). The agent streams the prior turns back as session/update
// notifications before it answers; then the session is live like a new one.
type SessionLoadParams struct {
	SessionID  string         `json:"sessionId"`
	Cwd        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

type SessionLoadResult struct {
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
	// image / audio: the base64 bytes and their type
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"` // also resource_link's
	// resource_link: a file the agent reads itself
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size,omitempty"`
	// resource: embedded contents (the prompt's small text files)
	Resource *EmbeddedResource `json:"resource,omitempty"`
	// the rest is passed through untouched when we relay a block
	Rest json.RawMessage `json:"-"`
}

// EmbeddedResource is a resource block's contents — the text form
// (TextResourceContents); xbin sends no blobs (an adapter may drop them:
// claude-agent-acp does), binary files go as resource_link.
type EmbeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
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
	Name       *string         `json:"name,omitempty"` // the programmatic tool name (tool-call-name RFD)
	Title      *string         `json:"title,omitempty"`
	Kind       *string         `json:"kind,omitempty"`
	Status     *string         `json:"status,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Locations  json.RawMessage `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"` // adapter extensions, read by toolmeta.go
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

// AttachParams: daemon → host, a prompt's file to drop inside the sandbox
// (data base64 on the wire); the host answers where it put it.
type AttachParams struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type AttachResult struct {
	Path string `json:"path"`
}
