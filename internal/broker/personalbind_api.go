package broker

// personalbind_api.go — the personal binds' routes (plans/partitions/05 §3,
// docs/partitions.md §Bind types): GET, POST and DELETE
// /api/xbin/partitions/binds, and the one bind-time refusal a global bind
// gains (bindConflict). Creating a personal bind is a person's own act
// (PersonOnly: their session, app or device — never tile code, view-as or
// the root token); admins list and delete every person's, and never create
// one for someone else — a bind an admin makes for a tile is a global bind.

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

func (b *Broker) registerPersonalBinds(srv *server.Server) {
	srv.RegisterAPI("GET /partitions/binds", b.apiPersonalBindsList)
	srv.RegisterAPI("POST /partitions/binds", b.apiPersonalBindAdd)
	srv.RegisterAPI("DELETE /partitions/binds", b.apiPersonalBindDelete)
}

// bindConflict is a binding validateBinding refuses with 409 rather than
// 400: well-formed, but it could never carry a call.
type bindConflict struct{ msg string }

func (e *bindConflict) Error() string { return e.msg }

// bindingStatus is the status a validateBinding error answers with.
func bindingStatus(err error) int {
	var c *bindConflict
	if errors.As(err, &c) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// partitionedProviderRefusal is the bind-time half of the edge matrix
// (05 §1, §3): a new ref of an unpartitioned requester's http slot to a
// tile whose recorded mode has user partitions and no global instance
// reaches nothing (Route refuses every call of it), so it is refused, 409.
// A partitioned requester reaches the same person's partition; with
// global, the unpartitioned requester reaches it. A ref the slot already
// holds (bound before its provider partitioned) isn't judged again: editing
// the slot's other refs keeps working as before.
func (b *Broker) partitionedProviderRefusal(comp, slot, ref string) error {
	if slices.Contains(b.Reg.Workspace().Bindings[comp][slot].Refs(), ref) {
		return nil
	}
	prov, _ := splitRef(ref)
	spec, part, err := b.tilePartitioning(prov)
	if err != nil || !part || spec.Global {
		return nil
	}
	if _, reqPart, err := b.tilePartitioning(comp); err == nil && reqPart {
		return nil
	}
	return &bindConflict{fmt.Sprintf("%s is partitioned and has no global instance: %s doesn't keep each person's data apart, so no call of it would reach %s (docs/partitions.md)", prov, comp, prov)}
}

// personalBindRow is one bind on the wire.
type personalBindRow struct {
	ID        string    `json:"id"`
	User      string    `json:"user"`
	Requester string    `json:"requester"`
	Slot      string    `json:"slot"`
	Provider  string    `json:"provider"`
	At        time.Time `json:"at"`
	// Live: the bind holds now (personalBindCheck); Why says why not.
	Live bool   `json:"live"`
	Why  string `json:"why,omitempty"`
}

func (b *Broker) bindRow(user string, pb personalBind) personalBindRow {
	_, why := b.personalBindCheck(user, pb)
	return personalBindRow{ID: pb.ID, User: user, Requester: pb.Requester, Slot: pb.Slot, Provider: pb.Provider, At: pb.At, Live: why == "", Why: why}
}

// personOf is the person p acts as on a personal-bind route: a person's own
// credential (session, app, device), never view-as. "" otherwise.
func personOf(p auth.Principal) string {
	if p.Component != "" || p.Impersonator != "" || p.User == nil || p.User.Disabled {
		return ""
	}
	return p.UserID
}

// apiPersonalBindsList — GET /partitions/binds: a person's own binds (by
// their live uid), or every person's for an admin (the admin tile
// included); anyone else 403.
func (b *Broker) apiPersonalBindsList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	rows := []personalBindRow{}
	switch id := personOf(p); {
	case b.IsAdmin(p):
		err := b.eachPersonalBinds(func(f *personalBindsFile) {
			if b.storedPartitionUID(f.User) != f.UID {
				return // a deleted person's, or an earlier incarnation's: applies to no one
			}
			for _, pb := range f.Binds {
				rows = append(rows, b.bindRow(f.User, pb))
			}
		})
		if err != nil {
			slog.Warn("partitions: a personal-binds record can't be read", "err", err)
		}
	case id != "":
		if f := b.livePersonalBinds(id); f != nil {
			for _, pb := range f.Binds {
				rows = append(rows, b.bindRow(id, pb))
			}
		}
	default:
		server.WriteError(w, http.StatusForbidden, "personal binds are listed to their person and to admins", "/docs/partitions.md")
		return
	}
	slices.SortFunc(rows, func(x, y personalBindRow) int {
		return cmp.Or(cmp.Compare(x.User, y.User), cmp.Compare(x.Requester, y.Requester), cmp.Compare(x.Slot, y.Slot), cmp.Compare(x.Provider, y.Provider))
	})
	server.WriteJSON(w, http.StatusOK, map[string]any{"binds": rows})
}

