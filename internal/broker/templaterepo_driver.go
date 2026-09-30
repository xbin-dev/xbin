package broker

// templaterepo_driver.go — the manifest's merge driver in every builtin
// template instance's repository (internal/manifestmerge; bx template
// merge-manifest; docs/overview/03-components.md §Templates).
//
// An instance takes its template's changes with `git merge template/main`
// (templaterepo.go). Its xbin.json was rewritten when it was made — by
// xbinds before this one re-marshalled, comments gone and keys sorted — so
// git's line merge conflicts on any manifest change upstream makes. xbind
// therefore names a merge driver for the instance's root xbin.json in the
// repository's own .git/config (merge.xbin-manifest.name/driver) and
// .git/info/attributes — never a tracked file, nothing a merge carries —
// when the instance is made (AddTemplateRemote), when one is copied (POST
// /clone: the copy's path), and, for every instance an older xbind made,
// at start. The driver keeps git's line merge where that is clean and
// merges by keys only where it conflicts and the other side is the
// template; without bx it is git's merge-file.
//
// It is the one write to existing instances' repositories T1 adds (an
// exception to plans/partitions/10 §A.1, recorded in records/T1.md), so
// the repository stays the builder's: xbind leaves the driver alone once
// the builder says so (manifestDriverWanted) — `git config
// xbin.manifestDriver false`, a driver command of their own under the
// name, a merge attribute of their own for xbin.json, or the attributes
// line xbind wrote removed (xbin.manifestDriver = true records that xbind
// wrote it, so a removed line stays removed).
//
// The writes run confined (D78): the repository is the builder's, written
// from sandboxes. xbind reads its config, attributes and .gitattributes
// only through fsutil.OpenBeneath, to learn whether the instance is one and
// wants the driver; an instance that has it current costs no run.

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/manifestmerge"
)

// templateRemoteURL is the `template` remote AddTemplateRemote writes for
// builtin template name.
func templateRemoteURL(name string) string {
	return "http://xbin/api/xbin/templates/" + name + ".git"
}

// manifestDriverKey is the config key recording that xbind named the
// driver (true), or the builder's opt-out (false).
const manifestDriverKey = "xbin.manifestDriver"

// manifestDriverScript names the driver in the repository in the working
// directory: $1 its title, $2 its command, $3 the attributes line.
const manifestDriverScript = `set -e
git -c safe.directory='*' config --local merge.` + manifestmerge.DriverName + `.name "$1"
git -c safe.directory='*' config --local merge.` + manifestmerge.DriverName + `.driver "$2"
f=.git/info/attributes
mkdir -p .git/info
if [ -s "$f" ] && [ -n "$(tail -c1 "$f")" ]; then echo >> "$f"; fi
grep -qxF "$3" "$f" 2>/dev/null || printf '%s\n' "$3" >> "$f"
git -c safe.directory='*' config --local ` + manifestDriverKey + ` true
`

// manifestDriver is the driver's command for the instance at tile of
// builtin template name: the template's own path renamed to tile's, as
// instantiation rewrote it.
func (b *Broker) manifestDriver(tile, name string) string {
	from := tile
	if b.templates != nil {
		if t, ok := b.templates.Get(name); ok && t.DefaultPath != "" {
			from = t.DefaultPath
		}
	}
	return manifestmerge.Driver(from, tile)
}

// ensureTemplateMergeDriver names the driver in the repository at dir —
// the tile at path tile — when that is a builtin template's instance whose
// repository wants it (manifestDriverWanted): one confined run, none when
// the driver is current or the builder declined it.
func (b *Broker) ensureTemplateMergeDriver(dir, tile string) error {
	if !isRepo(dir) {
		return nil
	}
	cfg := parseGitConfig(readRepoFile(dir, ".git/config"))
	name, ok := templateRemoteOf(cfg)
	if !ok {
		return nil
	}
	cmd := b.manifestDriver(tile, name)
	if !manifestDriverWanted(cfg, readRepoFile(dir, ".git/info/attributes"), readRepoFile(dir, ".gitattributes"), cmd) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := confine.Run(ctx, confine.Cmd{
		Argv: []string{"sh", "-c", manifestDriverScript, "sh", manifestmerge.DriverTitle, cmd, manifestmerge.Attribute},
		Dir:  dir,
	})
	return err
}

// maxDriverFailures is how many instances' runs may fail in one pass of
// EnsureTemplateMergeDrivers before the rest wait for the next start (the
// sandbox is then likely failing for all of them).
const maxDriverFailures = 3

// EnsureTemplateMergeDrivers names the driver in every builtin template
// instance's repository that lacks it or names an older command: the
// instances older xbinds made. Run at start, after EnsureComponentRepos,
// off the boot path (a failing sandbox costs a timeout per instance).
func (b *Broker) EnsureTemplateMergeDrivers() {
	failed := 0
	for _, c := range b.Reg.Components() {
		err := b.ensureTemplateMergeDriver(c.Dir, c.Path)
		if err == nil {
			continue
		}
		slog.Warn("template instance: the manifest's merge driver couldn't be set (its `git merge template/main` merges xbin.json by lines)",
			"instance", c.Path, "err", err)
		if failed++; failed >= maxDriverFailures {
			slog.Warn("template instances: the manifest's merge driver failed repeatedly; the rest are set at the next start")
			return
		}
	}
}

// manifestDriverWanted reports whether xbind (re)writes the driver, from
// the repository's config, .git/info/attributes (info) and .gitattributes
// (tracked): not when the builder declined it — the opt-out key, a driver
// command of their own under the name, the attributes line xbind wrote
// removed, a merge attribute of their own for xbin.json where the line
// isn't yet — and not when it is current.
func manifestDriverWanted(cfg map[string]string, info, tracked []byte, cmd string) bool {
	marker, marked := cfg[strings.ToLower(manifestDriverKey)]
	if marked && !gitTrue(marker) {
		return false
	}
	driver, named := cfg["merge."+manifestmerge.DriverName+".driver"]
	if named && !strings.HasPrefix(driver, manifestmerge.DriverPrefix) {
		return false
	}
	line := hasLine(info, manifestmerge.Attribute)
	if marked && !line {
		return false
	}
	if !line && (setsMergeAttr(tracked, "") || setsMergeAttr(info, manifestmerge.Attribute)) {
		return false
	}
	return !(marked && line && driver == cmd && cfg["merge."+manifestmerge.DriverName+".name"] == manifestmerge.DriverTitle)
}

// readRepoFile is dir/rel, read without following a link out of dir or
// blocking on a FIFO; nil when it can't be read. At most 1 MiB.
func readRepoFile(dir, rel string) []byte {
	f, err := fsutil.OpenBeneath(dir, filepath.FromSlash(rel))
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return b
}

// templateRemoteOf is the builtin template a repository's config names as
// its `template` remote (the URL AddTemplateRemote writes).
func templateRemoteOf(cfg map[string]string) (string, bool) {
	url := cfg["remote.template.url"]
	name, ok := strings.CutPrefix(url, "http://xbin/api/xbin/templates/")
	name, ok2 := strings.CutSuffix(name, ".git")
	if !ok || !ok2 || !templateNameOK(name) || templateRemoteURL(name) != url {
		return "", false
	}
	return name, true
}
