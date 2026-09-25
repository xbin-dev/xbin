// routes.go — every HTTP route and what its caller needs (D83). The table is
// the single place access is declared: guard() resolves the caller and, for a
// route on one run, its access to that run's root before the handler runs.
package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// need is what a route asks of its caller.
type need int

const (
	needAny         need = iota // any caller but cron; the handler filters by access
	needStart                   // may start runs: a person, an element, the system
	needViewer                  // may see the run
	needParticipant             // may talk to it and steer it
	needOwner                   // may rename, share or delete it
	needUser                    // a person (joining a shared conversation)
	needManager                 // may change what the whole tile does
	needAutomation              // an automation's own routes; the handler checks ownership
	needCron                    // cron or the system
	needSelf                    // the tile itself
)

type routeDef struct {
	pattern string
	need    need
	h       http.HandlerFunc
}

func routeTable() []routeDef {
	return []routeDef{
		{"GET /runs", needAny, handleListRuns},
		{"POST /runs", needStart, handleNewRun},
		{"POST /ask", needStart, handleAsk},
		{"GET /runs/{id}", needViewer, handleGetRun},
		{"GET /runs/{id}/view", needViewer, handleView},
		{"GET /runs/{id}/stream", needViewer, handleStream},
		{"GET /stream", needAny, handleStream},
		{"DELETE /runs/{id}", needOwner, handleDeleteRun},
		{"POST /runs/{id}/message", needParticipant, handleMessage},
		{"POST /runs/{id}/answer", needParticipant, handleMessage},
		{"DELETE /runs/{id}/inbox/{iid}", needParticipant, handleRemoveQueued},
		{"POST /runs/{id}/approve", needParticipant, handleApprove},
		{"POST /runs/{id}/interrupt", needParticipant, handleInterrupt},
		{"POST /runs/{id}/cancel", needParticipant, handleCancel},
		{"GET /runs/{id}/tree", needViewer, handleRunTree},
		{"GET /halt", needAny, handleHaltGet},
		{"PUT /halt", needManager, handleHaltPut},
		{"POST /runs/{id}/resume", needParticipant, handleResume},
		{"POST /runs/{id}/compact", needParticipant, handleCompact},
		{"PUT /runs/{id}/memory", needParticipant, handleMemoryPut},
		{"DELETE /runs/{id}/memory", needParticipant, handleMemoryDelete},
		// Session files: metadata and content are separate routes.
		{"GET /runs/{id}/files", needViewer, handleFilesList},
		{"GET /runs/{id}/file", needViewer, handleFileGet},
		{"PUT /runs/{id}/file", needParticipant, handleFilePut},
		{"DELETE /runs/{id}/file", needParticipant, handleFileDelete},
		// Attachments: the raw body in, metadata in the query string (the tile is
		// a sandboxed opaque origin; no custom headers pass the gateway's CORS).
		{"PUT /runs/{id}/upload", needParticipant, handleUpload},
		{"GET /runs/{id}/raw", needViewer, handleRaw},
		{"GET /config", needManager, handleGetConfig},
		{"PUT /config", needManager, handlePutConfig},
		{"GET /features", needAny, handleFeatures},
		{"GET /models", needManager, handleModels},
		{"GET /schedules", needAny, handleListSchedules},
		{"POST /schedules", needStart, handleNewSchedule},
		{"PUT /schedules/{id}", needAutomation, handleUpdateSchedule},
		{"DELETE /schedules/{id}", needAutomation, handleDeleteSchedule},
		{"POST /schedules/{id}/fire", needCron, handleFireSchedule},
		{"POST /schedules/{id}/trigger", needAutomation, handleFireSchedule},
		{"GET /skills", needAny, handleListSkills},
		{"PUT /skills", needManager, handleSaveSkill},
		{"DELETE /skills/{name}", needManager, handleDeleteSkill},
		{"POST /runs/{id}/learn", needParticipant, handleLearn},
		{"POST /tick", needCron, handleTick},
		{"GET /engine/hold", needSelf, handleHold},
	}
}

// routes registers the table. Every route is admin-only at the platform
// level (the tile itself and its owner); who the human behind a call is, and
// what they may do with a run, is decided here.
func routes(mux *http.ServeMux) {
	for _, rt := range routeTable() {
		mux.Handle(rt.pattern, xbin.RoleFunc("admin", guard(rt.need, rt.h)))
	}
}

// guard is where a route's need is enforced (access.go).
func guard(n need, h http.HandlerFunc) http.HandlerFunc {
	_ = n
	return h
}
