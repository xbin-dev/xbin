// platform.go — the seam between this bridge and a chat platform. The bridge
// does everything platform-independent: the agent-inbox contract, storing
// events before they are acknowledged, delivering them in order, attachments
// both ways, replies (split, retried, never posted twice), the typing hint,
// the page, identity links, restarts. A platform does only what is specific
// to it, behind the Platform interface below.
//
// To add one, write _backend/platform_<name>.go with a type that implements
// Platform and register it in an init():
//
//	func init() { registerPlatform("matrix", func(b Bridge) Platform { return &matrix{b: b} }) }
//
// AGENTS.md (next to this tile's xbin.json) is the full guide: what each
// field means to the agent, session semantics, attachments, identity links,
// formatting, testing. The built-in "console" platform (console.go) is a
// working example that needs no network.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Platform is one chat platform.
type Platform interface {
	// Info is what the page shows and asks for: the platform's name, the
	// secrets it needs (a field each; values go to this tile's vault), the
	// egress it needs, and setup steps.
	Info() Info

	// Start connects and receives until ctx ends or the connection fails —
	// return the error and the bridge starts it again after a backoff. For
	// each account it serves it calls b.Account once per start; for each
	// inbound event, b.Receive, which has stored the event durably when it
	// returns nil: acknowledge the event to the platform only then.
	Start(ctx context.Context, b Bridge) error

	// Send posts one message: text already in the platform's markup (Format)
	// and within its length (Limit), and its files (on the last piece of a
	// long reply). It returns the platform's id for the message. Wrap errors
	// retrying can't fix with Permanent; rate limits with RetryAfter.
	Send(ctx context.Context, account string, to Address, msg Outgoing) (ref string, err error)

	// Typing shows (on) or clears a "working on it" indicator in that
	// conversation, where the platform has one; otherwise return nil.
	Typing(ctx context.Context, account string, to Address, on bool) error

	// Format turns the agent's Markdown into the platform's markup (and
	// escapes what the platform would otherwise interpret). Limit is the
	// longest text one message may carry; the bridge splits longer replies.
	Format(markdown string) string
	Limit() int

	// Fetch downloads an inbound attachment the platform only referenced
	// (FileRef.URL / FileRef.ID), with the account's credentials. Platforms
	// that deliver bytes inline (FileRef.Data) never see it called.
	Fetch(ctx context.Context, account string, f FileRef) (io.ReadCloser, error)
}

// Bridge is what a platform uses of the bridge.
type Bridge interface {
	// Account announces an account this platform serves (a workspace, a
	// bot, a server): one channel on the agent each, claimed there.
	Account(ctx context.Context, a Account) error
	// Receive stores one inbound event and queues it for the agent.
	Receive(e Event) error
	// Secret reads one of the platform's secrets (Info.Secrets) — "" when unset.
	Secret(name string) string
	// Store is a key-value store for the platform's own state (cursors,
	// caches); keys are private to the platform.
	Store() Store
	// Logf records a line in the page's recent activity.
	Logf(format string, args ...any)
}

// Store is the tile's kv.
type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, val []byte) error
	Delete(key string) error
	List(prefix string) ([]string, error)
}

// Info describes a platform to the page.
type Info struct {
	Name    string        `json:"name"`             // the agent's `platform`: "matrix", "discord", …
	Title   string        `json:"title"`            // "Matrix"
	Secrets []SecretField `json:"secrets"`          // what the page asks for (stored in the vault)
	Egress  string        `json:"egress,omitempty"` // the net rule it needs: "internet:*.example.com:443" ("" = none)
	Setup   []string      `json:"setup,omitempty"`  // steps for the page (plain text, one per step)
}

// SecretField is one secret the page asks for.
type SecretField struct {
	Name   string `json:"name"`             // vault key ("bot-token")
	Label  string `json:"label"`            // "Bot token"
	Hint   string `json:"hint,omitempty"`   // where to find it
	Prefix string `json:"prefix,omitempty"` // a prefix a valid value starts with ("" = any)
}

// Account is one account a platform serves.
type Account struct {
	ID       string   `json:"id"`                 // stable: the workspace/team/server id
	Name     string   `json:"name"`               // shown to people: "Acme"
	BotID    string   `json:"botId,omitempty"`    // the bot's own user id (its mentions)
	BotName  string   `json:"botName,omitempty"`  // "agentbot"
	Features []string `json:"features,omitempty"` // informational: "threads", "files", …
}

