package acptest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// llm.go — the `llm-call` script: the agent as a client of an LLM connector
// (sdk/acp connector.go), the way a real CLI pointed at one calls it. It
// reads the endpoint from the agent's own environment — XBIN_LLM_URL and
// XBIN_LLM_KEY, which a host injects (acp.Fake's Connector) — and makes one
// Chat Completions call with the prompt's remaining text, as the model
// FAKEACP_LLM_MODEL names ("" = the connector's default).

// llmCall answers what the turn says: "llm: <the answer's text>", "llm: no
// connector" without the variables, or "llm: error <status> <code or
// body>" for a refusal (the key is never echoed).
func (f *fake) llmCall(text string) string {
	base, key := f.getenv("XBIN_LLM_URL"), f.getenv("XBIN_LLM_KEY")
	if base == "" || key == "" {
		return "llm: no connector"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "hello"
	}
	req := map[string]any{"messages": []map[string]string{{"role": "user", "content": text}}}
	if m := f.getenv("FAKEACP_LLM_MODEL"); m != "" {
		req["model"] = m
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "llm: error " + err.Error()
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(hr)
	if err != nil {
		return "llm: error unreachable: " + strings.ReplaceAll(err.Error(), key, "…")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode/100 != 2 && out.Error != nil:
		code, _ := out.Error.Code.(string)
		return "llm: error " + resp.Status[:3] + " " + code
	case resp.StatusCode/100 != 2:
		return "llm: error " + resp.Status[:3]
	case len(out.Choices) == 0:
		return "llm: error no choices"
	}
	return "llm: " + out.Choices[0].Message.Content
}
