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
	XbinPrefix  = "_xbin/" // the daemon ⇄ host vocabulary: never the agent's to send, or to receive
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
	// LLM is the workspace's LLM connector for this agent, when it is
	// offered one (D226 part 2): the host — which sees the session's $HOME
	// where the daemon does not — decides who serves the agent's model
	// calls and applies that environment. nil: Env is the agent's, as it is.
	LLM *SpawnLLM `json:"llm,omitempty"`
	// Signin is a saved sign-in of the session's person the daemon puts
	// into the agent's environment (D230): applied by the host before the
	// connector's part is settled, so a session that carries one has "a
	// sign-in of its own". nil: none.
	Signin *SpawnSignin `json:"signin,omitempty"`
	// LoginFiles are the provider's own login files (shell words): the host
	// reports whether the session's $HOME holds one (HelloParams.HomeLogin),
	// so the session can say it runs on the tile's own login.
	LoginFiles []string `json:"loginFiles,omitempty"`
}

// SpawnSignin is a saved sign-in for one agent session. Its Env line holds
// the secret: it is set on the agent's process alone — never on the host's
// own environment, in a file, or in a log line.
type SpawnSignin struct {
	// Env is the credential's line, "NAME=value"; Unset the provider's
	// other credential variables, dropped first so none outranks it.
	Env   string   `json:"env"`
	Unset []string `json:"unset,omitempty"`
	// KeyHome: the CLI writes a key handed to it through authenticate into
	// File under the directory its variable Env names (codex: CODEX_HOME,
	// by default Dir). The host then gives the agent a directory of its
	// own under the sandbox's /tmp — links to everything in the person's
	// one but File — so the key file never lands in the shared $HOME, and
	// removes it when the agent is gone. If the person's directory already
	// holds File (the CLI signed in on its own there), the saved sign-in is
	// left out altogether: the CLI would start signed in by that file.
	KeyHome *SpawnKeyHome `json:"keyHome,omitempty"`
}

// SpawnKeyHome is sdk/acp's KeyHome on the wire.
type SpawnKeyHome struct {
	Env  string `json:"env"`
	Dir  string `json:"dir"` // a shell word ($HOME)
	File string `json:"file"`
	// At is the private directory when the daemon made it (isolation off:
	// the daemon removes it with the session); "" = the host makes one
	// under the sandbox's own /tmp and removes it.
	At string `json:"at,omitempty"`
}

// SpawnLLM is the connector hook-up of one agent session (sdk/acp
// connector.go): the precedence already resolved as far as the daemon can
// (Rule), and both outcomes' environments rendered.
type SpawnLLM struct {
	// Rule: "connector" (the connector serves), "own" (the person's own
	// sign-in does) or "own-first" — their own when the host finds one
	// (KeyEnvs set in Env, or one of LoginFiles there), else the connector.
	Rule string `json:"rule"`
	// LoginFiles are the provider's login files as shell words, KeyEnvs the
	// variables it reads a credential of the person's from.
	LoginFiles []string `json:"loginFiles,omitempty"`
	KeyEnvs    []string `json:"keyEnvs,omitempty"`
	// Connector is applied to Env when the connector serves, Own when the
	// person's own sign-in does.
	Connector EnvDelta `json:"connector"`
	Own       EnvDelta `json:"own"`
	// Logins are every offered provider's login files (id → shell words):
	// the host reports which of them are signed in (HelloParams.Logins), so
	// the launcher can say what each would run on.
	Logins map[string][]string `json:"logins,omitempty"`
}

// EnvDelta changes an environment: Unset names dropped, Set lines added.
type EnvDelta struct {
	Set   []string `json:"set,omitempty"`
	Unset []string `json:"unset,omitempty"`
}

type HelloParams struct {
	Version string `json:"version"`
	// LLM answers SpawnParams.LLM: who serves the agent's model calls —
	// "connector" or "own". Logins: which of SpawnLLM.Logins' providers the
	// person is signed in to here.
	LLM    string          `json:"llm,omitempty"`
	Logins map[string]bool `json:"logins,omitempty"`
	// Signin answers SpawnParams.Signin: "saved" (the saved sign-in is in
	// the agent's environment) or "home" (left out: the CLI is signed in on
	// its own in this $HOME, and would start on that).
	Signin string `json:"signin,omitempty"`
	// HomeLogin: one of SpawnParams.LoginFiles is there.
	HomeLogin bool `json:"homeLogin,omitempty"`
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
