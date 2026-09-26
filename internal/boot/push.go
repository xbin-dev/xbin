package boot

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/push"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
)

// setupPush starts the push plane (plans/native.md §14, internal/push):
// device registrations, the relay sender, POST /notify for tiles, and the
// agent session events as pushes. Off until a relay is configured — the
// routes answer either way, so the app and tiles never need to know.
func (st *State) setupPush(srv *server.Server) error {
	userStore := st.Users
	ps, err := push.New(push.Options{
		Dir:      filepath.Join(st.WS, "data", "push"),
		RelayURL: st.Cfg.PushRelay, RelayKey: st.Cfg.PushRelayKey,
		IsAdmin: st.Broker.IsAdmin,
		// the recipient must be able to read the tile that notifies them
		CanRead: func(user, tile string) bool {
			if user == push.OwnerUser {
				return true // the bootstrap owner reads everything
			}
			if userStore == nil {
				return false
			}
			if u, ok := userStore.Get(user); !ok || u.Disabled {
				return false
			}
			acc, ok := userStore.Access(user)
			return ok && acc.CanReadTile(tile)
		},
		Account: func(user string) push.Account {
			if userStore == nil {
				return push.Account{}
			}
			u, ok := userStore.Get(user)
			return push.Account{Exists: ok, Disabled: ok && u.Disabled, Created: u.Created}
		},
	})
	if err != nil {
		return err
	}
	st.Push = ps
	if st.Broker != nil {
		// signed out everywhere, disabled or deleted: the devices lose their
		// pushes with their sessions (a deleted account's preferences go too)
		prev := st.Broker.OnUserSignedOut
		st.Broker.OnUserSignedOut = func(id string, deleted bool) {
			if prev != nil {
				prev(id, deleted)
			}
			if deleted {
				ps.ForgetUser(id)
			} else {
				ps.SignedOut(id)
			}
		}
	}
	if st.Term != nil {
		publish := st.Term.OnEvent // the /ws/events publisher (stepServer)
		st.Term.OnEvent = func(cwd string, ev term.SessionEvent) {
			if publish != nil {
				publish(cwd, ev)
			}
			ps.AgentEvent(ev.User, ev.ID, cwd, ev.Event)
		}
	}
	srv.RegisterAPI("POST /devices/push", ps.APIRegister)
	srv.RegisterAPI("GET /devices/push", ps.APIList)
	srv.RegisterAPI("DELETE /devices/push/{deviceId}", ps.APIUnregister)
	srv.RegisterAPI("GET /push/prefs", ps.APIPrefs)
	srv.RegisterAPI("PUT /push/prefs", ps.APISetPrefs)
	srv.RegisterAPI("POST /push/test", ps.APITest)
	srv.RegisterAPI("GET /push/config", ps.APIConfig)
	srv.RegisterAPI("PUT /push/config", ps.APISetConfig)
	srv.RegisterAPI("DELETE /push/config", ps.APIDeleteConfig)
	srv.RegisterAPI("GET /push/devices", ps.APIAdminDevices)
	srv.RegisterAPI("DELETE /push/devices/{user}", ps.APIAdminForget)
	srv.RegisterAPI("DELETE /push/devices/{user}/{deviceId}", ps.APIAdminForget)
	srv.RegisterAPI("POST /notify", ps.APINotify)
	return nil
}
