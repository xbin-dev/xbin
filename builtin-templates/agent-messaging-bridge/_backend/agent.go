package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// agentClient speaks the agent-inbox contract (docs/agent-inbox.md) to the
// agent this tile's `agent` interface is bound to.
type agentClient struct {
	base string // XBIN_IFACE_AGENT_URL ("" = not bound)
	hc   *http.Client
}

func newAgentClient(base string, hc *http.Client) *agentClient {
	return &agentClient{base: strings.TrimRight(base, "/"), hc: hc}
}

var errUnknownChannel = errors.New("the agent doesn't know this channel")

type helloReply struct {
	ChannelID int64  `json:"channelId"`
	State     string `json:"state"`
}

func (a *agentClient) do(method, path, ctype string, body io.Reader, out any) error {
	req, err := http.NewRequest(method, a.base+path, body)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound && (strings.HasPrefix(path, "/adapter/message") || strings.HasPrefix(path, "/adapter/files?")):
		return errUnknownChannel
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the agent refused this bridge (%s) — bind the agent interface to an agent tile", strings.TrimSpace(string(b)))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("agent %s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (a *agentClient) post(path string, body, out any) error {
	raw, _ := json.Marshal(body)
	return a.do("POST", path, "application/json", bytes.NewReader(raw), out)
}

func (a *agentClient) hello(platform string, acct Account) (helloReply, error) {
	var out helloReply
	err := a.post("/adapter/hello", map[string]any{"protocol": 1, "platform": platform,
		"account":  map[string]string{"id": acct.ID, "name": acct.Name},
		"bot":      map[string]string{"id": acct.BotID, "name": acct.BotName},
		"features": orSlice(acct.Features)}, &out)
	return out, err
}

func orSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// agentMsg is POST /adapter/message.
type agentMsg struct {
	ChannelID       int64        `json:"channelId"`
	EventID         string       `json:"eventId"`
	Conversation    Conversation `json:"conversation"`
	Thread          string       `json:"thread,omitempty"`
	MessageID       string       `json:"messageId"`
	AssistantThread bool         `json:"assistantThread,omitempty"`
	Sender          struct {
		ID    string `json:"id"`
		Name  string `json:"name,omitempty"`
		IsBot bool   `json:"isBot,omitempty"`
	} `json:"sender"`
	Mentioned bool     `json:"mentioned,omitempty"`
	Text      string   `json:"text"`
	Command   string   `json:"command,omitempty"`
	Files     []string `json:"files,omitempty"`
}

type verdict struct {
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason"`
	Dup        bool   `json:"dup"`
	Command    string `json:"command"`
	SessionKey string `json:"sessionKey"`
	RunID      int64  `json:"runId"`
}

func (a *agentClient) message(m agentMsg) (verdict, error) {
	var v verdict
	err := a.post("/adapter/message", m, &v)
	return v, err
}

// upload stages one attachment for a message → its file id.
func (a *agentClient) upload(chID int64, name, mime string, data []byte) (string, error) {
	var out struct {
		FileID string `json:"fileId"`
	}
	q := url.Values{"channelId": {strconv.FormatInt(chID, 10)}, "name": {name}, "mime": {mime}}
	err := a.do("POST", "/adapter/files?"+q.Encode(), orStr(mime, "application/octet-stream"), bytes.NewReader(data), &out)
	return out.FileID, err
}

// download reads one file of an outbox row.
func (a *agentClient) download(rowID int64, i int) ([]byte, string, error) {
	resp, err := a.hc.Get(fmt.Sprintf("%s/adapter/files/%d/%d", a.base, rowID, i))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("agent file %d/%d: %s", rowID, i, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	return b, resp.Header.Get("Content-Type"), err
}

type ackItem struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Ref   string `json:"ref,omitempty"`
	Error string `json:"error,omitempty"`
}

func (a *agentClient) ack(items ...ackItem) error {
	return a.post("/adapter/ack", map[string]any{"acks": items}, nil)
}

func (a *agentClient) outbox(since int64) (*http.Response, error) {
	resp, err := a.hc.Get(a.base + "/adapter/outbox?since=" + strconv.FormatInt(since, 10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("agent outbox: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}
