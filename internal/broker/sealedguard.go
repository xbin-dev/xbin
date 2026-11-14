package broker

// sealedguard.go — where a sealed component is kept sealed (D227).
//
// The route gate (server/sealedclass.go) is the early, friendly answer; it
// reads a request beside its handler and is not what the rule rests on.
// The rule is refused where the work is done, on the value the code acts
// on. Two rules, each with one helper:
//
//   - What is a sealed component's own — its resources, its secrets, its
//     registrations, its status — is used by its backend alone: the
//     instance credential xbind gave that backend (sealedActor). Not an
//     admin, not the root token, not another tile, and not its own page:
//     a frame token of a sealed component is a viewer's (server/sealed.go).
//     Checked in allowRes and allowAt (every kv, blob and bus use, the
//     events socket's bus filter included), vaultAccess, and the cron and
//     bus registration handlers and their restore path.
//
//   - What does not exist for a sealed component is refused for everyone
//     (sealedNone): a grant from, on or into one (grantMutation, and the
//     workspace file's writer, registry.MutateWorkspace, which takes no
//     such row); a binding to one, or of one beyond what its table entry
//     allows (validateBinding); an owner (users.Store.SetOwner); a
//     deployment, a checkpoint or a record (deployments.Plane.judge and
//     its record writer); its source (component, prTarget,
//     requireCodeRead, codeGrantAllows); a partition switch, review or
//     consent; a clone, a template instance, an import; an interface
//     instance, an ingress host, a chrome flag; a single file out of its
//     backup; a restore from an archive xbind can't authenticate.
//
// internal/boot's TestSealedBoundaryWithoutTheGate drives every Refused
// and Self row of the route table with the gate off and expects each of
// these to answer.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedActor reports whether p acts as the sealed component comp: its
// backend's instance credential, or xbind acting for it (the principal
// xbind itself builds for a delivery's checks carries no Via; no credential
// authenticates to one).
func sealedActor(p auth.Principal, comp string) bool {
	return comp != "" && p.Component == comp && (p.Via == "instance" || p.Via == "")
}

// sealedError is a refusal about a sealed component; Status is what a
// handler answers.
type sealedError struct {
	Status int
	Msg    string
}

func (e *sealedError) Error() string { return e.Msg }

// sealedOwn refuses p on what is the sealed component's own when name — a
// component path, a path below one, or a res:/code: target — lies in the
// sealed namespace and p is not that component's backend. nil for
// everything outside the namespace.
func sealedOwn(p auth.Principal, name, what string) error {
	if !util.SealedTarget(name) {
		return nil
	}
	if comp := sealedComponentOf(name); sealedActor(p, comp) {
		return nil
	}
	return &sealedError{Status: http.StatusForbidden, Msg: util.SealedRefusal(sealedShown(name), what)}
}

// sealedNone refuses name for an operation that doesn't exist for a sealed
// component, whoever asks. nil outside the sealed namespace.
func sealedNone(name, what string) error {
	if !util.SealedTarget(name) {
		return nil
	}
	return &sealedError{Status: http.StatusBadRequest, Msg: util.SealedRefusal(sealedShown(name), what)}
}

// sealedComponentOf is the sealed component a name in the namespace
// belongs to: "xbin/<name>", from a path, a path below it, or a target.
func sealedComponentOf(name string) string {
	name = strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(name), "res:"), "code:"), "/")
	rest, ok := strings.CutPrefix(name, util.SealedPrefix)
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, "/")
	first, _, _ = strings.Cut(first, "#")
	return util.SealedPrefix + first
}

func sealedShown(name string) string { return strings.Trim(strings.TrimSpace(name), "/") }

// sealedRefused writes err when it is a sealed refusal (or a workspace
// file's refusal of a sealed row) and reports whether it did.
func sealedRefused(w http.ResponseWriter, err error) bool {
	var se *sealedError
	var row *registry.SealedRowError
	switch {
	case errors.As(err, &se):
		server.WriteError(w, se.Status, se.Msg, server.SealedDocs)
	case errors.As(err, &row):
		server.WriteError(w, http.StatusBadRequest, row.Error(), server.SealedDocs)
	default:
		return false
	}
	return true
}

// Texts of the refusals (the route gate's rows say the same).
const (
	sealedOwnRes   = "only it reads and writes its own resources"
	sealedOwnVault = "only it reads and writes its own secrets"
	sealedOwnRegs  = "only it registers its own deliveries"
	sealedOwnBus   = "only it publishes on its own bus"
	sealedNoGrant  = "its grants are fixed by the xbin release, and nothing is granted a call into it or its resources"
	sealedNoOwner  = "it has no owner: workspace admins manage it"
	sealedNoSource = "it has no source to read: its code is the xbin release's"
	sealedNoPRs    = "it takes no change proposals: its code is the xbin release's"
	sealedNoMode   = "its partition mode and its code are the xbin release's"
	sealedNoReach  = "no tile reaches people's partitions of it"
	sealedNoCreate = "nothing is created under xbin/"
	sealedNoClone  = "it can't be cloned, and nothing is created under xbin/"
	sealedNoIface  = "it provides no interface and publishes no endpoint"
)
