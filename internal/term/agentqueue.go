package term

// MESSAGES WHILE A TURN RUNS (D197): what the Agent tab's Send does while
// the agent is busy. A message for a busy session goes to the running turn
// where the agent takes messages mid-turn (sdk/acp Steer over the
// adapters' _session/steering: claude-agent-acp, codex-acp — taken at the
// agent's next step), else it is HELD here and sent as the next prompt when
// the turn ends; held messages go in together, in order, as one prompt (as
// many as one prompt's attachment limits allow). Once something is held,
// later messages are held behind it rather than steered past it.
//
// The queue is the session's, not a client's: every client sees it (a
// `queue` event in the log carries the held items after each change, and
// the tail page's state carries them now), any driving client takes a
// message back (AgentUnqueue — the tab's Stop takes them all back into the
// composer before it cancels), and it survives a cancel: a message held
// with interrupt (⌘/Ctrl+Enter in the tab) cancels the running turn and
// goes in when that turn has ended. The session's end drops what is still
// held, said in the log (a notice).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/util"
)

// EvQueue is the session's held messages after a change ({items, sent?,
// dropped?, reason?}; docs/protocol.md): logged by xbin, not the agent.
const EvQueue = "queue"

// Queue outcomes (QueueResult.Outcome).
const (
	QueuePrompted = "prompted" // the session was idle: it started a turn, as a prompt does
	QueueSteered  = "steered"  // the running turn took it (a user message.delta with steered:true)
	QueueHeld     = "queued"   // held: it goes in when the turn ends
)

const (
	maxQueued      = 16              // held messages per session
	flushTimeout   = 2 * time.Minute // one flush's prompt (its files' hand-off)
	queueEventText = 200             // a held message's text as the event shows it (runes)
)

// Errors the API maps to statuses.
var (
	ErrQueueFull  = fmt.Errorf("%d messages are already waiting for this turn to end — take one back or wait", maxQueued)
	ErrNoQueued   = errors.New("no such queued message")
	ErrQueueTaken = errors.New("another message for this session is being taken — try again")
)

// Queued is one held message as the queue event, the tail page and the API
// show it: its text (cut to a preview in events; whole when taken back)
// and its attachments' names.
type Queued struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
	At    int64    `json:"at"` // unix ms, when it was held
}

// QueueResult is how a message was taken (POST …/queue's answer).
type QueueResult struct {
	Outcome string `json:"outcome"`
	Turn    uint64 `json:"turn,omitempty"` // prompted: the turn it started
	ID      string `json:"id,omitempty"`   // queued: the held message (DELETE …/queue/{id} takes it back)
}

// queueItem is a held message and its prompt.
type queueItem struct {
	q Queued
	p agent.Prompt
}

// queueData is EvQueue's payload.
type queueData struct {
	Items   []Queued `json:"items"`
	Sent    []string `json:"sent,omitempty"`    // ids that just went in as a prompt
	Dropped int      `json:"dropped,omitempty"` // held messages the session's end dropped
	Reason  string   `json:"reason,omitempty"`
}

// busyLocked: a turn runs (or waits for an answer, or is being cancelled).
func (st *agentState) busyLocked() bool {
	return st.status == agent.StatusRunning || st.status == agent.StatusWaiting || st.status == agent.StatusCancelling
}

// ReserveQueue takes the session's one queueing slot before the request's
// body is read (like ReservePrompt: a message's files are tens of MiB
// decoded), so concurrent messages wait their turn instead of buffering.
func (m *Manager) ReserveQueue(id string) (release func(), err error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	if !st.queueing.CompareAndSwap(false, true) {
		return nil, ErrQueueTaken
	}
	return func() { st.queueing.Store(false) }, nil
}

