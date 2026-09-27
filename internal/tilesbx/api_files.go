package tilesbx

// api_files.go — files, tar and copy (§3.8). Every path is resolved inside
// the sandbox by its agent, never on the host. Not built yet: each route
// answers unsupported after its gates.

import "net/http"

// ServeStat answers GET /sandboxes/{name}/files/stat?path=.
func (m *Manager) ServeStat(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeReadFile answers GET /sandboxes/{name}/files/content?path=&offset=&length=.
func (m *Manager) ServeReadFile(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeWriteFile answers PUT /sandboxes/{name}/files/content?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*.
func (m *Manager) ServeWriteFile(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeListDir answers GET /sandboxes/{name}/files/list?path=&limit=.
func (m *Manager) ServeListDir(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeMkdir answers POST /sandboxes/{name}/files/mkdir {path, parents}.
func (m *Manager) ServeMkdir(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeRemove answers POST /sandboxes/{name}/files/remove {path, recursive}.
func (m *Manager) ServeRemove(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeMove answers POST /sandboxes/{name}/files/move {from, to, overwrite}.
func (m *Manager) ServeMove(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("files"))
	}
}

// ServeGetTar answers GET /sandboxes/{name}/tar?path=&exclude=….
func (m *Manager) ServeGetTar(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("trees (tar)"))
	}
}

// ServePutTar answers PUT /sandboxes/{name}/tar?path=&mkdirs=1.
func (m *Manager) ServePutTar(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.managed(w, r); ok {
		writeErr(w, notBuilt("trees (tar)"))
	}
}

// ServeCopy answers POST /sandboxes/copy {from:{sandbox, path}, to:{sandbox,
// path}, overwrite}: between two sandboxes of the caller's own.
func (m *Manager) ServeCopy(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.manager(w, r); ok {
		writeErr(w, notBuilt("copies between sandboxes (tar)"))
	}
}
