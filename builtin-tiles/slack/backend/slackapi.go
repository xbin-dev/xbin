package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// slackAPI is the Web API: a POST per method with the token as a bearer.
// Writes send JSON, reads a form (not every read method takes JSON).
type slackAPI struct {
	base string
	hc   *http.Client
}

func newSlackAPI(base string) *slackAPI {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return &slackAPI{base: base, hc: &http.Client{Timeout: 30 * time.Second}}
}

// apiError is Slack's {ok:false, error}. Permanent ones are not retried.
type apiError struct{ Code string }

func (e *apiError) Error() string { return "slack: " + e.Code }

// permanent: retrying the same call can't help.
func (e *apiError) permanent() bool {
	switch e.Code {
	case "ratelimited", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		return false
	}
	return true
}

// rateError is a 429: wait Retry-After.
type rateError struct{ after time.Duration }

func (e *rateError) Error() string {
	return fmt.Sprintf("slack: rate limited, retry after %s", e.after)
}

func (a *slackAPI) call(token, method string, body any, out any) error {
	var req *http.Request
	var err error
	switch b := body.(type) {
	case url.Values:
		req, err = http.NewRequest("POST", a.base+method, strings.NewReader(b.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	default:
		raw, _ := json.Marshal(b)
		req, err = http.NewRequest("POST", a.base+method, bytes.NewReader(raw))
		if err == nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
	}
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &rateError{after: time.Duration(max(secs, 1)) * time.Second}
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("slack %s: %s", method, resp.Status)
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("slack %s: %s", method, resp.Status)
	}
	if !head.OK {
		return &apiError{Code: orStr(head.Error, "unknown_error")}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// authInfo is auth.test: which workspace and which bot the token is.
type authInfo struct {
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	User   string `json:"user"`    // the bot user's name
	UserID string `json:"user_id"` // the bot user's id: <@UserID> mentions it
	BotID  string `json:"bot_id"`
	URL    string `json:"url"`
}

func (a *slackAPI) authTest(bot string) (authInfo, error) {
	var out authInfo
	err := a.call(bot, "auth.test", url.Values{}, &out)
	if ae, ok := err.(*apiError); ok && ae.Code == "invalid_auth" {
		return out, fmt.Errorf("the bot token is not valid (invalid_auth) — copy it again from OAuth & Permissions")
	}
	return out, err
}

// connectionsOpen asks for a Socket Mode URL (the app-level token).
func (a *slackAPI) connectionsOpen(app string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := a.call(app, "apps.connections.open", url.Values{}, &out)
	if ae, ok := err.(*apiError); ok && (ae.Code == "invalid_auth" || ae.Code == "not_allowed_token_type") {
		return "", fmt.Errorf("the app-level token is not valid (%s) — it starts with xapp- and has connections:write", ae.Code)
	}
	return out.URL, err
}

// postMessage posts text (mrkdwn) and returns the message's ts.
func (a *slackAPI) postMessage(bot, channel, thread, text string) (string, error) {
	body := map[string]any{"channel": channel, "text": text, "mrkdwn": true, "unfurl_links": false}
	if thread != "" {
		body["thread_ts"] = thread
	}
	var out struct {
		TS string `json:"ts"`
	}
	err := a.call(bot, "chat.postMessage", body, &out)
	return out.TS, err
}

// setStatus shows (or, with "", clears) the assistant thread's status line.
func (a *slackAPI) setStatus(bot, channel, thread, status string) error {
	return a.call(bot, "assistant.threads.setStatus", map[string]string{"channel_id": channel, "thread_ts": thread, "status": status}, nil)
}

func (a *slackAPI) userName(bot, id string) (string, error) {
	var out struct {
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := a.call(bot, "users.info", url.Values{"user": {id}}, &out); err != nil {
		return "", err
	}
	u := out.User
	return orStr(u.Profile.DisplayName, orStr(u.Profile.RealName, orStr(u.RealName, u.Name))), nil
}

func (a *slackAPI) channelName(bot, id string) (string, error) {
	var out struct {
		Channel struct {
			Name string `json:"name"`
		} `json:"channel"`
	}
	if err := a.call(bot, "conversations.info", url.Values{"channel": {id}}, &out); err != nil {
		return "", err
	}
	return out.Channel.Name, nil
}
