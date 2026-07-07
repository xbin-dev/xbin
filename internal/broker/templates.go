package broker

import (
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/builtins"
	"github.com/magik6k/xbin/internal/server"
)

// Template component endpoints (plans/templates.md). A template is a component
// blueprint that isn't plugged in until instantiated into a named copy.
// Sources are the embedded builtin catalog and any workspace component carrying
// a xbin.json "template" block. Listing is open to any authenticated
// principal; instantiating creates a component, so it needs the same
// workspace-management capability as POST /create (xbin:writer, admin implies).

func (b *Broker) registerTemplates(srv *server.Server) {
	srv.RegisterAPI("GET /templates", b.apiTemplatesList)
	srv.RegisterAPI("POST /templates/new", b.apiTemplatesNew)
	// Read-only dumb-HTTP git server for template source repos; instances get
	// this as their `template` remote (plans/agent-v2.md §template updates).
	srv.RegisterAPI("GET /templates/{repo}/{rest...}", b.serveTemplateRepo)
}

type templateItem struct {
	ID          string `json:"id"`          // builtin name, or workspace component path
	Source      string `json:"source"`      // "builtin" | "workspace"
	Title       string `json:"title"`       //
	Description string `json:"description"` //
	DefaultName string `json:"defaultName"` // suggested instance basename
}

func (b *Broker) apiTemplatesList(w http.ResponseWriter, r *http.Request) {
	out := []templateItem{}
	if b.templates != nil {
		for _, t := range b.templates.List() {
			out = append(out, templateItem{
				ID: t.Name, Source: "builtin",
				Title: t.Title, Description: t.Description, DefaultName: t.DefaultName,
			})
		}
	}
	for _, c := range b.Reg.Components() {
		if !c.IsTemplate() {
			continue
		}
		tm := c.Manifest.Template
		name := tm.DefaultName
		if name == "" {
			name = path.Base(c.Path)
		}
		out = append(out, templateItem{
			ID: c.Path, Source: "workspace",
			Title: tm.Title, Description: tm.Description, DefaultName: name,
		})
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func (b *Broker) apiTemplatesNew(w http.ResponseWriter, r *http.Request) {
	// Instantiating a template creates a component: same capability as /create.
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		role, ok := b.grantedRole(p.Component, "xbin")
		if p.Component == "" || !ok || !roleSatisfies(role, "writer", nil) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error": "instantiating templates needs the workspace-management grant (xbin:writer) — the same as creating components",
				"docs":  "/docs/auth.md",
			})
			return
		}
	}
	var body struct {
		Source string `json:"source"` // builtin name or workspace component path
		Path   string `json:"path"`   // target component path (optional)
	}
	if err := decodeJSON(r, &body); err != nil || body.Source == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {source, path?}", "docs": "/docs/protocol.md"})
		return
	}

	var (
		installed   string
		files       []string
		builtinName string // set for builtin templates → gets a `template` remote
		err         error
	)
	switch {
	case b.templates != nil && templateExists(b.templates, body.Source):
		builtinName = body.Source
		installed, files, err = b.templates.Instantiate(b.Reg.Root, body.Source, body.Path)
	default:
		// Workspace template: source is a component path carrying a marker.
		c, ok := b.Reg.Component(body.Source)
		if !ok || !c.IsTemplate() {
			server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such template: " + body.Source})
			return
		}
		target := body.Path
		installed, files, err = instantiateWorkspace(b.Reg.Root, c.Path, target)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Make the instance (and its Go module, if any) usable immediately —
	// don't wait for the debounced watcher tick.
	_ = b.Reg.Rescan()
	if b.OnStructureChange != nil {
		b.OnStructureChange() // EnsureComponentRepos gives the instance its git repo
	}
	b.Provision()

	// A builtin-template instance gets the template's served repo as its
	// `template` remote, so a builder can `git fetch template && git merge`
	// upstream fixes (plans/agent-v2.md).
	if builtinName != "" {
		b.AddTemplateRemote(filepath.Join(b.Reg.Root, filepath.FromSlash(installed)), builtinName)
	}

	var pending []registryGrantLite
	for _, g := range b.Pending() {
		if hasPrefix(g.From, installed) {
			pending = append(pending, registryGrantLite{From: g.From, Target: g.Target, Role: g.Role})
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"path": installed, "files": files, "pendingGrants": pending,
	})
}

func templateExists(s *builtins.TemplateSet, name string) bool {
	_, ok := s.Get(name)
	return ok
}

// instantiateWorkspace copies a workspace template component (at srcPath) into
// a named instance, stripping the template marker. Default target basename is
// the template's DefaultName under apps/.
func instantiateWorkspace(root, srcPath, targetPath string) (string, []string, error) {
	if targetPath == "" {
		targetPath = "apps/" + path.Base(srcPath)
	}
	written, err := builtins.CopyTree(os.DirFS(root), srcPath, root, targetPath, srcPath, true)
	if err != nil {
		return "", written, err
	}
	return targetPath, written, nil
}
