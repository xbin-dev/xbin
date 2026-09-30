package deps

import (
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goStd is the host toolchain's standard library, as the runner asks it.
func goStd(imp string) bool {
	if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") {
		return false
	}
	fi, err := os.Stat(filepath.Join(build.Default.GOROOT, "src", filepath.FromSlash(imp)))
	return err == nil && fi.IsDir()
}

// xsys is golang.org/x/sys as xbin's own go.sum pins it — a version the
// host's module cache holds (xbin builds with it) — and its go.sum lines.
func xsys(t *testing.T) (version, sums string) {
	t.Helper()
	b, err := os.ReadFile("../../go.sum")
	if err != nil {
		t.Skip("no go.sum of xbin's:", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "golang.org/x/sys" && !strings.HasSuffix(f[1], "/go.mod") {
			version = f[1]
		}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "golang.org/x/sys" && strings.TrimSuffix(f[1], "/go.mod") == version {
			sums += l + "\n"
		}
	}
	if version == "" {
		t.Skip("xbin's go.sum pins no golang.org/x/sys")
	}
	return version, sums
}

// goBuildRun builds dir's ./backend with w's go.work, offline, and runs it:
// the binary's output, or the build's when it fails.
func goBuildRun(t *testing.T, gobin string, w Work, dir string) (string, error) {
	t.Helper()
	wd := t.TempDir()
	gw := filepath.Join(wd, "go.work")
	if err := os.WriteFile(gw, w.GoWork, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(wd, "bin")
	cmd := exec.Command(gobin, "build", "-o", bin, "./backend")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK="+gw, "GOPROXY=off", "GOTOOLCHAIN=local", "GOFLAGS=", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return string(out), err
	}
	out, err := exec.Command(bin).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// covers D166 — the review's hijacks, each built by the go command itself:
// a tile declaring a module path at or beneath one the victim's go.mod
// requires published (golang.org/x/sys/unix with a replace hiding x/sys's
// own package, golang.org/x), beneath the SDK's
// (github.com/xbin-dev/xbin/sdk/sandboxcontract — every agent tile imports
// an SDK sub-package), beneath a dotless module the victim requires
// (calendar/store), the path of a module the victim replaces with its own
// directory or another tile's (example.com/lib, example.com/calendar), or
// beneath the victim's own (b/store) — and the same reached through a
// module the victim uses. The victim compiles its own choice every time:
// "real", never "EVIL". A dotless import two modules could provide
// (calendar/store, no require) builds neither, and says so.
func TestBuildWorkSquatsBuild(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go")
	}
	sysVer, sysSums := xsys(t)
	evilUnix := map[string]string{ // replaces x/sys with a module lacking unix/, so the namesake alone provides it
		"go.mod":         "module golang.org/x/sys/unix\n\ngo 1.24\n\nreplace golang.org/x/sys => ./fakesys\n",
		"u.go":           "package unix\n\nfunc Getpid() int { return -4242 }\n",
		"fakesys/go.mod": "module golang.org/x/sys\n\ngo 1.24\n",
		"fakesys/doc.go": "package sys\n",
	}
	evilX := map[string]string{
		"go.mod":         "module golang.org/x\n\ngo 1.24\n\nreplace golang.org/x/sys => ./fakesys\n",
		"sys/unix/u.go":  "package unix\n\nfunc Getpid() int { return -4242 }\n",
		"fakesys/go.mod": "module golang.org/x/sys\n\ngo 1.24\n",
		"fakesys/doc.go": "package sys\n",
		"other/o.go":     "package other\n",
	}
	who := func(pkg string) string {
		return "package main\n\nimport (\n\t\"fmt\"\n\n\tp \"" + pkg + "\"\n)\n\nfunc main() { fmt.Println(p.Who) }\n"
	}
	store := func(path, who string) map[string]string {
		return map[string]string{"go.mod": "module " + path + "\n\ngo 1.24\n", "s.go": "package store\n\nconst Who = \"" + who + "\"\n"}
	}
	for _, tc := range []struct {
		name   string
		victim map[string]string
		tiles  map[string]map[string]string // other tiles beside the victim
		want   string                       // the victim's output; "" = the build fails
		hint   string                       // in the failed build's hint
	}{
		{
			name: "x/sys/unix under a published require",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n\nrequire golang.org/x/sys " + sysVer + "\n",
				"go.sum":          sysSums,
				"backend/main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"golang.org/x/sys/unix\"\n)\n\nfunc main() { fmt.Println(unix.Getpid() > 0) }\n",
			},
			tiles: map[string]map[string]string{"evilunix": evilUnix, "evilx": evilX},
			want:  "true",
		},
		{
			name: "x/sys/unix through a module the victim uses",
			victim: map[string]string{
				// pid, imported only (a require of a workspace module at
				// v0.0.0 fails once the go command loads the whole graph:
				// "pid@v0.0.0: module lookup disabled"), imports x/sys/unix
				// on the strength of b's requirement
				"go.mod":          "module b\n\ngo 1.24\n\nrequire golang.org/x/sys " + sysVer + "\n",
				"go.sum":          sysSums,
				"backend/main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"pid\"\n)\n\nfunc main() { fmt.Println(pid.Pid() > 0) }\n",
			},
			tiles: map[string]map[string]string{
				"pid":      {"go.mod": "module pid\n\ngo 1.24\n", "p.go": "package pid\n\nimport \"golang.org/x/sys/unix\"\n\nfunc Pid() int { return unix.Getpid() }\n"},
				"evilunix": evilUnix, "evilx": evilX,
			},
			want: "true",
		},
		{
			name: "an SDK sub-package",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
				"backend/main.go": who("github.com/xbin-dev/xbin/sdk/sandboxcontract"),
			},
			tiles: map[string]map[string]string{
				"evilsdk":    {"go.mod": "module github.com/xbin-dev/xbin/sdk/sandboxcontract\n\ngo 1.24\n", "c.go": "package sandboxcontract\n\nconst Who = \"EVIL\"\n"},
				"evilprefix": {"go.mod": "module github.com/xbin-dev/xbin\n\ngo 1.24\n", "sdk/sandboxcontract/c.go": "package sandboxcontract\n\nconst Who = \"EVIL\"\n", "x.go": "package xbin\n"},
			},
			want: "real",
		},
		{
			name: "calendar/store, calendar required",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n\nrequire calendar v0.0.0\n",
				"backend/main.go": who("calendar/store"),
			},
			tiles: map[string]map[string]string{
				"calendar": {"go.mod": "module calendar\n\ngo 1.24\n", "store/s.go": "package store\n\nconst Who = \"real\"\n"},
				"evil":     store("calendar/store", "EVIL"),
			},
			want: "real",
		},
		{
			name: "calendar/store, imported only",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n",
				"backend/main.go": who("calendar/store"),
			},
			tiles: map[string]map[string]string{
				"calendar": {"go.mod": "module calendar\n\ngo 1.24\n", "store/s.go": "package store\n\nconst Who = \"real\"\n"},
				"evil":     store("calendar/store", "EVIL"),
			},
			hint: "more than one of the workspace's Go modules could provide calendar/store (calendar, calendar/store)",
		},
		{
			name: "a placeholder with its own replace",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ./lib\n",
				"lib/go.mod":      "module example.com/lib\n\ngo 1.24\n",
				"lib/l.go":        "package lib\n\nconst Who = \"real\"\n",
				"backend/main.go": who("example.com/lib"),
			},
			tiles: map[string]map[string]string{"evil": {"go.mod": "module example.com/lib\n\ngo 1.24\n", "l.go": "package lib\n\nconst Who = \"EVIL\"\n"}},
			want:  "real",
		},
		{
			name: "a replace with another tile's directory",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n\nrequire example.com/calendar v0.0.0\n\nreplace example.com/calendar => ../calendar\n",
				"backend/main.go": who("example.com/calendar/store"),
			},
			tiles: map[string]map[string]string{
				"calendar": {"go.mod": "module example.com/calendar\n\ngo 1.24\n", "store/s.go": "package store\n\nconst Who = \"real\"\n"},
				"evil":     {"go.mod": "module example.com/calendar\n\ngo 1.24\n", "store/s.go": "package store\n\nconst Who = \"EVIL\"\n"},
				"evil2":    store("example.com/calendar/store", "EVIL"),
			},
			want: "real",
		},
		{
			name: "a package of the victim's own",
			victim: map[string]string{
				"go.mod":          "module b\n\ngo 1.24\n",
				"store/s.go":      "package store\n\nconst Who = \"real\"\n",
				"backend/main.go": who("b/store"),
			},
			tiles: map[string]map[string]string{"evil": store("b/store", "EVIL")},
			want:  "real",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sdk := filepath.Join(root, "sdk")
			wsFiles(t, sdk, map[string]string{
				"go.mod":               "module github.com/xbin-dev/xbin/sdk\n\ngo 1.24\n",
				"sdk.go":               "package xbin\n",
				"sandboxcontract/c.go": "package sandboxcontract\n\nconst Who = \"real\"\n",
			})
			files := map[string]string{}
			for rel, s := range tc.victim {
				files["apps/b/"+rel] = s
			}
			var others []Module
			for tile, fs := range tc.tiles {
				for rel, s := range fs {
					files["apps/"+tile+"/"+rel] = s
				}
				others = append(others, wsModule(root, "apps/"+tile))
			}
			wsFiles(t, root, files)
			w := BuildWork(Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Others: others, SDK: sdk, Std: goStd})
			out, err := goBuildRun(t, gobin, w, filepath.Join(root, "apps/b"))
			if strings.Contains(out, "EVIL") || strings.Contains(out, "-4242") || strings.Contains(out, "false") {
				t.Fatalf("HIJACK: another tile's code in the victim: %q\n%s", out, w.GoWork)
			}
			if tc.want != "" {
				if err != nil || out != tc.want {
					t.Fatalf("the victim: %q %v, want %q\n%s", out, err, tc.want, w.GoWork)
				}
				return
			}
			if err == nil {
				t.Fatalf("the build succeeded: %q\n%s", out, w.GoWork)
			}
			if h := w.Hint(out); !strings.Contains(h, tc.hint) {
				t.Errorf("hint for\n%s:\n%s", out, h)
			}
		})
	}
}

// covers D166 — a module whose go.mod says `go 1.24.0` (what `go mod init`
// writes) builds: its build's go.work isn't older than it.
func TestBuildWorkGoLineBuild(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go")
	}
	root := t.TempDir()
	wsFiles(t, root, map[string]string{
		"apps/b/go.mod":          "module b\n\ngo 1.24.0\n",
		"apps/b/backend/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"ok\") }\n",
	})
	w := BuildWork(Build{Tile: "apps/b", Own: []Module{wsModule(root, "apps/b")}, Std: goStd})
	if out, err := goBuildRun(t, gobin, w, filepath.Join(root, "apps/b")); err != nil || out != "ok" {
		t.Fatalf("%q %v\n%s", out, err, w.GoWork)
	}
}
