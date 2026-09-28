package main

// deployref.go — a tile ref on the wire (D127j): "<tile>+<name>" rides URL
// paths and JSON bodies as it is, and a query string as the tile and
// deployment= apart, since a '+' in a query string reads as a space. Reading
// the state (GET /deployments) for a ref, and the refs the read commands
// (bx status, bx logs, bx deployment log and diff) put into their queries.

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// getDeployState reads GET /deployments for a tile ref. A query string never
// carries the qualifier (D127j: a '+' there reads as a space), so a ref ending
// in "+<name>" asks for its tile with deployment=<name>. When that names no
// deployment of a tile with a record, the ref is a tile's own name holding
// '+' (an exact match wins: no new name may hold one, but older directories
// keep resolving), asked for as it is; failing that, the first answer
// stands.
func getDeployState(ref string) (*deployState, []byte, error) {
	tile, dep := splitRef(ref)
	st, b, err := readDeployState(tile, dep)
	var old *dcError
	if dep == "" || err == nil && st.Record || errors.As(err, &old) && old.code == exitNoDeployments {
		return st, b, err
	}
	if st2, b2, err2 := readDeployState(ref, ""); err2 == nil {
		return st2, b2, nil
	}
	return st, b, err
}

func readDeployState(tile, dep string) (*deployState, []byte, error) {
	q := url.Values{"tile": {tile}}
	if dep != "" {
		q.Set("deployment", dep)
	}
	b, err := dcCall("GET", "/api/xbin/deployments?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	st := &deployState{}
	if err := decodeAnswer(b, st); err != nil {
		return nil, nil, err
	}
	return st, b, nil
}

// splitRef splits a tile ref "<tile>+<name>" at its last segment's last '+'
// when a deployment name follows; any other ref is a tile with no name.
func splitRef(ref string) (tile, dep string) {
	i := strings.LastIndexByte(ref, '+')
	if i < 1 || ref[i-1] == '/' || strings.Contains(ref[i:], "/") || !dcNameRe.MatchString(ref[i+1:]) {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}

// queryTile is the tile and the deployment a query names for ref (D127j):
// splitRef's, resolved through the state when the ref has a qualifier, since
// a '+' may also be part of a tile's own name.
func queryTile(ref string) (tile, dep string, err error) {
	if _, d := splitRef(ref); d == "" {
		return ref, "", nil
	}
	st, _, err := getDeployState(ref)
	if err != nil {
		return "", "", err
	}
	return st.Tile, st.Selected, nil
}

// readRef is a read command's positional tile ref (bx status, bx logs):
// "<tile>+<name>" names that deployment of its tile, as a path would, and
// goes out as the tile and deployment= (D127j) — unless something in the
// workspace sits at the whole ref (a tile named with '+' before the rule:
// the exact match wins, with no request), or xbind knows no such
// deployment. dep "": the ref is a tile's own path.
func readRef(ref string) (tile, dep string) {
	if _, d := splitRef(ref); d == "" {
		return ref, ""
	}
	if ws := workspaceRoot(); ws != "" {
		if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(ref))); err == nil {
			return ref, ""
		}
	}
	if st, _, err := getDeployState(ref); err == nil && st.Selected != "" {
		return st.Tile, st.Selected
	}
	return ref, ""
}
