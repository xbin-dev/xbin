// projects_coord_profile.go — Projects' coordinator as a profile of the
// coordinator kit (coord_profile.go; API.md §The coordinator): the first
// one, and what the kit was drawn from. A coordinator of a project is a
// run with origin project in the built-in web class (the web lane — it
// steers tasks that have egress), its session key proj:<pid>:coord:<user>;
// what it starts are the project's tasks; what wakes it are the project's
// events; its knowledge store is the project's wiki; its research
// subagents read the project's sandbox (projects_coord_research.go).
package main

// coordProject is the project profile.
var coordProject *coordProfile

func init() {
	coordProject = registerCoordProfile(&coordProfile{
		ID:     "project",
		Origin: originProject,
		Class:  classWeb,
		Deny:   coordDeny,
		Allow: []string{"task_create", "task_list", "task_status", "task_message", "task_result", "task_cancel", "task_retry",
			"scm_pr", "scm_issues", "project_goal", "research", "task_link_pr", "task_refresh"},
		Limits: coordBounds{Research: researchMax, ResearchTimeout: researchTimeout},
		Words:  coordStateWords,
		Is: func(d *DB, run *Run) bool {
			_, _, ok := coordOf(d, run)
			return ok
		},
		Resolve: func(ag *Agent, t *DB, run *Run, cfg Config) (any, error) {
			p, err := ag.coordinatorOf(t, run, cfg)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Pending:   coordPending,
		Deliver:   coordUpdateOf,
		Knowledge: (*Agent).projWikiRole,
	})
	coordProject.mustRegister(projCoordTools()...)
}
