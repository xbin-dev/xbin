package proxy

// globaladdress.go — ?xbin-partition=global, F5 (plans/partitions/05 §6;
// PD-16, S3): a partitioned tile's own frames, terminals, agent sessions
// and user-partition backends address the tile's global instance, as the
// partition's person. The client's xbin.fetch(url, {partition: 'global'})
// and the Go SDK's xbin.GlobalURL(path) send it, from a user partition only.
//
// The proxy consumes the parameter on partitioned targets only — the
// backend never sees it there — and asks the broker's RouteGlobal instead of
// Route. On every other target it reads nothing: the parameter reaches the
// backend as any query parameter, as it always did, so a tile that isn't
// partitioned sees exactly today's requests. A value other than one
// "global" is 400; a path ticket's call 403 (a fixed-prefix credential for
// its own partition, which only the proxy can tell from its tile's frame);
// the rest is RouteGlobal's (the broker's partitionglobal.go).

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// errGlobalAddressValue is the 400 for ?xbin-partition with any value but
// one "global".
var errGlobalAddressValue = errors.New("?" + util.QueryPartition + " addresses a partitioned tile's global instance: its one value is global")

// errGlobalAddressTicket refuses F5 to a path ticket (S3).
var errGlobalAddressTicket = errors.New("a path ticket reaches its own partition only: ?" + util.QueryPartition + "=global needs the page's own frame token")

// decideAddressed is decide, unless r addresses comp's global instance
// (?xbin-partition=global on a partitioned target, consumed here): then
// RouteGlobal's answer, refused to path tickets and — fail closed — when
// no RouteGlobal is installed.
func (px *Proxy) decideAddressed(r *http.Request, p auth.Principal, comp *registry.Component, qualifier string) Decision {
	asked, err := consumeGlobalAddress(r, comp)
	switch {
	case err != nil:
		return Decision{Deny: err}
	case !asked:
		return px.decide(p, comp, qualifier)
	case auth.ViaPathTicket(r.Context()):
		return Decision{Deny: errGlobalAddressTicket}
	case px.RouteGlobal == nil:
		return Decision{Deny: fmt.Errorf("this xbind can't address %s's global instance", comp.Path)}
	}
	return px.RouteGlobal(p, comp, qualifier)
}

// consumeGlobalAddress takes ?xbin-partition off r when comp is
// partitioned — its recorded mode has user partitions, or can't be read
// (the partition gate then holds the tile) — and reports whether it asked
// for the global instance. On any other tile it reads nothing.
func consumeGlobalAddress(r *http.Request, comp *registry.Component) (bool, error) {
	if _, rec, _ := comp.PartitionState(); !rec.User && !comp.PartitionRecordUnknown() {
		return false, nil
	}
	q, _ := url.ParseQuery(r.URL.RawQuery) // what forward sends: the pairs that parse
	vals, ok := q[util.QueryPartition]
	if !ok {
		return false, nil
	}
	q.Del(util.QueryPartition)
	r.URL.RawQuery = q.Encode()
	if len(vals) != 1 || vals[0] != string(util.PartitionGlobal) {
		return false, errGlobalAddressValue
	}
	return true, nil
}

// denyStatus is the status of a refusal Route or RouteGlobal answered:
// 404 for a deployment or a global instance that doesn't exist, 400 for a
// malformed ?xbin-partition, 403 for everything else.
func denyStatus(err error) int {
	switch {
	case errors.Is(err, util.ErrNoDeployment), errors.Is(err, util.ErrNoGlobalInstance):
		return http.StatusNotFound
	case errors.Is(err, errGlobalAddressValue):
		return http.StatusBadRequest
	}
	return http.StatusForbidden
}
