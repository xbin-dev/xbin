package boot

import (
	"testing"
)

// covers D127c D127h — GET /deployments/backups lists one deployment's archived
// versions for admins: ?deployment=, else the ref's qualifier, else the
// primary; a removed deployment's name still reads (its archives stay);
// people who aren't admins get 403; a qualifier and a differing
// ?deployment= are a 400; without the broker's hook it answers 501.
func TestDeploymentBackupsRead(t *testing.T) {
	f := newDplFix(t)
	if code, _, body := f.do(t, dplAdmin, "GET", "/deployments/backups?tile=apps/crm", ""); code != 501 {
		t.Fatalf("without the hook: %d %s", code, body)
	}
	var asked []string
	f.dp.DataBackups = func(tile, dep string) (any, error) {
		asked = append(asked, tile+"/"+dep)
		return map[string]any{"deployment": dep, "versions": []any{}, "archiver": ""}, nil
	}
	for _, c := range []struct{ target, want, dep string }{
		{"/deployments/backups?tile=apps/crm", "apps/crm/main", "main"}, // the primary
		{"/deployments/backups?tile=apps/crm&deployment=dev", "apps/crm/dev", "dev"},
		{"/deployments/backups?tile=apps/crm&deployment=gone", "apps/crm/gone", "gone"},
		{"/deployments/backups?tile=apps/pin", "apps/pin/main", "main"},
		{"/deployments/backups?tile=apps/zs&deployment=main", "apps/zs/main", "main"},
	} {
		asked = nil
		if got := f.get(t, dplAdmin, c.target, 200); got["deployment"] != c.dep || len(asked) != 1 || asked[0] != c.want {
			t.Errorf("GET %s asked %q, answered %v; want %s", c.target, asked, got, c.want)
		}
	}
	asked = nil
	for _, c := range []struct {
		target string
		code   int
	}{
		{"/deployments/backups?tile=apps/crm%2Bdev", 400}, // D127j: never tile+name in a query
		{"/deployments/backups?tile=apps/crm+dev&deployment=main", 400},
		{"/deployments/backups?tile=apps/crm&deployment=Bad", 400},
		{"/deployments/backups?tile=apps/nope", 404},
	} {
		if code, _, body := f.do(t, dplAdmin, "GET", c.target, ""); code != c.code {
			t.Errorf("GET %s = %d %s, want %d", c.target, code, body, c.code)
		}
	}
	if code, _, body := f.do(t, dplWriter, "GET", "/deployments/backups?tile=apps/crm", ""); code != 403 {
		t.Errorf("a writer: %d %s, want 403", code, body)
	}
	if len(asked) != 0 {
		t.Errorf("a refused read asked the broker: %q", asked)
	}
}