// AgentQueue takes a message for session id whatever it is doing: idle, it
// starts a turn (QueuePrompted); busy, the running turn takes it where the
// agent steers and nothing is held yet (QueueSteered), else it is held for
// the turn's end (QueueHeld). interrupt holds it and cancels the running
// turn, so it goes in once that turn has ended.
func (m *Manager) AgentQueue(ctx context.Context, id string, p agent.Prompt, interrupt bool) (QueueResult, error) {
	s, st, err := m.agentOf(id)
	if err != nil {
		return QueueResult{}, err
	}
	select {
	case <-st.ready:
	case <-st.done:
		return QueueResult{}, agent.ErrEnded
	case <-ctx.Done():
		return QueueResult{}, ctx.Err()
	}
	st.mu.Lock()
	startErr, busy, held := st.startErr, st.busyLocked(), len(st.queue) > 0
	st.mu.Unlock()
	if startErr != nil {
		return QueueResult{}, fmt.Errorf("the agent did not start: %w", startErr)
	}
	if !busy && !held { // nothing to wait for: a prompt
		turn, err := m.AgentPromptWith(ctx, id, p)
		if err == nil {
			return QueueResult{Outcome: QueuePrompted, Turn: turn}, nil
		}
		if !errors.Is(err, agent.ErrBusy) {
			return QueueResult{}, err
		}
		// a turn started meanwhile: take it as for a busy session
	}
	if sr, ok := st.drv.(agent.Steerer); ok && !interrupt && !held {
		out, err := sr.Steer(ctx, p)
		switch {
		case err == nil && (out == agent.SteerInjected || out == agent.SteerStartedNewTurn):
			s.hub.Touch()
			return QueueResult{Outcome: QueueSteered}, nil
		case err == nil && out == agent.SteerPromptRequired: // the turn ended as we asked
			turn, perr := m.AgentPromptWith(ctx, id, p)
			if perr == nil {
				return QueueResult{Outcome: QueuePrompted, Turn: turn}, nil
			}
			if !errors.Is(perr, agent.ErrBusy) {
				return QueueResult{}, perr
			}
		case err != nil && !errors.Is(err, agent.ErrSteeringUnsupported):
			return QueueResult{}, err // refused (its files, the session's end): not held either
		}
	}
	item, err := s.hold(m, p)
	if err != nil {
		return QueueResult{}, err
	}
	if interrupt {
		st.mu.Lock()
		running := st.busyLocked()
		st.mu.Unlock()
		if running {
			if err := st.drv.Cancel(); err != nil {
				return QueueResult{Outcome: QueueHeld, ID: item.ID}, nil // held all the same: it goes in when the turn ends
			}
		}
	}
	m.kickQueue(s) // the turn may have ended while it was being held
	return QueueResult{Outcome: QueueHeld, ID: item.ID}, nil
}

// hold appends p to the session's queue and logs the queue.
func (s *Session) hold(m *Manager, p agent.Prompt) (Queued, error) {
	st := s.agent
	q := Queued{ID: util.RandomToken(6), Text: p.Text, At: time.Now().UnixMilli()}
	for _, a := range p.Attachments {
		q.Files = append(q.Files, a.Name)
	}
	st.mu.Lock()
	if len(st.queue) >= maxQueued {
		st.mu.Unlock()
		return Queued{}, ErrQueueFull
	}
	bytes := attachBytes(p)
	for _, it := range st.queue {
		bytes += attachBytes(it.p)
	}
	if bytes > maxQueuedBytes {
		st.mu.Unlock()
		return Queued{}, fmt.Errorf("%w: the waiting messages' files would come to more than %d MiB — take one back or wait", agent.ErrAttachmentTooLarge, maxQueuedBytes>>20)
	}
	st.queue = append(st.queue, queueItem{q: q, p: p})
	items := st.queuedLocked()
	st.mu.Unlock()
	s.logEvent(m, agent.New(EvQueue, queueData{Items: items}))
	return q, nil
}

// maxQueuedBytes bounds what the held messages' files keep in memory
// together: two prompts' worth.
const maxQueuedBytes = 2 * agent.MaxAttachmentsBytes

func attachBytes(p agent.Prompt) int {
	n := 0
	for _, a := range p.Attachments {
		n += len(a.Data)
	}
	return n
}

// queuedLocked is the held messages as the event shows them (st.mu held).
func (st *agentState) queuedLocked() []Queued {
	out := make([]Queued, 0, len(st.queue))
	for _, it := range st.queue {
		q := it.q
		q.Text = preview(q.Text)
		out = append(out, q)
	}
	return out
}

func preview(s string) string {
	r := []rune(s)
	if len(r) <= queueEventText {
		return s
	}
	return string(r[:queueEventText]) + "…"
}

// AgentQueued is the session's held messages now (the tail page's state).
func (m *Manager) AgentQueued(id string) ([]Queued, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.queuedLocked(), nil
}

// AgentUnqueue takes held messages back (qid; "" = all of them) and
// returns them whole, so a client can put their text back where it was
// typed. Their files are not returned. ErrNoQueued when qid is not held
// (already sent, or taken back by another client).
func (m *Manager) AgentUnqueue(id, qid string) ([]Queued, error) {
	s, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	var taken []Queued
	keep := st.queue[:0:0]
	for _, it := range st.queue {
		if qid == "" || it.q.ID == qid {
			taken = append(taken, it.q)
		} else {
			keep = append(keep, it)
		}
	}
	if qid != "" && len(taken) == 0 {
		st.mu.Unlock()
		return nil, ErrNoQueued
	}
	st.queue = keep
	items := st.queuedLocked()
	st.mu.Unlock()
	if len(taken) > 0 {
		s.logEvent(m, agent.New(EvQueue, queueData{Items: items}))
	}
	if taken == nil {
		taken = []Queued{}
	}
	return taken, nil
}

