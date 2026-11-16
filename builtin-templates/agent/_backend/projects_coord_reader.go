// projects_coord_reader.go — the first codeReader (coord_reader.go): a
// project's sandbox, for the project coordinator's research subagents
// (projects_coord_research.go; D222). Files are read with the coding
// tools' own readers (sandbox_fs.go) and history with a read-only git
// (projects_coord_research_git.go), in the sandbox the binding names — the
// project's, or a task's own. Opened again on every call: the subagent's
// root is still the project's coordinator, the sandbox still the
// project's, and the class's firewall allows it. Every path must resolve
// (symlinks followed, in the sandbox) inside the binding's roots: the
// project's directory, or an adopted repo's clone.
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

func init() {
	registerCodeReader(&codeReaderKind{
		Kind:  "",
		Caps:  codeCaps{Git: gitSubOrder},
		What:  "the project's code and its wiki",
		Files: sandboxFileSpecs,
		Open: func(ctx context.Context, ag *Agent, run *Run, cfg Config) (codeReader, error) {
			use, err := ag.codeReadUse(ctx, run, cfg)
			if err != nil {
				return nil, err
			}
			return &sbxCodeReader{use: use, b: cfg.CodeRead}, nil
		},
	})
}

// gitSpec is the sandbox reader's git tool.
func gitSpec() toolSpec { return codeGitSpec(gitSubOrder) }

// sbxCodeReader reads a sandbox as binding b allows.
type sbxCodeReader struct {
	use *sbxUse
	b   *CodeReadBinding
}

// resolve is p (def: "" is the working directory) as a real path inside
// the binding's roots.
func (r *sbxCodeReader) resolve(ctx context.Context, p string, def bool) (string, error) {
	if strings.TrimSpace(p) == "" && !def {
		return "", errors.New("path: the file")
	}
	at, err := r.use.path(p)
	if err != nil {
		return "", err
	}
	real, err := codeReadReal(ctx, r.use, at)
	if err != nil {
		return "", err
	}
	if !inRoots(real, r.b.Roots) {
		return "", fmt.Errorf("%s is outside the project's code (%s): research reads only there", at, strings.Join(r.b.Roots, ", "))
	}
	return real, nil
}

// file runs one of the coding tools' readers on args, its path resolved.
func (r *sbxCodeReader) file(ctx context.Context, args map[string]any, def bool,
	f func(context.Context, *sbxUse, map[string]any) (string, error)) (string, error) {
	p, err := r.resolve(ctx, str(args["path"]), def)
	if err != nil {
		return "", err
	}
	a := map[string]any{}
	for k, v := range args {
		a[k] = v
	}
	a["path"] = p
	return f(ctx, r.use, a)
}

func (r *sbxCodeReader) Read(ctx context.Context, args map[string]any) (string, error) {
	return r.file(ctx, args, false, sbxRead)
}

func (r *sbxCodeReader) Ls(ctx context.Context, args map[string]any) (string, error) {
	return r.file(ctx, args, true, sbxLs)
}

func (r *sbxCodeReader) Glob(ctx context.Context, args map[string]any) (string, error) {
	return r.file(ctx, args, true, sbxGlob)
}

func (r *sbxCodeReader) Grep(ctx context.Context, args map[string]any) (string, error) {
	return r.file(ctx, args, true, sbxGrep)
}

func (r *sbxCodeReader) Git(ctx context.Context, args map[string]any) (string, error) {
	dir, err := r.resolve(ctx, str(args["path"]), true)
	if err != nil {
		return "", err
	}
	return codeReadGit(ctx, r.use, dir, r.b.Roots, args["args"])
}

// codeReadUse is the sandbox a research subagent reads, as its binding names
// it — checked on every call: its root is still the project's coordinator,
// the sandbox still the project's (or one of its tasks'), and the class's
// firewall allows it. Called as the project's owner, as the worker is.
func (ag *Agent) codeReadUse(ctx context.Context, run *Run, cfg Config) (*sbxUse, error) {
	p, _, err := ag.researchRootOf(run, cfg) // its root, resolved as the project's coordinator
	if err != nil {
		return nil, err
	}
	b := cfg.CodeRead
	if b.Ref != p.SandboxRef {
		var n int
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND n=? AND sandbox_ref=?`, p.ID, b.Task, b.Ref).Scan(&n)
		if n == 0 {
			return nil, errors.New("the sandbox you were reading is no longer the project's: tell the coordinator, who can start research again")
		}
	}
	conn, id, err := sbxDialRef(b.Ref, sbxUserOf(binderWho(p.Owner)))
	if err != nil {
		return nil, err
	}
	hello, err := managerHello(ctx, conn.M)
	if err != nil {
		return nil, err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if why := partitionBoxRefusal(box); why != "" {
		return nil, &sbxError{Refusal: "not-allowed", Msg: why}
	}
	if why := taintRefusal(classOf(cfg), box); why != "" {
		return nil, &sbxError{Refusal: "not-allowed", Msg: why}
	}
	return &sbxUse{Conn: conn, ID: id, Binding: SandboxBinding{Ref: b.Ref, Name: box.Name, Cwd: b.Cwd}, Box: box, Hello: hello, Cwd: b.Cwd}, nil
}

// codeReadReal is p with every symlink on its way resolved, in the sandbox.
func codeReadReal(ctx context.Context, use *sbxUse, p string) (string, error) {
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Cmd: `realpath -e -- "$X" 2>/dev/null || readlink -e -- "$X"`, Env: map[string]string{"X": p},
		TimeoutMs: 20000, MaxOutput: 8 << 10})
	if err != nil {
		return "", err
	}
	if !res.ok() {
		return "", &sbxError{Refusal: "not-found", Msg: p + " doesn't exist in the sandbox"}
	}
	lines, _ := runLines(res.Stdout)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return "", fmt.Errorf("%s: the sandbox couldn't resolve it", p)
	}
	return path.Clean(lines[0]), nil
}

// inRoots: p is one of roots or inside one.
func inRoots(p string, roots []string) bool {
	for _, r := range roots {
		r = strings.TrimSuffix(r, "/")
		if r != "" && (p == r || strings.HasPrefix(p, r+"/")) {
			return true
		}
	}
	return false
}
