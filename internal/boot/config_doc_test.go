package boot

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFlagSet() *flag.FlagSet { return flag.NewFlagSet("xbind", flag.ContinueOnError) }

// docs/config.md is rendered from Config's tags — the one list of flags and
// environment variables the daemon reads. This test fails when the page is
// stale; UPDATE_DOCS=1 rewrites it.
func TestConfigDoc(t *testing.T) {
	want := renderConfigDoc()
	path := filepath.Join("..", "..", "docs", "config.md")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		if os.Getenv("UPDATE_DOCS") != "" {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatalf("docs/config.md is stale (or missing): it is generated from internal/boot.Config — run\n  UPDATE_DOCS=1 go test ./internal/boot -run TestConfigDoc")
	}
}

func renderConfigDoc() string {
	var b strings.Builder
	b.WriteString(`# xbind configuration

Every setting the daemon reads, in one place. This page is generated from
the daemon's configuration struct (` + "`internal/boot.Config`" + `; ` + "`make check`" + ` fails
when it is stale), so it is complete by construction.

**Precedence:** a command-line flag beats its environment variable, which
beats the default. A flag's default *is* the environment variable's value
when one is set (` + "`xbind -h`" + ` shows the resolved defaults). Boolean flags have
no environment form. Settings marked *read by* are consulted by that
package where they are used, not at boot; they are listed here so nothing
is undocumented.

` + "```" + `
xbind init <dir>     scaffold a workspace (also happens automatically on an
                     empty --workspace)
xbind [flags]        serve a workspace
xbind version
` + "```" + `

| Setting | Flag | Environment | Default | Read by | What it does |
|---|---|---|---|---|---|
`)
	for _, s := range Settings() {
		flagCol, envCol, def, readBy := "", "", "", "boot"
		if s.Flag != "" {
			flagCol = "`--" + s.Flag + "`"
		}
		if s.Env != "" {
			envCol = "`" + s.Env + "`"
		}
		switch {
		case s.Bool:
			def = "off"
		case s.Default != "":
			def = "`" + s.Default + "`"
		}
		if s.ReadBy != "" {
			readBy = "`" + s.ReadBy + "`"
		}
		doc := strings.ReplaceAll(s.Doc, "|", "\\|")
		if s.Secret {
			doc += " *(secret: never logged)*"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", s.Field, flagCol, envCol, def, readBy, doc)
	}
	b.WriteString(`
The vault's boot mode follows from these settings, first match wins:
` + "`XBIN_VAULT_PASSPHRASE`" + ` set → auto-unseal; ` + "`--insecure-vault`" + ` or ` + "`--no-auth`" + ` →
plaintext at rest; ` + "`--dev`" + ` → a built-in dev key (insecure); otherwise the
daemon starts sealed (a barrier exists) or locked (none yet) until an admin
unseals it — see [auth.md](/docs/auth.md).
`)
	return b.String()
}