// KickQueue sends what is held when the session is idle — after a prompt
// that was refused (it never started the turn whose end would have sent
// them).
func (m *Manager) KickQueue(id string) {
	if s, _, err := m.agentOf(id); err == nil {
		m.kickQueue(s)
	}
}

// kickQueue starts a flush when something is held and no turn runs.
func (m *Manager) kickQueue(s *Session) {
	st := s.agent
	st.mu.Lock()
	go1 := len(st.queue) > 0 && !st.flushing && st.queueSendable()
	st.mu.Unlock()
	if go1 {
		go m.flushQueue(s)
	}
}

// queueSendable: the session can take the queue as a prompt now — its turn
// ended (idle, or the turn failed) and no prompt is being taken (st.mu
// held).
func (st *agentState) queueSendable() bool {
	return (st.status == agent.StatusIdle || st.status == agent.StatusError) && !st.prompting.Load()
}

// flushQueue sends what is held as one prompt: the messages in order, their
// texts one paragraph each, their files together — as many messages as one
// prompt's limits allow (the rest wait for that turn's end). A prompt the
// session refuses because a turn started meanwhile puts them back; refused
// for good (the session ended, its files refused), they are dropped and
// the log says why.
func (m *Manager) flushQueue(s *Session) {
	st := s.agent
	st.mu.Lock()
	if st.flushing || len(st.queue) == 0 || !st.queueSendable() {
		st.mu.Unlock()
		return
	}
	st.flushing = true
	n := batchLen(st.queue)
	batch := append([]queueItem(nil), st.queue[:n]...)
	st.queue = append(st.queue[:0:0], st.queue[n:]...)
	st.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	_, err := m.AgentPromptWith(ctx, s.ID, joinPrompts(batch))
	cancel()

	ids := make([]string, len(batch))
	for i, it := range batch {
		ids[i] = it.q.ID
	}
	st.mu.Lock()
	st.flushing = false
	var data queueData
	switch {
	case err == nil:
		data = queueData{Sent: ids}
	case errors.Is(err, agent.ErrBusy): // a prompt went in first: they wait for its turn's end
		st.queue = append(batch, st.queue...)
	default:
		data = queueData{Dropped: len(batch), Reason: err.Error()}
	}
	data.Items = st.queuedLocked()
	st.mu.Unlock()
	if err != nil && errors.Is(err, agent.ErrBusy) {
		return // nothing changed that a client sees
	}
	s.logEvent(m, agent.New(EvQueue, data))
	if err != nil {
		s.logEvent(m, agent.New(EvNotice, noticeData{Text: fmt.Sprintf("%s: %v", notSent(len(batch)), err)}))
	}
}

// batchLen is how many held messages, from the first, one prompt can carry
// (its attachment limits); always at least one.
func batchLen(q []queueItem) int {
	files, bytes := 0, 0
	for i, it := range q {
		files += len(it.p.Attachments)
		bytes += attachBytes(it.p)
		if i > 0 && (files > agent.MaxAttachments || bytes > agent.MaxAttachmentsBytes) {
			return i
		}
	}
	return len(q)
}

// joinPrompts is held messages as one prompt: their texts in order, a blank
// line between, and their files.
func joinPrompts(batch []queueItem) agent.Prompt {
	if len(batch) == 1 {
		return batch[0].p
	}
	var p agent.Prompt
	for _, it := range batch {
		if it.p.Text != "" {
			if p.Text != "" {
				p.Text += "\n\n"
			}
			p.Text += it.p.Text
		}
		p.Attachments = append(p.Attachments, it.p.Attachments...)
	}
	return p
}

// dropQueue: the session ended with messages held — they go, and the log
// says so (the pump, before the session's teardown).
func (s *Session) dropQueue(m *Manager, why string) {
	st := s.agent
	st.mu.Lock()
	n := len(st.queue)
	st.queue = nil
	st.mu.Unlock()
	if n == 0 {
		return
	}
	s.logEvent(m, agent.New(EvQueue, queueData{Items: []Queued{}, Dropped: n, Reason: why}))
	s.logEvent(m, agent.New(EvNotice, noticeData{Text: fmt.Sprintf("%s: %s", notSent(n), why)}))
}

func notSent(n int) string {
	if n == 1 {
		return "1 queued message was not sent"
	}
	return fmt.Sprintf("%d queued messages were not sent", n)
}
