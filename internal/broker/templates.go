package broker

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
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
	srv.RegisterAPI("GET /templates/updates", b.apiTemplateUpdates)
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
	// The mode new instances start in (template.partition), and why this
	// xbind won't write it ("needs --isolate"); both absent when the
	// template names none (templatepartition.go).
	Partition        []string `json:"partition,omitempty"`
	PartitionSkipped string   `json:"partitionSkipped,omitempty"`
}

func (b *Broker) apiTemplatesList(w http.ResponseWriter, r *http.Request) {
	out := []templateItem{}
	if b.templates != nil {
		for _, t := range b.templates.List() {
			_, skipped := instanceOpts(t.Partition, nil)
			out = append(out, templateItem{
				ID: t.Name, Source: "builtin",
				Title: t.Title, Description: t.Description, DefaultName: t.DefaultName,
				Partition: t.Partition, PartitionSkipped: skipped,
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
		def := workspaceTemplateDefault(c)
		_, skipped := instanceOpts(def, nil)
		out = append(out, templateItem{
			ID: c.Path, Source: "workspace",
			Title: tm.Title, Description: tm.Description, DefaultName: name,
			Partition: def, PartitionSkipped: skipped,
		})
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// templateNewBody is POST /templates/new's body.
type templateNewBody struct {
	Source string `json:"source"` // builtin name or workspace component path
	Path   string `json:"path"`   // target component path (optional)
	Owner  string `json:"owner"`  // as /create: "org:<id>" | "user:<id>" | "" (D24/D52)
	// false: the instance doesn't start in the template's partition mode
	// (PD-35 opt-out); absent or true: it does, under --isolate.
	Partition *bool `json:"partition"`
}

// templateNewPlan is what a POST /templates/new would create: the template
// (a builtin, or the workspace component src), the path, the owner ref and
// the template's partition mode.
type templateNewPlan struct {
	builtin bool
	src     *registry.Component // a workspace template
	target  string
	owner   string
	def     []string // the template's default partition mode (templatepartition.go)
}

// templateNewCheck is everything POST /templates/new decides about body by
// p before it writes: the plan, or the refusal. The handler, a delegate's
// dry run and request (CreatePreview) and a change set's tile.create line
// share it, so what one says the other does.
func (b *Broker) templateNewCheck(p auth.Principal, body templateNewBody) (templateNewPlan, *refused) {
	return b.templateNewCheckFor(p, body, p.Agent != "")
}

// templateNewCheckFor is templateNewCheck; quiet says the refusal is read
// by a person's agent (a delegate's call, a plan's line): what it says must
// not tell of tiles the person can't see — a template they can't read is
// no template, a place taken by something they can't read is "taken", with
// no name, and the state a removed tile left at a path is not listed.
func (b *Broker) templateNewCheckFor(p auth.Principal, body templateNewBody, quiet bool) (templateNewPlan, *refused) {
	if body.Source == "" {
		return templateNewPlan{}, &refused{http.StatusBadRequest, "need {source, path?, owner?, partition?}", "/docs/protocol.md"}
	}
	// Resolve the source and the effective target FIRST — instantiating
	// creates a component there, so it takes the same authority as /create
	// (the ownership path rule; the confused-deputy clamp applies), and copying
	// a WORKSPACE template additionally needs read on the source.
	if no := sealedRefusal(cmp.Or(sealedNone(body.Source, "it is no template, and nothing is created under xbin/"), sealedNone(body.Path, sealedNoCreate))); no != nil {
		return templateNewPlan{}, no
	}
	plan := templateNewPlan{builtin: b.templates != nil && templateExists(b.templates, body.Source)}
	if !plan.builtin {
		c, ok := b.Reg.Component(body.Source)
		if !ok || !c.IsTemplate() || quiet && !b.attributedCanRead(p, c.Path) {
			return templateNewPlan{}, &refused{http.StatusNotFound, "no such template: " + body.Source, ""}
		}
		plan.src = c
	}
	plan.target = strings.Trim(body.Path, "/")
	if plan.target == "" {
		if plan.builtin {
			if t, ok := b.templates.Get(body.Source); ok {
				plan.target = t.DefaultPath
			}
		} else {
			plan.target = "apps/" + path.Base(plan.src.Path) // instantiateWorkspace's default
		}
	}
	owner, msg := b.resolveCreateOwner(p, body.Owner)
	if msg != "" {
		return templateNewPlan{}, &refused{http.StatusForbidden, msg, "/docs/auth.md"}
	}
	plan.owner = owner
	if ok, msg := b.canCreateAt(p, plan.target, owner); !ok {
		if quiet && strings.Contains(msg, "still carries state from a removed tile") {
			// (the list of that state — grants, bindings — is an admin's to read)
			msg = "can't create " + plan.target + ": the path still carries state from a removed tile — pick another path, or ask a workspace admin to clear it first"
		}
		return templateNewPlan{}, &refused{http.StatusForbidden, msg, "/docs/auth.md"}
	}
	if plan.src != nil && !b.attributedCanRead(p, plan.src.Path) {
		return templateNewPlan{}, &refused{http.StatusForbidden, "instantiating copies the template — your account has no read access to " + plan.src.Path, "/docs/auth.md"}
	}
	if err := b.guardNewComponentTree(plan.target); err != nil {
		msg := err.Error()
		if tc := (*treeConflict)(nil); quiet && errors.As(err, &tc) && !b.attributedCanRead(p, tc.other) {
			msg = "can't create " + plan.target + ": the place is taken — pick another path"
		}
		return templateNewPlan{}, &refused{http.StatusConflict, msg, ""}
	}
	if plan.builtin {
		t, _ := b.templates.Get(body.Source)
		plan.def = t.Partition
	} else {
		plan.def = workspaceTemplateDefault(plan.src)
	}
	return plan, nil
}

// CreatePreview is what delegate p's POST /templates/new with body would
// create — {template, path, owner, partition?} — or why it is refused: the
// handler's own check, run without writing (the server's delegate gate asks
// it for a dry run and before it puts a request in front of the person).
func (b *Broker) CreatePreview(p auth.Principal, body []byte) (map[string]any, error) {
	var in templateNewBody
	if err := server.StrictDecode(body, &in); err != nil { // what the person is shown is read one way only (D252)
		return nil, errors.New(`need {source, path?, owner?, partition?}, each once, in lower case (` + err.Error() + ")")
	}
	plan, why := b.templateNewCheck(p, in)
	if why != nil {
		return nil, errors.New(why.msg)
	}
	out := map[string]any{"template": in.Source, "path": plan.target, "owner": plan.owner}
	if opts, _ := instanceOpts(plan.def, in.Partition); plan.def != nil && opts.Partition {
		out["partition"] = plan.def
	}
	return out, nil
}

func (b *Broker) apiTemplatesNew(w http.ResponseWriter, r *http.Request) {
	var body templateNewBody
	if err := server.DecodeJSON(r, &body); err != nil || body.Source == "" {
		server.WriteError(w, http.StatusBadRequest, "need {source, path?, owner?, partition?}", "/docs/protocol.md")
		return
	}
	p := auth.PrincipalOf(r)
	plan, why := b.templateNewCheck(p, body)
	if why != nil {
		why.write(w)
		return
	}
	isBuiltin, srcComp, target, owner, def := plan.builtin, plan.src, plan.target, plan.owner, plan.def

	// A removed tile's deployment state at the path goes before the tree is
	// written, so the first Rescan composes the new tile in the zero state (D119i).
	if err := b.resetDeploymentState(target); err != nil {
		slog.Error("a removed tile's deployment record couldn't be reset for the new tile", "tile", target, "err", err)
	}

	opts, skipped := instanceOpts(def, body.Partition)
	who := b.noteInstantiator(target, p) // the first auto mode record names who
	defer who.done()

	var (
		installed   string
		files       []string
		builtinName string   // set for builtin templates → gets a `template` remote
		notCopied   []string // a workspace template's files left out: links, FIFOs, files too large
		err         error
	)
	switch {
	case isBuiltin:
		builtinName = body.Source
		installed, files, err = b.templates.Instantiate(b.Reg.Root, body.Source, target, opts)
	default:
		installed, files, notCopied, err = instantiateWorkspace(b.Reg.Root, srcComp.Path, target, opts)
	}
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	who.created() // this request's copy is the one written

	// Seed the instance's repo from the template's served repo BEFORE
	// EnsureComponentRepos can give it an unrelated fresh root — shared
	// ancestry is what lets `git merge template/main` apply upstream fixes.
	// Best-effort: on failure the instance still gets a plain repo below.
	if builtinName != "" {
		if serr := b.SeedInstanceRepo(filepath.Join(b.Reg.Root, filepath.FromSlash(installed)), builtinName); serr != nil {
			slog.Warn("template instance repo seeding failed (falling back to a fresh root)",
				"template", builtinName, "instance", installed, "err", serr)
		}
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
	b.assignOwner(installed, owner) // D24: creator-owned (workspace-owned for admins) unless requested
	out := map[string]any{"path": installed, "files": files, "pendingGrants": pending}
	if len(notCopied) > 0 { // said, never silently left out (re-review of 2026-11-20, M3)
		out["skipped"] = notCopied
		out["notes"] = []string{fmt.Sprintf("%d file(s) of the template not copied — links, FIFOs and files over 32 MiB are not copied: %s",
			len(notCopied), strings.Join(notCopied, "; "))}
	}
	switch {
	case skipped != "":
		out["partitionSkipped"] = skipped
	case def != nil && opts.Partition:
		out["partition"] = def
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func templateExists(s *builtins.TemplateSet, name string) bool {
	_, ok := s.Get(name)
	return ok
}

// instantiateWorkspace copies a workspace template component (at srcPath) into
// a named instance, stripping the template marker; it starts in the partition
// mode opts allows. Default target basename is the template's DefaultName
// under apps/.
func instantiateWorkspace(root, srcPath, targetPath string, opts builtins.InstanceOpts) (string, []string, []string, error) {
	if targetPath == "" {
		targetPath = "apps/" + path.Base(srcPath)
	}
	// The template is a tile someone writes: its files are read as they
	// are, never through a link (to a home, to another tile) or from a
	// FIFO; such a file is not copied, and said (skipped).
	written, skipped, err := builtins.CopyTreeSkipping(fsutil.NoSymlinksFS(root), srcPath, root, targetPath, srcPath, &opts)
	if err != nil {
		return "", written, skipped, err
	}
	return targetPath, written, skipped, nil
}
