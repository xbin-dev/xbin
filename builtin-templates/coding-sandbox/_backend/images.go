// images.go — images are the substrate's base plus a setup script. The
// first sandbox of an image builds it: a template sandbox of its own
// (img-<id>-…) is made and prepared, the script runs in it as root, the
// sandbox stops and is snapshotted. Every later sandbox of the image clones
// that snapshot (the substrate's clone capability). A changed script, mode
// or an outdated base rebuilds it at the next use; the old template sandbox
// goes only once the new one is ready. A rebuild that fails keeps the
// previous good build (builtImage.Previous), which serves while it is
// current for the script and the mode. Builds are one at a time per image,
// and a creation waiting on one shows `creating`.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// buildJob is an image build under way; its waiters share the result.
type buildJob struct {
	done chan struct{}
	img  *builtImage
	err  error
}

// setupHash names what a build of im is: its script, and its sudo (the grant
// is in the build's snapshot). Without sudo it is the script's alone, as
// before sudo was: an image's existing build stays current.
func setupHash(im Image) string {
	if im.Sudo {
		return hashOf([]string{im.Setup, "sudo"})
	}
	return hashOf([]string{im.Setup})
}

// usableBuild is the build of b that a sandbox of the image clones for
// setup hash h in mode: b itself when it is ready and current, else the
// previous good build it keeps (a rebuild under way, or one that failed)
// when that one is current. nil: none is.
func usableBuild(b *builtImage, h, mode string) *builtImage {
	if b == nil {
		return nil
	}
	if b.State == "ready" && b.SetupHash == h && b.Mode == mode {
		cp := *b
		cp.Previous = nil
		return &cp
	}
	if p := b.Previous; p != nil && p.State == "ready" && p.SetupHash == h && p.Mode == mode {
		cp := *p
		cp.ID, cp.Previous = b.ID, nil
		return &cp
	}
	return nil
}

// lastGood is the good build a new build of the image keeps until it is
// ready: the current one when it is ready, else the one it kept.
func lastGood(b *builtImage) *builtImage {
	switch {
	case b == nil:
		return nil
	case b.State == "ready":
		return &builtImage{ID: b.ID, Runtime: b.Runtime, Snapshot: b.Snapshot, SetupHash: b.SetupHash, Mode: b.Mode,
			State: "ready", Started: b.Started, Built: b.Built}
	}
	return b.Previous
}

// failedRecently: b's last build failed less than a minute ago.
func failedRecently(b *builtImage) bool {
	return b != nil && b.State == "error" && time.Since(time.UnixMilli(b.Started)) < time.Minute
}

// imageReady: a build of im is current for mode, and none is under way (a
// sandbox of it is a clone made now).
func (m *Manager) imageReady(im Image, mode string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.imgs[im.ID]
	return b != nil && b.State != "building" && m.builds[im.ID] == nil && usableBuild(b, setupHash(im), mode) != nil
}

// imageFor is im's built image for mode, building it first when it is
// missing, stale or its base is outdated. It waits for a build under way.
// When a rebuild fails, the previous good build serves while it is current
// for the script and the mode (an outdated base, an operator's rebuild).
func (m *Manager) imageFor(ctx context.Context, im Image, mode string) (*builtImage, error) {
	h := setupHash(im)
	m.mu.Lock()
	b := m.imgs[im.ID]
	var cur builtImage
	if b != nil {
		cur = *b
	}
	use := usableBuild(b, h, mode)
	job := m.builds[im.ID]
	m.mu.Unlock()
	if job == nil && use != nil {
		in, err := m.backend().Get(ctx, use.Runtime)
		switch {
		case err == nil && (!in.Base.Outdated || failedRecently(&cur)):
			return use, nil // current (or its rebuild for a newer base failed a moment ago)
		case err != nil && !isNotFound(err):
			return nil, err
		}
	}
	if job == nil && b != nil && cur.SetupHash == h && failedRecently(&cur) {
		return nil, fmt.Errorf("its last build failed a moment ago: %s", cur.Detail)
	}
	built, err := m.startBuild(ctx, im, mode, false)
	if err != nil && ctx.Err() == nil {
		m.mu.Lock()
		prev := usableBuild(m.imgs[im.ID], h, mode)
		m.mu.Unlock()
		if prev != nil {
			if _, gerr := m.backend().Get(ctx, prev.Runtime); gerr == nil {
				m.logf("image %s: the rebuild failed; its previous build serves (%v)", im.ID, err)
				return prev, nil
			}
		}
	}
	return built, err
}

