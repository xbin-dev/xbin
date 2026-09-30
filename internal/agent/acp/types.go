package acp

// The ACP v1 subset (sdk/acp types.go), re-exported, and xbind's own
// frames daemon ⇄ host (_xbin/*), which never reach an agent.

import (
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

const ProtocolVersion = sdkacp.ProtocolVersion

// Method names (schema/v1/meta.json) and session/update variants.
const (
	MInitialize        = sdkacp.MInitialize
	MAuthenticate      = sdkacp.MAuthenticate
	MSessionNew        = sdkacp.MSessionNew
	MSessionLoad       = sdkacp.MSessionLoad
	MSessionPrompt     = sdkacp.MSessionPrompt
	MSessionCancel     = sdkacp.MSessionCancel
	MSessionSetMode    = sdkacp.MSessionSetMode
	MSessionSetConfig  = sdkacp.MSessionSetConfig
	MSessionUpdate     = sdkacp.MSessionUpdate
	MRequestPermission = sdkacp.MRequestPermission
	MElicitCreate      = sdkacp.MElicitCreate
	MElicitComplete    = sdkacp.MElicitComplete
	MCancelRequest     = sdkacp.MCancelRequest
	MFsRead            = sdkacp.MFsRead
	MFsWrite           = sdkacp.MFsWrite
	MTermCreate        = sdkacp.MTermCreate
	MTermOutput        = sdkacp.MTermOutput
	MTermWait          = sdkacp.MTermWait
	MTermKill          = sdkacp.MTermKill
	MTermRelease       = sdkacp.MTermRelease
	MAuthStatus        = sdkacp.MAuthStatus
	UpUserChunk        = sdkacp.UpUserChunk
	UpAgentChunk       = sdkacp.UpAgentChunk
	UpThoughtChunk     = sdkacp.UpThoughtChunk
	UpToolCall         = sdkacp.UpToolCall
	UpToolCallUpdate   = sdkacp.UpToolCallUpdate
	UpPlan             = sdkacp.UpPlan
	UpAvailableCmds    = sdkacp.UpAvailableCmds
	UpCurrentMode      = sdkacp.UpCurrentMode
	UpConfigOption     = sdkacp.UpConfigOption
	UpSessionInfo      = sdkacp.UpSessionInfo
	UpUsage            = sdkacp.UpUsage
	// xbin's own, daemon ⇄ host (never reaches the agent):
	MXbinSpawn  = "_xbin/spawn"
	MXbinHello  = "_xbin/hello"
	MXbinLog    = "_xbin/log"
	MXbinAttach = "_xbin/attach" // a prompt's file, dropped inside the sandbox by the host (a request)
)

type (
	InitializeParams        = sdkacp.InitializeParams
	ClientCapabilities      = sdkacp.ClientCapabilities
	ElicitationCaps         = sdkacp.ElicitationCaps
	FSCapabilities          = sdkacp.FSCapabilities
	Info                    = sdkacp.Info
	InitializeResult        = sdkacp.InitializeResult
	AgentCapabilities       = sdkacp.AgentCapabilities
	PromptCapabilities      = sdkacp.PromptCapabilities
	AuthMethod              = sdkacp.AuthMethod
	SessionNewParams        = sdkacp.SessionNewParams
	SessionNewResult        = sdkacp.SessionNewResult
	SessionLoadParams       = sdkacp.SessionLoadParams
	SessionLoadResult       = sdkacp.SessionLoadResult
	ConfigOption            = sdkacp.ConfigOption
	ConfigValue             = sdkacp.ConfigValue
	SetConfigParams         = sdkacp.SetConfigParams
	SetConfigResult         = sdkacp.SetConfigResult
	ConfigOptionUpdate      = sdkacp.ConfigOptionUpdate
	SessionInfoUpdate       = sdkacp.SessionInfoUpdate
	SessionModes            = sdkacp.SessionModes
	ModeEntry               = sdkacp.ModeEntry
	ContentBlock            = sdkacp.ContentBlock
	EmbeddedResource        = sdkacp.EmbeddedResource
	PromptParams            = sdkacp.PromptParams
	PromptResult            = sdkacp.PromptResult
	SessionIDParams         = sdkacp.SessionIDParams
	SetModeParams           = sdkacp.SetModeParams
	SessionUpdate           = sdkacp.SessionUpdate
	UpdateEnvelope          = sdkacp.UpdateEnvelope
	ChunkUpdate             = sdkacp.ChunkUpdate
	ToolCallUpdate          = sdkacp.ToolCallUpdate
	PlanUpdate              = sdkacp.PlanUpdate
	CurrentModeUpdate       = sdkacp.CurrentModeUpdate
	UsageUpdate             = sdkacp.UsageUpdate
	RequestPermissionParams = sdkacp.RequestPermissionParams
	RequestPermissionResult = sdkacp.RequestPermissionResult
	PermissionOutcome       = sdkacp.PermissionOutcome
	CancelRequestParams     = sdkacp.CancelRequestParams
	FsReadParams            = sdkacp.FsReadParams
	FsReadResult            = sdkacp.FsReadResult
	FsWriteParams           = sdkacp.FsWriteParams
	EnvVar                  = sdkacp.EnvVar
	TermCreateParams        = sdkacp.TermCreateParams
	TermCreateResult        = sdkacp.TermCreateResult
	TermIDParams            = sdkacp.TermIDParams
	ExitStatus              = sdkacp.ExitStatus
	TermOutputResult        = sdkacp.TermOutputResult
	PermissionOption        = sdkacp.PermissionOption
)

// xbin's daemon → host first frame: what to run.
type SpawnParams struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Cwd  string   `json:"cwd"`
	// AttachDir is where the host drops a prompt's files when the daemon
	// owns that directory (isolation off: the host shares xbind's /tmp and
	// is SIGKILLed at the end, so the daemon removes it). "" = the host
	// makes one under the sandbox's own /tmp.
	AttachDir string `json:"attachDir,omitempty"`
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
