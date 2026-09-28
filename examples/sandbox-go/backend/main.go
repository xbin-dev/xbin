// sandbox-go — the smallest sandbox manager on xbind's own runtime (D120).
//
// A manager tile's backend defines sandboxes xbind runs for it and drives
// them over /api/xbin/sandboxes/… (docs/protocol.md §Tile sandboxes). This
// one holds cap:sandboxes (a workspace admin approves it) and one egress
// class, the "internet" sandbox-net slot (bx bind <tile> internet=internet).
// Its routes (API.md) are a thin layer over xbin.SandboxAPI():
//
//   - definitions and lifecycle are typed calls (Create, Get, Start, …);
//   - run and exec are typed calls too: the manager sets forUser, the
//     person xbind verified, whatever the caller's body said;
//   - an exec's output, stdin, signals, files and tar are Forward to a
//     typed route (ExecOutput(eid), FilesRoute(op), TarRoute()) with the
//     query the manager picked — the bodies stream both ways;
//   - terminals are RelayTTY / RelayNewTTY: the caller's WebSocket is
//     tunnelled to the runtime byte for byte, with no WebSocket code here.
//
// Every per-sandbox route checks first that the sandbox is the caller's:
// its home label is the calling tile (X-XBin-From), stamped at create. A
// consumer's id can't reach around that check — the SDK refuses a sandbox
// name with a "/" and an exec id that fails the runtime's grammar before
// anything is sent (docs/sdk.md §Tile sandboxes). A real manager (the
// coding-sandbox template) adds sharing, people, images and quotas on top.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// homeLabel is the label that says which caller a sandbox belongs to.
const homeLabel = "sandbox-go.home"

var sbx = xbin.SandboxAPI()

func main() {
	mux := http.NewServeMux()
	see := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, xbin.RoleFunc("reader", h)) }
	use := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, xbin.RoleFunc("writer", h)) }

	see("GET /runtime", runtimeInfo)
	see("GET /sandboxes", list)
	use("POST /sandboxes", create)
	see("GET /sandboxes/{id}", mine(func(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) { writeJSON(w, 200, in) }))
	use("DELETE /sandboxes/{id}", mine(remove))
	for _, op := range []string{"start", "stop", "reset", "rebase"} {
		use("POST /sandboxes/{id}/"+op, mine(lifecycle(op)))
	}

	use("POST /sandboxes/{id}/run", mine(run))
	use("POST /sandboxes/{id}/execs", mine(execStart))
	see("GET /sandboxes/{id}/execs", mine(execList))
	see("GET /sandboxes/{id}/execs/{eid}", mine(execOne))
	use("DELETE /sandboxes/{id}/execs/{eid}", mine(execForward(xbin.ExecRoute)))
	see("GET /sandboxes/{id}/execs/{eid}/output", mine(execForward(xbin.ExecOutput, "since", "max", "waitMs", "encoding")))
	use("POST /sandboxes/{id}/execs/{eid}/stdin", mine(execForward(xbin.ExecStdin, "eof")))
	use("POST /sandboxes/{id}/execs/{eid}/signal", mine(execForward(xbin.ExecSignal)))
	use("POST /sandboxes/{id}/execs/{eid}/resize", mine(execForward(xbin.ExecResize)))
	use("GET /sandboxes/{id}/execs/{eid}/tty", mine(ttyAttach))
	use("GET /sandboxes/{id}/tty", mine(ttyStart))

	see("GET /sandboxes/{id}/files/{op}", mine(files))  // stat, content, list
	use("PUT /sandboxes/{id}/files/{op}", mine(files))  // content
	use("POST /sandboxes/{id}/files/{op}", mine(files)) // mkdir, remove, move
	see("GET /sandboxes/{id}/tar", mine(tar))
	use("PUT /sandboxes/{id}/tar", mine(tar))

	see("GET /sandboxes/{id}/snapshots", mine(snapshots))
	use("POST /sandboxes/{id}/snapshots", mine(snapshot))
	use("POST /sandboxes/{id}/snapshots/{sid}/restore", mine(restore))
	use("DELETE /sandboxes/{id}/snapshots/{sid}", mine(dropSnapshot))

	xbin.Serve(mux)
}

// home is who a call comes from, as xbind sets X-XBin-From (a caller
// can't): the calling tile — this tile's own page included — or "owner" /
// "user:<id>" for a call made with no tile's credential.
func home(r *http.Request) string { return xbin.Caller(r).From }

