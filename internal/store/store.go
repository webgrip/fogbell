// Package store keeps fogbell's incidents: the Slack messages that carry their state, read back from
// channel history, an in-memory index over them, and the per-incident locks.
//
// The messages are the store (docs/adrs/0001-the-slack-message-is-the-only-store.md); the index is a
// cache of them that a restart rebuilds.
package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
)

// History reads a channel's messages since a time, newest first.
type History interface {
	History(ctx context.Context, channel string, oldest float64) ([]slack.Message, error)
}

// Entry is the latest message of an incident and its state.
type Entry struct {
	Channel string
	TS      string
	State   state.Incident
}

// Child is a group folded into a parent incident's thread.
type Child struct {
	Channel, TS, Parent string
	Count               int
}

// Message is a channel message that carries incident state.
type Message struct {
	Channel string
	Msg     slack.Message
	State   *state.Incident
}

// Events names the metadata event types the store reads and writes.
type Events struct {
	Incident string
	Adopt    []string
}

// Store is the index of incidents. The zero value is not usable; build it with New.
type Store struct {
	history History
	events  Events
	window  float64
	now     func() float64
	log     *slog.Logger
	failed  func()

	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	scanLocks map[string]*sync.Mutex
	scanned   map[string]bool
	index     map[string]Entry
	children  map[string]Child
	groups    map[string]*alert.Group
}

// Options configure a Store.
type Options struct {
	History History
	Events  Events
	// Window is how far back, in seconds, a scan reads.
	Window float64
	Now    func() float64
	Log    *slog.Logger
	// HistoryFailed is called when a history read fails.
	HistoryFailed func()
}

// New returns an empty store.
func New(o Options) *Store {
	s := &Store{history: o.History, events: o.Events, window: o.Window, now: o.Now, log: o.Log, failed: o.HistoryFailed}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.failed == nil {
		s.failed = func() {}
	}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.locks = map[string]*sync.Mutex{}
	s.scanLocks = map[string]*sync.Mutex{}
	s.scanned = map[string]bool{}
	s.index = map[string]Entry{}
	s.children = map[string]Child{}
	s.groups = map[string]*alert.Group{}
}

// Lock takes an incident's lock and returns its release. A parent's lock may be taken while a child's
// is held, never the other way round.
func (s *Store) Lock(key string) func() {
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Decode reads incident state from a message: its own event type, or one it adopts.
func (s *Store) Decode(m *slack.Message) (*state.Incident, bool) {
	var st state.Incident
	for _, ev := range append([]string{s.events.Incident}, s.events.Adopt...) {
		if m.Payload(ev, &st) {
			return &st, true
		}
	}
	return nil, false
}

// Metadata is the metadata that stores an incident's state on its message.
func (s *Store) Metadata(st *state.Incident) *slack.Metadata {
	raw, _ := json.Marshal(st)
	return &slack.Metadata{EventType: s.events.Incident, EventPayload: raw}
}

// Messages is a channel's messages since a time; empty, and counted, when history is unreadable.
func (s *Store) Messages(ctx context.Context, channel string, since float64) []slack.Message {
	msgs, err := s.history.History(ctx, channel, since)
	if err != nil {
		s.failed()
		s.log.Warn("conversations.history unavailable", "channel", channel, "error", err)
		return nil
	}
	return msgs
}

// Incidents is every incident message in a channel since a time, newest first.
func (s *Store) Incidents(ctx context.Context, channel string, since float64) []Message {
	var out []Message
	for _, m := range s.Messages(ctx, channel, since) {
		if st, ok := s.Decode(&m); ok {
			out = append(out, Message{Channel: channel, Msg: m, State: st})
		}
	}
	return out
}

// Scan reads a channel's incidents of the window into the index, once per channel. The newest message
// per group key wins, so a scan never replaces a newer entry.
func (s *Store) Scan(ctx context.Context, channel string) {
	s.mu.Lock()
	sl, ok := s.scanLocks[channel]
	if !ok {
		sl = &sync.Mutex{}
		s.scanLocks[channel] = sl
	}
	s.mu.Unlock()
	sl.Lock()
	defer sl.Unlock()
	s.mu.Lock()
	done := s.scanned[channel]
	s.mu.Unlock()
	if done {
		return
	}
	found := s.Incidents(ctx, channel, s.now()-s.window)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(found) - 1; i >= 0; i-- {
		m := found[i]
		if m.State.GroupKey == "" {
			continue
		}
		if cur, has := s.index[m.State.GroupKey]; has && older(m.Msg.TS, cur.TS) {
			continue
		}
		s.index[m.State.GroupKey] = Entry{Channel: channel, TS: m.Msg.TS, State: *m.State}
		for k, n := range m.State.ChildMap() {
			s.children[k] = Child{Channel: channel, TS: m.Msg.TS, Parent: m.State.GroupKey, Count: n}
		}
	}
	s.scanned[channel] = true
}

func older(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x < y
}

// Find is a key's latest incident in a channel, scanning the channel first.
func (s *Store) Find(ctx context.Context, channel, key string) (Entry, bool) {
	s.Scan(ctx, channel)
	en, ok := s.Get(key)
	if !ok || en.Channel != channel {
		return Entry{}, false
	}
	return en, true
}

// Get is a key's latest incident in any channel.
func (s *Store) Get(key string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	en, ok := s.index[key]
	return en, ok
}

// Put records a key's latest incident.
func (s *Store) Put(key string, en Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index[key] = en
}

// Each calls f for a copy of every indexed incident.
func (s *Store) Each(f func(key string, en Entry)) {
	s.mu.Lock()
	snapshot := make(map[string]Entry, len(s.index))
	for k, v := range s.index {
		snapshot[k] = v
	}
	s.mu.Unlock()
	for k, v := range snapshot {
		f(k, v)
	}
}

// Group is the latest notification of a key, kept to re-render its open incident; nil after a restart.
func (s *Store) Group(key string) *alert.Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groups[key]
}

// SetGroup records the latest notification of a key, or forgets it with nil.
func (s *Store) SetGroup(key string, g *alert.Group) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g == nil {
		delete(s.groups, key)
		return
	}
	s.groups[key] = g
}

// Child is where a folded group's notifications go.
func (s *Store) Child(key string) (Child, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.children[key]
	return c, ok
}

// SetChild records a folded group, or forgets it with a negative count.
func (s *Store) SetChild(key string, c Child) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Count < 0 {
		delete(s.children, key)
		return
	}
	s.children[key] = c
}

// Forget drops everything held in memory, as a restart would.
func (s *Store) Forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
}