// Event is one inbound message, in the agent-inbox contract's terms.
type Event struct {
	Account      string       `json:"account"`             // an Account.ID
	ID           string       `json:"id"`                  // the platform's id for this event: a retry carries the same (dedupe)
	Conversation Conversation `json:"conversation"`        // where it was said
	Thread       string       `json:"thread,omitempty"`    // the thread's ROOT message id, when it is in a thread
	MessageID    string       `json:"messageId,omitempty"` // this message's own id
	// AssistantThread marks a platform's dedicated assistant pane: every
	// thread there is a conversation of its own.
	AssistantThread bool      `json:"assistantThread,omitempty"`
	Sender          Person    `json:"sender"`
	Mentioned       bool      `json:"mentioned,omitempty"` // it addresses the bot: every DM; in groups a mention (removed from Text) or a reply to the bot
	Text            string    `json:"text"`                // plain text: the platform's markup turned into words
	Command         string    `json:"command,omitempty"`   // a command the platform parsed: "new hello" (text starting with "/" works too)
	Files           []FileRef `json:"files,omitempty"`
}

// Conversation is where a message was said.
type Conversation struct {
	ID   string `json:"id"`
	Type string `json:"type"`           // dm | group (a multi-person DM) | channel
	Name string `json:"name,omitempty"` // "general" — shown to the agent and the owner
}

// Person is a message's sender.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Bot  bool   `json:"bot,omitempty"` // bots (and the bridge's own messages) are never delivered
}

// FileRef is an inbound attachment: inline bytes, or a reference Fetch reads.
type FileRef struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
	URL  string `json:"url,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// Address is where a reply goes (the agent's outbox row).
type Address struct {
	Conversation string `json:"conversation"`
	Type         string `json:"type"`
	Thread       string `json:"thread,omitempty"`
	User         string `json:"user,omitempty"`
}

// Outgoing is one message to post.
type Outgoing struct {
	Kind  string // answer | question | approval | error | notice | announce
	Text  string // in the platform's markup, within Limit
	Files []OutFile
}

// OutFile is a file the agent attached to its reply.
type OutFile struct {
	Name string
	Mime string
	Data []byte
}

// --- errors --------------------------------------------------------------------

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks an error retrying can't fix (no such conversation, no
// permission, a message too long): the reply is reported failed at once.
func Permanent(err error) error { return permanentError{err} }

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

type retryAfterError struct {
	after time.Duration
	err   error
}

func (e retryAfterError) Error() string { return fmt.Sprintf("%v (retry after %s)", e.err, e.after) }
func (e retryAfterError) Unwrap() error { return e.err }

// RetryAfter marks a rate limit: the bridge waits d and tries again.
func RetryAfter(d time.Duration, err error) error { return retryAfterError{d, err} }

func retryAfter(err error) (time.Duration, bool) {
	var r retryAfterError
	if errors.As(err, &r) {
		return r.after, true
	}
	return 0, false
}

// --- the registry ------------------------------------------------------------------

var platforms = struct {
	sync.Mutex
	m map[string]func(Bridge) Platform
}{m: map[string]func(Bridge) Platform{}}

// registerPlatform makes a platform available (call it from an init()).
func registerPlatform(name string, make func(Bridge) Platform) {
	platforms.Lock()
	defer platforms.Unlock()
	platforms.m[name] = make
}

// platformNames lists the registered platforms, the console last.
func platformNames() []string {
	platforms.Lock()
	defer platforms.Unlock()
	var out []string
	for n := range platforms.m {
		if n != "console" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if _, ok := platforms.m["console"]; ok {
		out = append(out, "console")
	}
	return out
}

// choosePlatform is the configured one, else the first real one, else the
// console.
func choosePlatform(configured string) string {
	names := platformNames()
	for _, n := range names {
		if n == configured {
			return n
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func newPlatform(name string, b Bridge) Platform {
	platforms.Lock()
	defer platforms.Unlock()
	if mk := platforms.m[name]; mk != nil {
		return mk(b)
	}
	return nil
}