type personalBindBody struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Requester string `json:"requester"`
	Slot      string `json:"slot"`
	Provider  string `json:"provider"`
}

// apiPersonalBindAdd — POST /partitions/binds {requester, slot, provider}:
// the caller's own partition of requester gains provider on slot (05 §3).
func (b *Broker) apiPersonalBindAdd(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body personalBindBody
	if err := server.DecodeJSON(r, &body); err != nil || body.Requester == "" || body.Slot == "" || body.Provider == "" || body.ID != "" || body.User != "" {
		server.WriteError(w, http.StatusBadRequest, "need {requester, slot, provider}")
		return
	}
	id := personOf(p)
	if id == "" || b.Users == nil {
		server.WriteError(w, http.StatusForbidden, "a personal bind is a person's own act: sign in and do it yourself — a tile's credentials can't, and an admin's bind for a tile is a global bind (POST /bindings)", "/docs/partitions.md")
		return
	}
	pb := personalBind{Requester: body.Requester, Slot: body.Slot, Provider: body.Provider}
	status, err := b.personalBindRefusal(id, pb)
	if err != nil {
		server.WriteError(w, status, err.Error(), "/docs/partitions.md")
		return
	}
	uid, err := b.mintPartitionUID(id)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.pbind.mu.Lock()
	f, err := b.readPersonalBinds(uid)
	switch {
	case err != nil:
		b.pbind.mu.Unlock()
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	case f == nil || f.User != id:
		f = &personalBindsFile{User: id, UID: uid}
	}
	for _, e := range f.Binds {
		if e.Requester == pb.Requester && e.Slot == pb.Slot && e.Provider == pb.Provider {
			b.pbind.mu.Unlock()
			server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "bind": b.bindRow(id, e)}) // already there
			return
		}
	}
	pb.ID, pb.At = newBindID(), time.Now().UTC()
	f.Binds = append(f.Binds, pb)
	err = b.writePersonalBinds(f)
	b.pbind.mu.Unlock()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("personal bind added", "user", id, "requester", pb.Requester, "slot", pb.Slot, "provider", pb.Provider)
	b.restartPartition(pb.Requester, id)
	b.publishPersonalBinds(pb.Requester, id)
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "bind": b.bindRow(id, pb)})
}

