package main

// bx agent send / unqueue: prompting a session, and messages while its turn
// runs (D197) — sent into the turn where the agent takes them mid-turn,
// else held until it ends (POST/DELETE …/term/sessions/<id>/queue).

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// cmdAgentSend prompts a running session and follows the turn. --queue
// sends it whatever the agent is doing (D197): into the running turn where
// the agent takes messages mid-turn, else held for the turn's end;
// --interrupt holds it and stops the running turn, so it goes in next.
func cmdAgentSend(args []string) error {
	queue, interrupt := false, false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--queue":
			queue = true
		case "--interrupt":
			queue, interrupt = true, true
		default:
			return fmt.Errorf("unknown flag %s", args[0])
		}
		args = args[1:]
	}
	if len(args) < 2 {
		return errors.New("usage: bx agent send [--queue | --interrupt] <session> \"<text>\"")
	}
	var cur struct {
		Next uint64 `json:"next"`
	}
	_ = apiJSON("GET", "/api/xbin/term/sessions/"+args[0]+"/events?since=0", nil, &cur)
	text := strings.Join(args[1:], " ")
	if !queue {
		if err := agentPrompt(args[0], text); err != nil {
			return err
		}
		return agentFollow(args[0], cur.Next, true)
	}
	var res struct{ Outcome, ID string }
	if err := apiJSON("POST", "/api/xbin/term/sessions/"+args[0]+"/queue", map[string]any{"text": text, "interrupt": interrupt}, &res); err != nil {
		return err
	}
	switch res.Outcome {
	case "steered":
		fmt.Fprintln(os.Stderr, "taken into the running turn")
	case "queued":
		fmt.Fprintf(os.Stderr, "queued (%s): it goes in when the running turn ends — bx agent unqueue %s %s takes it back\n", res.ID, args[0], res.ID)
		return nil
	}
	return agentFollow(args[0], cur.Next, true)
}

// cmdAgentUnqueue takes held messages back (all, or one) and prints them.
func cmdAgentUnqueue(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: bx agent unqueue <session> [<queued id>]")
	}
	path := "/api/xbin/term/sessions/" + args[0] + "/queue"
	if len(args) == 2 {
		path += "/" + args[1]
	}
	var res struct {
		Items []struct{ ID, Text string } `json:"items"`
	}
	if err := apiJSON("DELETE", path, nil, &res); err != nil {
		return err
	}
	for _, it := range res.Items {
		fmt.Printf("%s\t%s\n", it.ID, it.Text)
	}
	return nil
}
