package delegation

// toolpins.go — an MCP tool's definition as its person saw it (D239). A
// tool is known by its name to an approval, but what the person allowed
// was the tool its server described then: its description, its input
// schema, its annotations (readOnlyHint among them). So a standing
// approval of a tool pins a hash of that definition, and so does the
// person's "read-only tools don't ask" — per person, from the first time
// xbind sees the tool (Pins). A served definition that differs from the pin
// is a tool the person hasn't allowed: it asks again, saying what changed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// ToolPin is a tool's definition, hashed part by part.
type ToolPin struct {
	Hash     string            `json:"hash"`
	Parts    map[string]string `json:"parts,omitempty"` // name, title, description, inputSchema, outputSchema, annotations
	ReadOnly bool              `json:"readOnly,omitempty"`
	At       time.Time         `json:"at,omitempty"`
}

// ToolParts are the parts of a definition a pin hashes, in the words a
// change is said in.
var ToolParts = []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}

// PinOf is the pin of a tool's definition as its server listed it (any
// JSON object; members outside ToolParts don't count).
func PinOf(def map[string]any, readOnly bool) ToolPin {
	p := ToolPin{Parts: map[string]string{}, ReadOnly: readOnly}
	whole := sha256.New()
	for _, k := range ToolParts {
		b, _ := json.Marshal(def[k]) // maps marshal with sorted keys: one spelling per value
		h := sha256.Sum256(b)
		p.Parts[k] = hex.EncodeToString(h[:8])
		whole.Write([]byte(k + "=" + p.Parts[k] + ";"))
	}
	p.Hash = hex.EncodeToString(whole.Sum(nil)[:16])
	return p
}

// Changed are the parts of next that differ from p.
func (p ToolPin) Changed(next ToolPin) []string {
	var out []string
	for _, k := range ToolParts {
		if p.Parts[k] != next.Parts[k] {
			out = append(out, k)
		}
	}
	return out
}

// PinFromPreview reads the pin a request's preview carries (its "pin").
func PinFromPreview(v any) *ToolPin {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var p ToolPin
	if json.Unmarshal(b, &p) != nil || p.Hash == "" {
		return nil
	}
	return &p
}

type toolPinsDoc struct {
	Pins map[string]ToolPin `json:"pins"`
}

func pinKey(server, tool string) string { return server + "\x00" + tool }

// ToolPinOf is user's pin of server's tool, as first seen or last allowed.
func (s *Store) ToolPinOf(user, server, tool string) (ToolPin, bool) {
	if s == nil || user == "" {
		return ToolPin{}, false
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	var doc toolPinsDoc
	if _, err := readJSON(s.userFile(user, "toolpins.json"), &doc); err != nil {
		return ToolPin{}, false
	}
	pin, ok := doc.Pins[pinKey(server, tool)]
	return pin, ok
}

// PinTool records pin as user's of server's tool: the first time xbind
// sees it for them, or once they allowed a call of the definition served.
// At most MaxToolPins are kept a person (the oldest go).
func (s *Store) PinTool(user, server, tool string, pin ToolPin) error {
	if s == nil || user == "" {
		return ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	var doc toolPinsDoc
	_, _ = readJSON(s.userFile(user, "toolpins.json"), &doc) // an unreadable file starts over: every tool asks again
	if doc.Pins == nil {
		doc.Pins = map[string]ToolPin{}
	}
	pin.At = s.now().UTC().Truncate(time.Second)
	doc.Pins[pinKey(server, tool)] = pin
	if len(doc.Pins) > MaxToolPins {
		keys := make([]string, 0, len(doc.Pins))
		for k := range doc.Pins {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return doc.Pins[a].At.Compare(doc.Pins[b].At) })
		for _, k := range keys[:len(keys)-MaxToolPins] {
			delete(doc.Pins, k)
		}
	}
	return writeJSON(s.userFile(user, "toolpins.json"), doc)
}

// MaxToolPins bounds a person's pins.
const MaxToolPins = 2000