// personalBindRefusal is why person id may not create pb, with its status:
// 404 an unknown tile; 403 authority (the provider isn't theirs, they can't
// read the requester, the ceiling); 409 a bind that can't be one (the
// requester doesn't partition or is paused, the slot, the provider's mode
// or service, a global bind of the same provider).
func (b *Broker) personalBindRefusal(id string, pb personalBind) (int, error) {
	c, ok := b.Reg.Component(pb.Requester)
	if !ok {
		return http.StatusNotFound, fmt.Errorf("no such component: %s", pb.Requester)
	}
	if _, ok := b.Reg.Component(pb.Provider); !ok {
		return http.StatusNotFound, fmt.Errorf("no such component: %s", pb.Provider)
	}
	if b.Users.Owner(pb.Provider) != users.OwnerKindUser+":"+id {
		return http.StatusForbidden, fmt.Errorf("%s isn't yours: a personal bind wires a tile you own personally into your own partition — for anything else, ask an admin for a global bind", pb.Provider)
	}
	if err := b.personLive(id, pb.Requester); err != nil {
		return http.StatusForbidden, err
	}
	if st, _, _ := c.PartitionState(); st.Held() || c.PartitionRecordUnknown() {
		return http.StatusConflict, fmt.Errorf("%s is paused (its partition mode is %s): a manager must decide first", pb.Requester, st)
	}
	if _, part, _ := b.tilePartitioning(pb.Requester); !part {
		return http.StatusConflict, fmt.Errorf("%s doesn't keep each person's data apart: its bindings are global (POST /bindings)", pb.Requester)
	}
	if def, ok := c.Manifest.Interfaces[pb.Slot]; !ok || def.Kind != "http" || !def.Multi {
		return http.StatusConflict, fmt.Errorf("%s has no multi http slot %q: personal binds wire multi:true http slots only", pb.Requester, pb.Slot)
	}
	if slices.Contains(b.Reg.Workspace().Bindings[pb.Requester][pb.Slot].Refs(), pb.Provider) {
		return http.StatusConflict, fmt.Errorf("%s is already bound on %s.%s for everyone (a global bind)", pb.Provider, pb.Requester, pb.Slot)
	}
	if msg := b.ceilingBlockMsg(pb.Requester, pb.Provider); msg != "" {
		return http.StatusForbidden, errors.New(msg)
	}
	if _, why := b.personalBindCheck(id, pb); why != "" {
		return http.StatusConflict, errors.New(why)
	}
	return 0, nil
}

// apiPersonalBindDelete — DELETE /partitions/binds {id} or {requester,
// slot, provider[, user]}: the person removes their own; an admin anyone's
// (user names whose, for the triple). Restarts that person's partition
// instance of the requester.
func (b *Broker) apiPersonalBindDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body personalBindBody
	if err := server.DecodeJSON(r, &body); err != nil || body.ID == "" && (body.Requester == "" || body.Slot == "" || body.Provider == "") {
		server.WriteError(w, http.StatusBadRequest, "need {id} or {requester, slot, provider[, user]}")
		return
	}
	admin, self := b.IsAdmin(p), personOf(p)
	whose := body.User // "": the caller's own — an admin's by id, or without a person of their own: anyone's
	if whose == "" && !(admin && body.ID != "") {
		whose = self
	}
	switch {
	case whose == "" && !admin:
		server.WriteError(w, http.StatusForbidden, "personal binds are deleted by their person or an admin", "/docs/partitions.md")
		return
	case whose != "" && whose != self && !admin:
		server.WriteError(w, http.StatusForbidden, "only an admin deletes someone else's personal bind", "/docs/partitions.md")
		return
	}
	match := func(user string, pb personalBind) bool {
		if whose != "" && user != whose {
			return false
		}
		if body.ID != "" {
			return pb.ID == body.ID
		}
		return pb.Requester == body.Requester && pb.Slot == body.Slot && pb.Provider == body.Provider
	}
	var gone []personalBindRow
	n, _, err := b.dropPersonalBinds(func(user string, pb personalBind) bool {
		if match(user, pb) {
			gone = append(gone, personalBindRow{ID: pb.ID, User: user, Requester: pb.Requester, Slot: pb.Slot, Provider: pb.Provider, At: pb.At})
			return true
		}
		return false
	}, false)
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		server.WriteError(w, http.StatusNotFound, "no such personal bind")
		return
	}
	for _, g := range gone {
		slog.Info("personal bind removed", "user", g.User, "requester", g.Requester, "slot", g.Slot, "provider", g.Provider, "by", cmp.Or(self, "admin"))
		b.restartPartition(g.Requester, g.User)
		b.publishPersonalBinds(g.Requester, g.User)
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": gone})
}
