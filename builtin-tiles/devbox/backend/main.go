// Devbox backend — a container-host tile (plans/containers.md). It runs three
// things in one process:
//
//   - a control HTTP API (xbin.Serve, on the gateway socket) that the tile's
//     page uses to create/list/remove Podman containers and manage the owner's
//     authorized SSH keys;
//   - an SSH proxy (ssh.go) on :2222 in the tile's netns — exposed to a host
//     TCP port by the owner's ingress binding (`bx expose apps/devbox
//     ssh=runtime --listen :2222`) — that authenticates the owner's key and
//     routes `ssh -p2222 <container>@host` to `podman exec -it <container>`;
//   - the Podman wrapper (podman.go) pinned to the persistent `storage`
//     resource.
//
// The tile needs the admin-only cap:containers grant (rootless podman needs its
// user-namespace caps to build nested namespaces/mounts) and a `net` binding
// for container egress. See docs/changes/2026-07-14-container-tiles.md.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	xbin "github.com/magik6k/xbin/sdk"
)

const sshPort = ":2222" // matches exposes.ssh.port in xbin.json

func main() {
	storage := xbin.Resource("storage")
	if storage == "" {
		log.Println("devbox: the `storage` resource isn't granted yet — approve it; running with an ephemeral store")
		storage = filepath.Join(os.TempDir(), "devbox-store")
		_ = os.MkdirAll(storage, 0o700)
	}
	runtimeDir := filepath.Join(os.TempDir(), "devbox-run")
	_ = os.MkdirAll(runtimeDir, 0o700)

	pod := newPodman(storage, runtimeDir)
	st := newStore(storage)

	// Bring up the SSH proxy (best-effort: the control plane still works if the
	// host key can't be written, e.g. storage not yet granted).
	var sshErr string
	if proxy, err := newSSHProxy(st, pod); err != nil {
		sshErr = err.Error()
		log.Printf("devbox: ssh proxy disabled: %v", err)
	} else if err := proxy.listenSSH(sshPort); err != nil {
		sshErr = err.Error()
		log.Printf("devbox: ssh listen %s: %v", sshPort, err)
	}

	mux := http.NewServeMux()

	// State for the page: podman health, containers, authorized keys.
	mux.Handle("GET /state", xbin.RoleFunc("reader", func(w http.ResponseWriter, r *http.Request) {
		ver, verErr := pod.version()
		cs, listErr := pod.list()
		if cs == nil {
			cs = []container{}
		}
		errStr := ""
		if verErr != nil {
			errStr = "podman not ready: " + verErr.Error() + " (approve cap:containers, then it rebuilds)"
		} else if listErr != nil {
			errStr = listErr.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"podmanVersion": ver,
			"containers":    cs,
			"keys":          st.list(),
			"sshPort":       sshPort,
			"sshError":      sshErr,
			"error":         errStr,
		})
	}))

	mux.Handle("POST /containers", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Image == "" {
			writeErr(w, http.StatusBadRequest, "need {name, image}")
			return
		}
		if !validName(body.Name) {
			writeErr(w, http.StatusBadRequest, "name must be a short dns-ish label (it's also the SSH username)")
			return
		}
		if out, err := pod.create(body.Name, body.Image, nil); err != nil {
			writeErr(w, http.StatusBadGateway, firstLine(out)+" — "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	mux.Handle("DELETE /containers/{name}", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if out, err := pod.remove(name); err != nil {
			writeErr(w, http.StatusBadGateway, firstLine(out)+" — "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	// Lifecycle: start/stop a container without removing it.
	mux.Handle("POST /containers/{name}/{action}", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		name, action := r.PathValue("name"), r.PathValue("action")
		var out string
		var err error
		switch action {
		case "start":
			out, err = pod.start(name)
		case "stop":
			out, err = pod.stop(name)
		default:
			writeErr(w, http.StatusBadRequest, "action must be start|stop")
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadGateway, firstLine(out)+" — "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	// Authorized SSH keys — the SSH proxy is default-deny until you add one.
	mux.Handle("POST /keys", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			writeErr(w, http.StatusBadRequest, "need {key: \"ssh-ed25519 AAAA… you@host\"}")
			return
		}
		k, err := st.add(body.Key)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, k)
	}))

	mux.Handle("DELETE /keys/{fp...}", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		if err := st.remove(r.PathValue("fp")); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	}))

	xbin.Serve(mux)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 200 {
		return s[:200]
	}
	return fmt.Sprint(s)
}
