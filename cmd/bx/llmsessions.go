package main

import (
	"fmt"
	"strings"
)

// bx llm sessions — what coding agents and terminals get of the LLM
// connector (D226, docs/llm.md "Coding agents and terminals"): who serves a
// coding agent's model calls by default, whether terminals get XBIN_LLM_URL
// and XBIN_LLM_KEY, and whether they also get the OPENAI_* pair.

type llmSessions struct {
	Harnesses string `json:"harnesses"`
	Terminals bool   `json:"terminals"`
	OpenAIEnv bool   `json:"openaiEnv"`
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func printLLMSessions(s *llmSessions, lock func(path string) string) {
	if s == nil {
		return
	}
	how := map[string]string{
		"own-first": "a person's own sign-in when they have one, else the connector",
		"connector": "the connector, whatever they are signed in to",
		"off":       "nothing of the connector",
	}[s.Harnesses]
	fmt.Printf("  coding agents: %s (%s)%s\n", s.Harnesses, how, lock("sessions.harnesses"))
	fmt.Printf("  terminals: XBIN_LLM_URL and XBIN_LLM_KEY %s%s; OPENAI_BASE_URL and OPENAI_API_KEY %s%s\n",
		onOff(s.Terminals), lock("sessions.terminals"), onOff(s.OpenAIEnv), lock("sessions.openaiEnv"))
}

// llmSessionsCmd changes the switches named as key=value; with none it
// prints the configuration.
func llmSessionsCmd(args []string, usage error) error {
	body := map[string]any{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return usage
		}
		switch k {
		case "harnesses":
			body["harnesses"] = v
		case "terminals", "openai-env":
			if v != "on" && v != "off" {
				return fmt.Errorf("%s: on or off", k)
			}
			name := map[string]string{"terminals": "terminals", "openai-env": "openaiEnv"}[k]
			body[name] = v == "on"
		default:
			return usage
		}
	}
	if len(body) == 0 {
		var v llmConfig
		if err := llmCall("GET", "/api/xbin/llm/config", nil, &v, nil); err != nil {
			return err
		}
		printLLMConfig(v)
		return nil
	}
	return llmPutConfig(map[string]any{"sessions": body})
}
