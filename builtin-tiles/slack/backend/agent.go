package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// agentClient speaks the agent-inbox contract (docs/agent-inbox.md) to the
// agent this tile's `agent` interface is bound to.
type agentClient struct {
	base string // XBIN_IFACE_AGENT_URL: "" = not bound
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

func (a *agentClient) post(path string, body, out any) error {
	raw, _ := json.Marshal(body)
	resp, err := a.hc.Post(a.base+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound && path == "/adapter/message":
		return errUnknownChannel
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the agent refused this tile (%s) — bind the agent interface to an agent tile", strings.TrimSpace(string(b)))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("agent %s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (a *agentClient) hello(who authInfo) (helloReply, error) {
	var out helloReply
	err := a.post("/adapter/hello", map[string]any{"protocol": 1, "platform": "slack",
		"account":  map[string]string{"id": who.TeamID, "name": who.Team},
		"bot":      map[string]string{"id": who.UserID, "name": who.User},
		"features": []string{"threads", "assistant", "commands"}}, &out)
	return out, err
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
