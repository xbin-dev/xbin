// dice.go — the example tool: roll_dice. Copy its shape for a tool of your
// own (a function returning a Tool, and its line in tools.go), or delete it
// with its line when you have one.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
)

// roll is one die of sides faces (a var so tests can load the dice).
var roll = func(sides int) int { return 1 + rand.IntN(sides) }

// diceRoll is roll_dice's result as data.
type diceRoll struct {
	Sides int   `json:"sides"`
	Count int   `json:"count"`
	Rolls []int `json:"rolls"`
	Sum   int   `json:"sum"`
}

func rollDice() Tool {
	return Tool{
		Name:        "roll_dice",
		Title:       "Roll dice",
		Description: "Roll dice: count dice (default 1) with sides faces each (default 6). Returns each roll and their sum.",
		Input: Object(map[string]any{
			"sides": IntProp("faces on each die", 2, 1000, 6),
			"count": IntProp("how many dice", 1, 100, 1),
		}),
		Output: Object(map[string]any{
			"sides": map[string]any{"type": "integer"},
			"count": map[string]any{"type": "integer"},
			"rolls": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"sum":   map[string]any{"type": "integer"},
		}, "sides", "count", "rolls", "sum"),
		// it changes nothing: a client may call it without asking
		Annotations: map[string]any{"readOnlyHint": true, "openWorldHint": false},
		Run: func(_ context.Context, raw json.RawMessage) (Result, error) {
			a, err := Args[struct {
				Sides *float64 `json:"sides"`
				Count *float64 `json:"count"`
			}](raw)
			if err != nil {
				return Result{}, err
			}
			sides, err := intArg("sides", a.Sides, 2, 1000, 6)
			if err != nil {
				return Result{}, err
			}
			count, err := intArg("count", a.Count, 1, 100, 1)
			if err != nil {
				return Result{}, err
			}
			r := diceRoll{Sides: sides, Count: count, Rolls: make([]int, count)}
			words := make([]string, count)
			for i := range r.Rolls {
				r.Rolls[i] = roll(sides)
				r.Sum += r.Rolls[i]
				words[i] = strconv.Itoa(r.Rolls[i])
			}
			return Result{Text: fmt.Sprintf("rolled %dd%d: %s (total %d)", count, sides, strings.Join(words, ", "), r.Sum), Data: r}, nil
		},
	}
}

// intArg is a whole-number argument within lo..hi, def when it was left
// out. Numbers arrive as JSON numbers, which a model may write as 20.0.
func intArg(name string, v *float64, lo, hi, def int) (int, error) {
	if v == nil {
		return def, nil
	}
	if *v != math.Trunc(*v) || *v < float64(lo) || *v > float64(hi) {
		return 0, fmt.Errorf("%s: a whole number from %d to %d (got %v)", name, lo, hi, *v)
	}
	return int(*v), nil
}
