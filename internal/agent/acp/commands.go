package acp

import (
	"encoding/json"
)

// Slash commands (D77): the agent advertises what "/name" it understands
// (available_commands_update); a client offers them as the user types "/".
// A command is sent as plain prompt text — ACP's own model — so all this
// carries is the list, normalized to {name, description, hint?}, on a status
// event when it changes and on every idle (like options, so a client
// replaying a long log after the ring dropped the update still has it).

type availableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Input       *struct {
		Hint string `json:"hint"`
	} `json:"input,omitempty"`
}

// Command is one slash command as the events carry it.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Hint        string `json:"hint,omitempty"` // what to type after the name
}

func (c *Client) onCommands(raw json.RawMessage) {
	var u struct {
		AvailableCommands []availableCommand `json:"availableCommands"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	cmds := make([]Command, 0, len(u.AvailableCommands))
	for _, a := range u.AvailableCommands {
		if a.Name == "" {
			continue
		}
		cmd := Command{Name: a.Name, Description: a.Description}
		if a.Input != nil {
			cmd.Hint = a.Input.Hint
		}
		cmds = append(cmds, cmd)
	}
	c.mu.Lock()
	c.commands = cmds
	c.mu.Unlock()
	c.emit(c.partialStatus(map[string]any{"commands": cmds}))
}

// withCommands adds the latest list to an idle status.
func (c *Client) withCommands(d map[string]any) {
	c.mu.Lock()
	cmds := c.commands
	c.mu.Unlock()
	if len(cmds) > 0 {
		d["commands"] = cmds
	}
}
