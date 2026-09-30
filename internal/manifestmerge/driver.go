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

// Driver is the driver's command (merge.<name>.driver) for an instance at
// path to made from a template whose own path is from: bx merges — git's
// line merge first, by keys only where that conflicts — and when bx is
// missing, too old, or can't merge, git merge-file leaves git's own
// conflict markers, so a merge is never worse than without the driver.
func Driver(from, to string) string {
	rename := ""
	if from != to && pathWord(from) && pathWord(to) {
		rename = " --rename " + from + "=" + to
	}
	return "bx template merge-manifest --marker-size %L" + rename + " %O %A %B" +
		" || git merge-file --marker-size=%L -L ours -L base -L theirs %A %O %B"
}

// pathWord reports whether p is a component path safe to put on a shell
// command line unquoted.
func pathWord(p string) bool {
	if p == "" {
		return false
	}
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-", c)) {
			return false
		}
	}
	return true
}
