// projects_coord_retry.go — the coordinator's task_retry (API.md §The
// coordinator): a task whose workspace failed has its failed steps queued
// again (retryTask — the task card's Retry, POST /runs/{id}/task/retry);
// one whose last turn errored also runs that turn again (POST
// /runs/{id}/resume's wake). Any other task is refused: retrying answers
// no one's question, approves nothing and pushes nothing.
package main

import (
	"errors"
	"fmt"
)

func taskRetrySpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{Name: "task_retry",
		Description: "Retry a task of this project whose workspace failed (its failed preparation steps run again, as the task's Retry button does) or whose last turn ended in an error (that turn runs again). " +
			"Refused for a task in any other state — one waiting for a person, working, or done.",
		Parameters: obj([]string{"task"}, map[string]any{
			"task": coordIntProp("the task's number"),
		})}}
}

func (ag *Agent) coordRetry(run *Run, p *Project, args map[string]any) (string, error) {
	var a struct {
		Task int64 `json:"task"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	k, trun, err := ag.db.projectTaskOf(p, a.Task)
	switch {
	case err != nil:
		return "", err
	case trun == nil:
		return "", fmt.Errorf("task #%d's conversation was deleted", k.N)
	case k.Phase != phaseOpen && k.Phase != phasePR:
		return "", fmt.Errorf("task #%d is %s: there is nothing to retry", k.N, k.Phase)
	}
	failed, errored := k.WS == wsFailed, trun.Status == statusError
	if !failed && !errored {
		return "", fmt.Errorf("task #%d is %s: task_retry retries only a task whose workspace failed or whose last turn ended in an error",
			k.N, coordStateOf(ag.db.projTaskView(p, k)))
	}
	if errored && !failed && ag.db.getSetting("halt") == "1" {
		return "", errors.New("the agent is paused by a manager: no turn starts until they resume it")
	}
	if err := ag.coordStillOwner(); err != nil {
		return "", err
	}
	if err := ag.retryTask(p, k, coordWho(run.Owner)); err != nil {
		return "", err
	}
	if failed {
		return fmt.Sprintf("task #%d: its workspace's failed steps run again — it goes on once its workspace is ready", k.N), nil
	}
	if _, _, err := ag.queue(trun.ID, inboxWake, inboxBody{}, ""); err != nil {
		return "", err
	}
	return fmt.Sprintf("task #%d: its last turn runs again", k.N), nil
}
