package manifestmerge

// driver.go — how git reaches Merge: a merge driver (gitattributes(5)
// "Defining a custom merge driver") that xbind names in each builtin
// template instance's own repository — .git/config and .git/info/attributes,
// never a tracked file — and bx runs (bx template merge-manifest).

import "strings"

// DriverName is the driver's name in the repository's config and attributes.
const DriverName = "xbin-manifest"

// Attribute is the .git/info/attributes line that sends the instance's
// manifest (only the one at its root) to the driver.
const Attribute = "/xbin.json merge=" + DriverName

// DriverTitle is the driver's merge.<name>.name: what git calls it.
const DriverTitle = "xbin.json merged by keys (bx template merge-manifest; /docs/overview/03-components.md)"

// DriverPrefix starts every command Driver writes: a driver command that
// doesn't is the builder's own, which xbind leaves alone.
const DriverPrefix = "bx template merge-manifest "

// Driver is the driver's command (merge.<name>.driver) for an instance at
// path to made from a template whose own path is from (the same path for an
// instance at the template's): bx merges — git's line merge first, by keys
// only where that conflicts and only when the other side is the template —
// and when bx is missing, too old, or can't merge, git merge-file leaves
// git's own conflict markers, so a merge is never worse than without the
// driver. The rename is always named, quoted for the shell git runs the
// command with, so bx can tell a manifest that doesn't name its own path.
func Driver(from, to string) string {
	return DriverPrefix + "--marker-size %L --rename " + shellWord(from+"="+to) + " %O %A %B" +
		" || git merge-file --marker-size=%L -L ours -L base -L theirs %A %O %B"
}

// shellWord is s as one sh word — single-quoted, each ' in it closed,
// escaped and reopened — with every % doubled: git expands %-placeholders
// in a driver's command before the shell sees it, and %% is a %.
func shellWord(s string) string {
	return strings.ReplaceAll("'"+strings.ReplaceAll(s, "'", `'\''`)+"'", "%", "%%")
}
