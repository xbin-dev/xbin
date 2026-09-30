// hosted_tools.go — the tools a hosted (non-secure) conversation's runs
// don't have (hosted.go). The host's engine drives them over `team` with the
// host's partition's agent, so a tool that keeps state outside the
// conversation would put it in the wrong place:
//
//   - schedule / unschedule register cron jobs in the HOST'S partition,
//     named by a schedule id that team numbers from 1 like the partition's
//     own — a hosted run's schedule would replace, fire or delete the host's
//     private one (and the fire route reads the partition's own db);
//   - schedules_list, schedule_inspect, threads_list, thread_inspect read the
//     conversation owner's other threads — in team, other people's hosted
//     conversations with other audiences;
//   - skills_list, skill_view, skill_manage read and write team's skills
//     table, which every hosted conversation of every host shares.
//
// They are absent from a hosted run's tool list and refused if called
// anyway. Every run in team is a hosted conversation's (ids from 2^39,
// team_runs.go), subagents included, so the run's id is enough.
package main

import "fmt"

// hostedToolsets are the toolsets a hosted run lacks.
var hostedToolsets = map[string]bool{tsSchedule: true, tsThreads: true, tsSkills: true}

// hostedToolRefused: name isn't available in run, a hosted conversation's.
func hostedToolRefused(run *Run, name string) error {
	if run == nil || !hostedID(run.ID) || !hostedToolsets[toolsetOf(name)] {
		return nil
	}
	return fmt.Errorf("%s is not available in a non-secure (hosted) conversation: it would keep state outside it, in its host's or everyone's space", name)
}

// hostedToolSpecs drops what a hosted run lacks from its tool list.
func hostedToolSpecs(run *Run, specs []toolSpec) []toolSpec {
	if run == nil || !hostedID(run.ID) {
		return specs
	}
	kept := specs[:0:0]
	for _, s := range specs {
		if !hostedToolsets[toolsetOf(s.Function.Name)] {
			kept = append(kept, s)
		}
	}
	return kept
}