// mine loads sandbox {id} and hands it on only when it is the caller's;
// anyone else's is not-found, as if it didn't exist.
func mine(h func(http.ResponseWriter, *http.Request, *xbin.SandboxInfo)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in, err := sbx.Get(r.Context(), r.PathValue("id"))
		if err == nil && in.Labels[homeLabel] != home(r) {
			err = &xbin.SandboxError{Status: 404, Refusal: "not-found", Message: "no such sandbox"}
		}
		if err != nil {
			xbin.WriteSandboxError(w, err)
			return
		}
		h(w, r, in)
	}
}

func runtimeInfo(w http.ResponseWriter, r *http.Request) {
	rt, err := sbx.Runtime(r.Context())
	reply(w, 200, rt, err)
}

func list(w http.ResponseWriter, r *http.Request) {
	all, err := sbx.List(r.Context())
	out := []xbin.SandboxInfo{}
	for _, in := range all {
		if in.Labels[homeLabel] == home(r) {
			out = append(out, in)
		}
	}
	reply(w, 200, map[string]any{"sandboxes": out}, err)
}

// createReq is this manager's create: a name, a mode ("namespace", the
// default, or "vm"), an egress ("none", the default, or "internet": the
// sandbox-net slot), and whether to start it.
type createReq struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Egress string `json:"egress"`
	MemMiB int    `json:"memMiB"`
	Start  bool   `json:"start"`
}

func create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	spec := xbin.SandboxSpec{Name: req.Name, Mode: req.Mode, MemMiB: req.MemMiB, Start: req.Start,
		Labels: map[string]string{homeLabel: home(r)}, ForUser: xbin.Caller(r).User}
	if strings.Contains(home(r), "/") {
		spec.For = home(r) // the claims the admin's view shows: the consumer tile, the person
	}
	if spec.Mode == "" {
		spec.Mode = "namespace"
	}
	switch req.Egress {
	case "", "none":
	case "internet":
		spec.Net = &xbin.SandboxNet{Egress: "class:internet"}
	default:
		xbin.WriteSandboxError(w, &xbin.SandboxError{Status: 400, Refusal: "invalid", Message: "egress is none or internet"})
		return
	}
	in, err := sbx.Create(r.Context(), spec)
	reply(w, http.StatusCreated, in, err)
}

func remove(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	reply(w, http.StatusNoContent, nil, sbx.Delete(r.Context(), in.Name))
}

// lifecycle is start, stop, reset or rebase, with the caller's ?wait=
// (seconds, at least 1; the runtime clamps it to limits.waitMaxSec, and
// waits that long without one).
func lifecycle(op string) func(http.ResponseWriter, *http.Request, *xbin.SandboxInfo) {
	call := map[string]func(context.Context, string, time.Duration) (*xbin.SandboxInfo, error){
		"start": sbx.Start, "stop": sbx.Stop, "reset": sbx.Reset, "rebase": sbx.Rebase,
	}[op]
	return func(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
		var wait time.Duration
		if s, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		out, err := call(r.Context(), in.Name, wait)
		reply(w, 200, out, err)
	}
}

// --- commands ----------------------------------------------------------------

func run(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	var req xbin.RunRequest
	if !decode(w, r, &req) {
		return
	}
	req.ForUser = xbin.Caller(r).User // the verified person, never the body's
	res, err := sbx.Sandbox(in.Name).Run(r.Context(), req)
	reply(w, 200, res, err)
}

func execStart(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	var req xbin.ExecRequest
	if !decode(w, r, &req) {
		return
	}
	req.ForUser = xbin.Caller(r).User
	ex, err := sbx.Sandbox(in.Name).Exec(r.Context(), req)
	reply(w, http.StatusCreated, ex, err)
}

func execList(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	execs, err := sbx.Sandbox(in.Name).Execs(r.Context())
	reply(w, 200, map[string]any{"execs": execs}, err)
}

func execOne(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	eid, ok := execID(w, r)
	if !ok {
		return
	}
	ex, err := sbx.Sandbox(in.Name).GetExec(r.Context(), eid)
	reply(w, 200, ex, err)
}

