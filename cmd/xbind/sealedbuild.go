package main

// sealedbuild.go — `xbind sealed-build`, the release's builds of its
// sealed components' backends (D227, docs/maintenance.md → Sealed
// components): `make build` runs it with the host's go.

import (
	"flag"
	"fmt"
	"os"

	"github.com/xbin-dev/xbin/internal/sealed"
)

// sealedBuildMain is the subcommand: sealedBuild, or exit 1 saying why.
func sealedBuildMain(args []string) {
	if err := sealedBuild(args); err != nil {
		fatal("sealed-build: %v", err)
	}
}

// sealedBuild is `xbind sealed-build`: compile the backend of every Go
// component of the compiled-in table into --out as <name>-<digest>, for
// --os/--arch (default: the go command's), and remove what else is there;
// with sealed.json, the manifest of what each was built from and its hash;
// --list prints the file names a release's bin/sealed must hold instead,
// --check verifies --out holds exactly those, each the file its manifest
// lists and built against the SDK at --sdk.
func sealedBuild(args []string) error {
	fs := flag.NewFlagSet("sealed-build", flag.ContinueOnError)
	out := fs.String("out", "bin/sealed", "the directory the backends are built into")
	sdk := fs.String("sdk", "sdk", "the xbin Go SDK's directory")
	goos := fs.String("os", "", "target GOOS (default: the go command's)")
	goarch := fs.String("arch", "", "target GOARCH (default: the go command's)")
	list := fs.Bool("list", false, "print the file names bin/sealed must hold, and build nothing")
	check := fs.Bool("check", false, "verify --out holds exactly those files, as its sealed.json lists them and built against --sdk, and build nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set, err := sealed.Load(sealed.Table())
	if err != nil {
		return err
	}
	want := set.BuildNames()
	switch {
	case *list:
		for _, n := range want {
			fmt.Println(n)
		}
		return nil
	case *check:
		n, err := set.CheckBuilds(*out, *sdk, sealed.BuildOptions{GOOS: *goos, GOARCH: *goarch})
		if err != nil {
			return err
		}
		fmt.Printf("sealed-build: %s holds this source's %d backend(s), as its %s says\n", *out, n, sealed.ManifestName)
		return nil
	}
	have, err := set.Build(sealed.BuildOptions{Out: *out, SDK: *sdk, GOOS: *goos, GOARCH: *goarch, Log: os.Stderr})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sealed-build: %s holds %d backend(s)\n", *out, len(have))
	return nil
}
