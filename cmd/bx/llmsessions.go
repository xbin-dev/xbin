package main

import (
	"fmt"
	"strings"
)

// bx llm sessions — what coding agents and terminals get of the LLM
// connector (D226, docs/llm.md "Coding agents and terminals"): whether
// agent sessions are offered it, whether terminals get XBIN_LLM_URL and
// XBIN_LLM_KEY, and whether they also get the OPENAI_* pair. (Who then
// serves an agent session — the connector or the person's own sign-in — is
// the agents policy's: bx org agents.)

type llmSessions struct {
	Agents    bool `json:"agents"`
	Terminals bool `json:"terminals"`
	OpenAIEnv bool `json:"openaiEnv"`
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
	fmt.Printf("  coding agents: the connector is %s for agent sessions%s (who serves one: bx org agents)\n", onOff(s.Agents), lock("sessions.agents"))
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
		case "agents", "terminals", "openai-env":
			if v != "on" && v != "off" {
				return fmt.Errorf("%s: on or off", k)
			}
			name := map[string]string{"agents": "agents", "terminals": "terminals", "openai-env": "openaiEnv"}[k]
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