// execID is the route's {eid}. One that doesn't fit the runtime's grammar
// names no exec here: not-found, and nothing is sent.
func execID(w http.ResponseWriter, r *http.Request) (string, bool) {
	eid := r.PathValue("eid")
	if !xbin.IsExecID(eid) {
		xbin.WriteSandboxError(w, &xbin.SandboxError{Status: 404, Refusal: "not-found", Message: "no such exec"})
		return "", false
	}
	return eid, true
}

// execForward passes the call through to the exec's runtime route, with
// only the query keys named (never the caller's raw query).
func execForward(route func(string) xbin.SandboxRoute, keys ...string) func(http.ResponseWriter, *http.Request, *xbin.SandboxInfo) {
	return func(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
		eid, ok := execID(w, r)
		if !ok {
			return
		}
		sbx.Sandbox(in.Name).Forward(w, r, route(eid), pick(r, keys...))
	}
}

// --- terminals ---------------------------------------------------------------

// ttyAttach relays the caller's terminal WebSocket to tty exec {eid}.
func ttyAttach(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	eid, ok := execID(w, r)
	if !ok {
		return
	}
	sbx.Sandbox(in.Name).RelayTTY(w, r, eid, xbin.TTYOptions{SandboxID: in.Name, ForUser: xbin.Caller(r).User})
}

// ttyStart starts a tty exec (the login shell unless ?cmd=) and relays the
// caller's terminal WebSocket to it.
func ttyStart(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	q := r.URL.Query()
	rows, _ := strconv.Atoi(q.Get("rows"))
	cols, _ := strconv.Atoi(q.Get("cols"))
	sbx.Sandbox(in.Name).RelayNewTTY(w, r, xbin.TTYStart{Cwd: q.Get("cwd"), Cmd: q.Get("cmd"),
		Rows: max(rows, 0), Cols: max(cols, 0), SandboxID: in.Name, ForUser: xbin.Caller(r).User})
}

// --- files -------------------------------------------------------------------

// files forwards files/{op}: FilesRoute refuses an op that isn't one.
func files(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	sbx.Sandbox(in.Name).Forward(w, r, xbin.FilesRoute(xbin.FilesOp(r.PathValue("op"))),
		pick(r, "path", "offset", "length", "limit", "mode", "mkdirs", "ifMatch", "ifNoneMatch"))
}

func tar(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	sbx.Sandbox(in.Name).Forward(w, r, xbin.TarRoute(), pick(r, "path", "exclude", "mkdirs"))
}

// --- snapshots ---------------------------------------------------------------

func snapshots(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	snaps, err := sbx.Sandbox(in.Name).Snapshots(r.Context())
	reply(w, 200, map[string]any{"snapshots": snaps}, err)
}

func snapshot(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	var req struct {
		Name     string `json:"name"`
		ClientID string `json:"clientId"`
	}
	if !decode(w, r, &req) {
		return
	}
	sn, err := sbx.Sandbox(in.Name).Snapshot(r.Context(), req.Name, req.ClientID)
	reply(w, http.StatusCreated, sn, err)
}

func restore(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	out, err := sbx.Sandbox(in.Name).RestoreSnapshot(r.Context(), r.PathValue("sid"))
	reply(w, 200, out, notFound(err))
}

func dropSnapshot(w http.ResponseWriter, r *http.Request, in *xbin.SandboxInfo) {
	reply(w, http.StatusNoContent, nil, notFound(sbx.Sandbox(in.Name).DeleteSnapshot(r.Context(), r.PathValue("sid"))))
}

// notFound turns the SDK's refusal of a snapshot id that fails the grammar
// (400: it names nothing) into the not-found this API answers for it.
func notFound(err error) error {
	var e *xbin.SandboxError
	if errors.As(err, &e) && e.Refusal == "invalid" && e.Status == 400 {
		return &xbin.SandboxError{Status: 404, Refusal: "not-found", Message: "no such snapshot"}
	}
	return err
}

// --- plumbing ----------------------------------------------------------------

// pick is the caller's query, only the keys named.
func pick(r *http.Request, keys ...string) url.Values {
	q, out := r.URL.Query(), url.Values{}
	for _, k := range keys {
		if vs, ok := q[k]; ok {
			out[k] = vs
		}
	}
	return out
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil && err != io.EOF {
		xbin.WriteSandboxError(w, &xbin.SandboxError{Status: 400, Refusal: "invalid", Message: "the body: " + err.Error()})
		return false
	}
	return true
}

// reply answers v as JSON with status, or err in the contract's shape (a
// runtime refusal passes on unchanged).
func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