func isNotFound(err error) bool { return errors.Is(err, xbin.ErrSandboxNotFound) }

// startBuild builds im (or joins the build under way) and waits for it.
func (m *Manager) startBuild(ctx context.Context, im Image, mode string, detach bool) (*builtImage, error) {
	m.mu.Lock()
	job := m.builds[im.ID]
	if job == nil {
		job = &buildJob{done: make(chan struct{})}
		m.builds[im.ID] = job
		m.goBG(func(bctx context.Context) {
			job.img, job.err = m.build(bctx, im, mode)
			m.mu.Lock()
			delete(m.builds, im.ID)
			m.mu.Unlock()
			close(job.done)
		})
	}
	m.mu.Unlock()
	if detach {
		return nil, nil
	}
	select {
	case <-job.done:
		return job.img, job.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// imageRuntimeName is a template sandbox's name: img-<slug>-<random>.
func imageRuntimeName(id string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(id), "-"), "-")
	if len(s) > 16 {
		s = s[:16]
	}
	return "img-" + orStr(s, "x") + "-" + randHex(3)
}

// build makes im's template sandbox, runs its setup and snapshots it.
func (m *Manager) build(ctx context.Context, im Image, mode string) (out *builtImage, err error) {
	rt, err := m.runtime(ctx)
	if err != nil {
		return nil, err
	}
	be := m.backend()
	cfg := m.config()
	b := &builtImage{ID: im.ID, Runtime: imageRuntimeName(im.ID), SetupHash: setupHash(im), Mode: mode, State: "building", Started: now()}
	m.mu.Lock()
	old := m.imgs[im.ID]
	prev := lastGood(old) // kept (and serving, while current) until this build is ready
	b.Previous = prev
	m.saveImage(b)
	m.mu.Unlock()
	m.logf("image %s: building in %s", im.ID, b.Runtime)
	defer func() {
		if err != nil && m.ctx.Err() != nil {
			return // the manager is stopping: the next one sees "building" and starts over (resume)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		var gone []string
		if old != nil && old.State != "ready" && old.Runtime != "" && old.Runtime != b.Runtime && (prev == nil || old.Runtime != prev.Runtime) {
			gone = append(gone, old.Runtime) // an attempt before this one that failed or was cut short: never a good build
		}
		if err == nil && prev != nil && prev.Runtime != b.Runtime {
			gone = append(gone, prev.Runtime) // this one is ready: the previous build goes (its clones are copies)
		}
		if err != nil {
			// what a failed build says reaches consumers (their sandbox's
			// stateDetail): never with the build sandbox's runtime name
			err = hideName(err, b.Runtime, "image:"+im.ID)
			b.State, b.Detail = "error", errText(err)
			m.saveImage(b) // b.Previous stays: the last good build is kept
			m.logf("image %s: %v", im.ID, err)
			gone = append(gone, b.Runtime) // and what was made of this one
		}
		if len(gone) > 0 {
			go func() {
				dctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				for _, name := range gone {
					_ = be.Delete(dctx, name)
				}
			}()
		}
	}()
	size, _ := cfg.size("")
	egress := im.BuildEgress
	if egress == "" {
		egress = "none"
		if contains(offeredEgress(rt), "internet") {
			egress = "internet"
		}
	}
	lay := m.layout(rt)
	tpl := record{ID: "image:" + im.ID, Name: "image " + im.ID, Workdir: lay.Workdir, Home: lay.Home, User: lay.User,
		UID: lay.UID, GID: lay.GID, Shell: lay.Shell, Sudo: im.Sudo && sudoWorks(rt, mode)}
	info, err := be.Create(ctx, xbin.SandboxSpec{Name: b.Runtime, Mode: mode, MemMiB: size.MemMiB, VCPUs: size.VCPUs, DiskGiB: size.DiskGiB,
		Net: &xbin.SandboxNet{Egress: egressClass(egress)}, Defaults: defaultsOf(tpl),
		Labels: map[string]string{"coding-sandbox/image": im.ID}, ClientID: b.Runtime})
	if err != nil {
		return nil, err
	}
	if info.Defaults.Cwd != "" {
		tpl.Workdir = info.Defaults.Cwd
	}
	if h := info.Defaults.Env["HOME"]; h != "" {
		tpl.Home = h
	}
	if info.Defaults.UID != nil && info.Defaults.GID != nil {
		tpl.UID, tpl.GID = *info.Defaults.UID, *info.Defaults.GID
	}
	tpl.Runtime = b.Runtime
	if _, err := be.Start(ctx, b.Runtime, m.waitMax(ctx)); err != nil {
		return nil, err
	}
	if err := m.prepare(ctx, tpl); err != nil {
		return nil, err
	}
	box := be.Sandbox(b.Runtime)
	root := 0
	x, err := box.Exec(ctx, xbin.ExecRequest{Cmd: im.Setup, Cwd: tpl.Workdir, UID: &root, GID: &root, Label: "image setup: " + im.ID,
		Env: map[string]string{"IMAGE_ID": im.ID, "SANDBOX_USER": tpl.User, "SANDBOX_HOME": tpl.Home, "SANDBOX_WORKDIR": tpl.Workdir}})
	if err != nil {
		return nil, err
	}
	log, ch, err := follow(ctx, box, x.ID, 16<<10)
	b.Log = log
	if err != nil {
		return nil, err
	}
	if ch.State != "exited" || ch.ExitCode == nil || *ch.ExitCode != 0 {
		how := "was " + ch.State
		if ch.ExitCode != nil {
			how = fmt.Sprintf("exited %d", *ch.ExitCode)
		} else if ch.Signal != "" {
			how = "was killed by " + ch.Signal
		}
		return nil, fmt.Errorf("the setup script %s: %s", how, lastLines(log, 6))
	}
	if _, err := be.Stop(ctx, b.Runtime, m.waitMax(ctx)); err != nil {
		return nil, err
	}
	snap, err := box.Snapshot(ctx, "image "+im.ID, b.Runtime)
	if err == nil {
		snap, err = settleSnapshot(ctx, box, snap) // a large image copies past the substrate's wait
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	b.Snapshot, b.State, b.Detail, b.Built, b.Previous = snap.ID, "ready", "", now(), nil
	m.saveImage(b)
	cp := *b
	m.mu.Unlock()
	m.logf("image %s: built (%s @ %s)", im.ID, b.Runtime, snap.ID)
	return &cp, nil
}

// saveImage stores b (m.mu held).
func (m *Manager) saveImage(b *builtImage) {
	cp := *b
	if err := m.st.putImage(&cp); err != nil {
		m.logf("image %s: %v", b.ID, err)
	}
	m.imgs[b.ID] = &cp
}

// follow reads exec id's output to its end, keeping the last keep bytes.
func follow(ctx context.Context, box Box, id string, keep int) (string, *xbin.OutputChunk, error) {
	var tail []byte
	since := int64(0)
	for {
		ch, err := box.Output(ctx, id, xbin.OutputQuery{Since: since, WaitMs: 30000, Encoding: "base64"})
		if err != nil {
			return string(tail), nil, err
		}
		if b, err := base64.StdEncoding.DecodeString(ch.Data); err == nil {
			tail = append(tail, b...)
			if len(tail) > keep {
				tail = tail[len(tail)-keep:]
			}
		}
		if ch.End > since {
			since = ch.End
		}
		if ch.State != "running" && ch.End >= ch.Total {
			return strings.ToValidUTF8(string(tail), "�"), ch, nil
		}
		if ch.End <= ch.Start && ch.State == "running" {
			select { // a substrate that answered at once: don't spin
			case <-ctx.Done():
				return string(tail), nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// rebuildImage starts a build of image id now (operators), not waiting.
func (m *Manager) rebuildImage(ctx context.Context, id string) error {
	im, ok := m.config().image(id)
	if !ok || id == "" {
		return errf(http.StatusNotFound, "not-found", "no image %s", quote(id))
	}
	if im.Setup == "" {
		return errf(http.StatusBadRequest, "invalid", "the image %s has no setup script: nothing to build", id)
	}
	rt, err := m.runtime(ctx)
	if err != nil {
		return err
	}
	if !contains(contractCaps(rt), "clone") || !contains(contractCaps(rt), "snapshots") {
		return errf(http.StatusNotImplemented, "unsupported", "building images needs the substrate's snapshots and clones")
	}
	mode, err := m.chooseMode(rt)
	if err != nil {
		return err
	}
	_, err = m.startBuild(ctx, im, mode, true)
	return err
}
