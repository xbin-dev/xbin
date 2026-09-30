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
// when the instance is made and, for every instance an older xbind made,
// at start. The driver keeps git's line merge where that is clean and
// merges by keys where it conflicts; without bx it is git's merge-file.
//
// Both writes run confined (D78): the repository is the builder's,
// written from sandboxes. xbind reads its config only to learn whether the
// instance is one and already has the driver, through fsutil.OpenBeneath.

import (
	"bufio"
	"bytes"
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

// manifestDriverScript names the driver in the repository in the working
// directory: $1 its title, $2 its command, $3 the attributes line.
const manifestDriverScript = `set -e
git -c safe.directory='*' config --local merge.` + manifestmerge.DriverName + `.name "$1"
git -c safe.directory='*' config --local merge.` + manifestmerge.DriverName + `.driver "$2"
f=.git/info/attributes
mkdir -p .git/info
if [ -s "$f" ] && [ -n "$(tail -c1 "$f")" ]; then echo >> "$f"; fi
grep -qxF "$3" "$f" 2>/dev/null || printf '%s\n' "$3" >> "$f"
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

// ensureManifestDriver names the driver in instanceDir's repository
// (idempotent; one confined run).
func (b *Broker) ensureManifestDriver(instanceDir, tile, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := confine.Run(ctx, confine.Cmd{
		Argv: []string{"sh", "-c", manifestDriverScript, "sh",
			manifestmerge.DriverTitle, b.manifestDriver(tile, name), manifestmerge.Attribute},
		Dir: instanceDir,
	})
	return err
}

// EnsureTemplateMergeDrivers names the driver in every builtin template
// instance's repository that lacks it (or names an older command): the
// instances older xbinds made. Run at start, after EnsureComponentRepos;
// an instance already set up costs two small reads.
func (b *Broker) EnsureTemplateMergeDrivers() {
	for _, c := range b.Reg.Components() {
		if !isRepo(c.Dir) {
			continue
		}
		cfg := readRepoFile(c.Dir, ".git/config")
		name, has := templateRemoteOf(cfg)
		if !has || !templateNameOK(name) {
			continue
		}
		attrs := readRepoFile(c.Dir, ".git/info/attributes")
		if hasLine(cfg, "driver = "+b.manifestDriver(c.Path, name)) && hasLine(attrs, manifestmerge.Attribute) {
			continue
		}
		if err := b.ensureManifestDriver(c.Dir, c.Path, name); err != nil {
			slog.Warn("template instance: the manifest's merge driver couldn't be set (its `git merge template/main` merges xbin.json by lines)",
				"instance", c.Path, "template", name, "err", err)
		}
	}
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
func templateRemoteOf(cfg []byte) (string, bool) {
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(cfg))
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(ln, "[") {
			section = ln
			continue
		}
		if section != `[remote "template"]` {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok || strings.TrimSpace(k) != "url" {
			continue
		}
		url := strings.TrimSpace(v)
		name, ok := strings.CutPrefix(url, "http://xbin/api/xbin/templates/")
		if name, ok2 := strings.CutSuffix(name, ".git"); ok && ok2 && templateRemoteURL(name) == url {
			return name, true
		}
	}
	return "", false
}

// hasLine reports whether a line of b is line (blanks around it aside).
func hasLine(b []byte, line string) bool {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == line {
			return true
		}
	}
	return false
}
