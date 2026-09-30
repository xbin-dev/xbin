package broker

// partitionglobal.go — F5: a partitioned tile's own principals addressing
// its global instance (plans/partitions/05 §6, 02 §4 rule 2's exception;
// PD-16 decided, S3). A self-call never leaves its partition, except this
// one way: a call carrying ?xbin-partition=global — which the proxy
// consumes on partitioned targets only — from the tile's own frames,
// terminals, agent sessions or user-partition backends (or an admin calling
// in person: Route's rule 4 grants a person nothing on /api/<tile>/ by
// themselves) reaches the tile's global instance, as the partition's
// PERSON:
//
//   - the call is attributed (Decision.Attribute, which the proxy's
//     identifyPartition applies): X-XBin-User is the person,
//     X-XBin-User-Level their level on the tile read live, and X-XBin-Role
//     clamped to that level — reader for read, writer for write or
//     terminal — never the self-call's admin, whatever the credential (a
//     user partition's instance token included). X-XBin-From stays the
//     tile, and X-XBin-Partition names user:<id> with its id, so global
//     can tell a person's partition from the tile itself;
//   - a person calling directly — an admin, the only person Route lets
//     call a tile's API by themselves (rule 3) — keeps their own identity
//     and role (they are the person already; the clamp is about the tile's
//     code acting for someone); only the instance they reach changes;
//   - a view-as credential (Impersonator set) is read-only: X-XBin-Role is
//     reader whatever the viewed person's level, since a WebSocket upgrade
//     is a GET and the read-only gate refuses only other methods;
//   - a principal already in global (the global instance, the owner
//     token's frames and terminals, the root token) and a non-primary
//     deployment's (whose one instance is global, PD-17) reach it as
//     without the parameter;
//   - refused: every other tile (403), cron, bus and mail deliveries (403:
//     no person drives them, 02 §5), and — the proxy's refusal, since only
//     it can tell one — path tickets (a fixed-prefix credential for its own
//     partition). A tile without a global instance answers 404
//     (util.ErrNoGlobalInstance).
//
// One way only: nothing addresses a user partition by the URL.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// GlobalAddressFeature is the feature word of F5 — xbind consumes
// ?xbin-partition=global and routes it by RouteGlobal — for the features
// list of GET /api/xbin/partitions (06 §6), whose pack lists it.
const GlobalAddressFeature = "global-address/1"

// RouteGlobal is Route for a call to target that carries
// ?xbin-partition=global, which the proxy consumed because target is
// partitioned (F5, the file comment). Route's deployment rules apply
// unchanged; only the partition reached, and for the tile's own credentials
// the identity, change.
func (b *Broker) RouteGlobal(p auth.Principal, target *registry.Component, qualifier string) Decision {
	t := target.Path
	spec, partitioned, err := b.tilePartitioning(t)
	if err != nil || !partitioned {
		// a mode that can't be read (the proxy's partition gate answers
		// 409), or one that changed under the call: Route's own answer
		return b.Route(p, target, qualifier)
	}
	if kind := deliveryOf(p); kind != "" {
		return Decision{Deny: fmt.Errorf("a %s delivery acts in the partition it was registered for: ?xbin-partition=global is for %s's own frames, terminals, agent sessions and backends", kind, t)}
	}
	own := p.Component != "" && p.Component == t
	if p.Component != "" && !own {
		return Decision{Deny: fmt.Errorf("?xbin-partition=global addresses a tile's own global instance: %s can't use it on %s", p.Component, t)}
	}
	d := b.routeDeployment(p, target, qualifier)
	if d.Deny != nil || !b.isPrimary(t, d.Deployment) {
		// refused as without the parameter, or a non-primary deployment's
		// one instance, which is global already (PD-17)
		return b.routePartition(p, target, d)
	}
	person, err := b.globalAddressPerson(p, own, t, spec)
	switch {
	case err != nil:
		return Decision{Deny: err}
	case person == "" && !own && !p.Owner:
		return b.routePartition(p, target, d) // nobody signed in: refused as without the parameter
	case !spec.Global:
		return Decision{Deny: util.NoGlobalInstance(t)}
	case person == "":
		return b.routePartition(p, target, d) // in global already
	}
	part := util.UserPartition(person)
	id, err := b.partitionID(part)
	if err != nil {
		return Decision{Deny: err}
	}
	d.Partition, d.CallerPartition, d.CallerPartitionID, d.Delivery = util.PartitionGlobal, part, id, ""
	if own {
		level := b.personLevel(person, t)
		d.Role = attributedRole(level)
		d.Attribute = &auth.Attribution{UserID: person, Level: level, Role: d.Role}
	}
	if p.ReadOnly() { // view-as: read-only at global, its sockets included
		d.Role = "reader"
		if d.Attribute != nil {
			d.Attribute.Role = d.Role
		}
	}
	return d
}

// globalAddressPerson is the person an F5 call by p acts for — the one
// whose partition of tile p acts in — checked live (PD-20): a user
// partition's instance token's registered person; the person behind the
// tile's frame, terminal or agent session (view-as included: the viewed
// person, and the call stays read-only — RouteGlobal's reader); a person
// calling directly (an admin: rule 4 refuses everyone else first). ""
// with no error: no person (the global instance, the owner token's
// credentials, the root token, nobody signed in).
func (b *Broker) globalAddressPerson(p auth.Principal, own bool, tile string, spec registry.PartitionSpec) (string, error) {
	var part util.Partition
	switch {
	case own && p.Via == "instance":
		if !p.Partition.IsUser() {
			return "", nil
		}
		var err error
		if part, err = b.registeredPartition(p.Partition, tile, spec); err != nil {
			return "", err
		}
	case p.UserID != "":
		var err error
		if part, err = b.userPartition(p.UserID, tile); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	id, _ := part.User()
	return id, nil
}

// personLevel is userID's level on tile (read, write or terminal), read
// live from the users store; "" when there is none.
func (b *Broker) personLevel(userID, tile string) string {
	if b.Users == nil {
		return ""
	}
	acc, ok := b.Users.Access(userID)
	if !ok {
		return ""
	}
	return acc.TileLevel(tile)
}

// globalAddressRegRefused answers 400, up front, a cron job or bus
// subscription of tile whose delivery path carries ?xbin-partition (any
// value, an encoded key too) when tile is partitioned — its recorded mode
// has user partitions, or can't be read: the proxy would consume the
// parameter and refuse every delivery (RouteGlobal: no person drives one).
// On every other tile the parameter reaches the backend as today, so
// nothing is refused. ok false: the refusal is answered.
func (b *Broker) globalAddressRegRefused(w http.ResponseWriter, tile, path, kind string) bool {
	_, query, _ := strings.Cut(path, "?")
	if tile == "" || query == "" {
		return false
	}
	if q, _ := url.ParseQuery(query); !q.Has(util.QueryPartition) { // the pairs that parse, as the proxy reads them
		return false
	}
	if _, partitioned, err := b.tilePartitioning(tile); !partitioned && err == nil {
		return false
	}
	server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("a %s delivery acts in the partition it was registered for: ?%s=global is for %s's own frames, terminals, agent sessions and backends",
		kind, util.QueryPartition, tile), "/docs/partitions.md")
	return true
}

// attributedRole is the role an attributed F5 call holds at global: its
// person's level, never the self-call's admin (S3) — writer for write and
// terminal, reader otherwise.
func attributedRole(level string) string {
	switch level {
	case users.LevelWrite, users.LevelTerminal:
		return "writer"
	}
	return "reader"
}
