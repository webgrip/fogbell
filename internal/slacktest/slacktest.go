// Package slacktest is an in-memory Slack Web API for tests: enough of chat.*, conversations.*,
// views.open, users.info and the external file upload to run the engine against. History ignores
// oldest: every stored message is recent enough.
package slacktest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/webgrip/fogbell/internal/slack"
)

// Reply is a thread reply.
type Reply struct {
	Channel, ThreadTS, TS, Text string
}

// Upload is a finished file upload.
type Upload struct {
	Filename, Channel, ThreadTS, Comment string
	Data                                 []byte
}

// Server is the fake API. Read its fields under Lock.
type Server struct {
	*httptest.Server
	sync.Mutex

	Messages map[string]*Stored
	Replies  []Reply
	Views    []json.RawMessage
	Uploads  []Upload
	Posted   []string
	Calls    []string
	// Channels maps names to ids for conversations.list; a name missing here resolves to "C" + name.
	Channels map[string]string
	// Closed channels refuse a post with not_in_channel.
	Closed map[string]bool
	Email  string
	// Fail makes a method answer with an error code.
	Fail map[string]string
	// ReplyClock stamps thread replies; nil uses a counter.
	ReplyClock func() float64

	seq     int
	pending map[string][]byte
}

// Stored is a top-level message.
type Stored struct {
	Channel string
	slack.Message
}

// New starts the fake.
func New() *Server {
	s := &Server{Messages: map[string]*Stored{}, Closed: map[string]bool{}, Fail: map[string]string{},
		Email: "Pat@Example.com", pending: map[string][]byte{}, seq: 1000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Client is a slack.Client pointed at the fake.
func (s *Server) Client() *slack.Client {
	c := slack.New("xoxb-test", s.Server.Client())
	c.BaseURL = s.URL + "/api/"
	c.Sleep = func(time.Duration) {}
	return c
}

// Only is the one top-level message; it fails the caller when there is not exactly one.
func (s *Server) Only() (*Stored, error) {
	s.Lock()
	defer s.Unlock()
	if len(s.Messages) != 1 {
		return nil, fmt.Errorf("want one message, have %d", len(s.Messages))
	}
	for _, m := range s.Messages {
		cp := *m
		return &cp, nil
	}
	return nil, nil
}

// Newest is the newest top-level message.
func (s *Server) Newest() *Stored {
	s.Lock()
	defer s.Unlock()
	var best *Stored
	for _, m := range s.Messages {
		if best == nil || less(best.TS, m.TS) {
			best = m
		}
	}
	if best == nil {
		return nil
	}
	cp := *best
	return &cp
}

// LastReply is the text of the latest thread reply.
func (s *Server) LastReply() string {
	s.Lock()
	defer s.Unlock()
	if len(s.Replies) == 0 {
		return ""
	}
	return s.Replies[len(s.Replies)-1].Text
}

// Put stores a message as if it had been posted earlier.
func (s *Server) Put(channel string, m slack.Message) string {
	s.Lock()
	defer s.Unlock()
	if m.TS == "" {
		s.seq++
		m.TS = fmt.Sprintf("%d.000100", s.seq)
	}
	s.Messages[m.TS] = &Stored{Channel: channel, Message: m}
	return m.TS
}

func less(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x < y
}

func (s *Server) id(name string) string {
	name = strings.TrimPrefix(name, "#")
	if id, ok := s.Channels[name]; ok {
		return id
	}
	if strings.HasPrefix(name, "C") && strings.ToUpper(name) == name {
		return name
	}
	return "C" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/upload/") {
		data, _ := io.ReadAll(r.Body)
		s.Lock()
		s.pending[strings.TrimPrefix(r.URL.Path, "/upload/")] = data
		s.Unlock()
		if s.Fail["upload"] != "" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	s.Lock()
	defer s.Unlock()
	s.Calls = append(s.Calls, method)
	answer := func(v map[string]any) {
		v["ok"] = true
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code string) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code}) }
	if code := s.Fail[method]; code != "" {
		fail(code)
		return
	}
	q := r.URL.Query()
	var body map[string]json.RawMessage
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(body[k], &v)
		return v
	}
	switch method {
	case "conversations.list":
		var chans []map[string]string
		for name, id := range s.Channels {
			chans = append(chans, map[string]string{"name": name, "id": id})
		}
		answer(map[string]any{"channels": chans})
	case "chat.postMessage":
		channel := s.id(str("channel"))
		if ts := str("thread_ts"); ts != "" {
			s.seq++
			at := fmt.Sprintf("%d.000200", 1793089000+s.seq)
			if s.ReplyClock != nil {
				at = strconv.FormatFloat(s.ReplyClock(), 'f', 6, 64)
			}
			s.Replies = append(s.Replies, Reply{Channel: channel, ThreadTS: ts, TS: at, Text: str("text")})
			answer(map[string]any{"ts": at, "channel": channel})
			return
		}
		if s.Closed[channel] {
			fail("not_in_channel")
			return
		}
		var m slack.Message
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &m)
		s.seq++
		m.TS = fmt.Sprintf("%d.000100", s.seq)
		s.Messages[m.TS] = &Stored{Channel: channel, Message: m}
		s.Posted = append(s.Posted, channel)
		answer(map[string]any{"ts": m.TS, "channel": channel})
	case "chat.update":
		m, ok := s.Messages[str("ts")]
		if !ok {
			fail("message_not_found")
			return
		}
		var u slack.Message
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &u)
		m.Text, m.Attachments, m.Metadata = u.Text, u.Attachments, u.Metadata
		answer(map[string]any{})
	case "conversations.history":
		channel := q.Get("channel")
		latest := q.Get("latest")
		var out []slack.Message
		for _, m := range s.Messages {
			if m.Channel != channel {
				continue
			}
			if latest != "" {
				if at, _ := strconv.ParseFloat(latest, 64); at != mustFloat(m.TS) {
					continue
				}
			}
			out = append(out, m.Message)
		}
		sort.Slice(out, func(i, j int) bool { return less(out[j].TS, out[i].TS) })
		answer(map[string]any{"messages": out})
	case "conversations.replies":
		ts := q.Get("ts")
		out := []map[string]string{{"ts": ts, "text": "parent"}}
		for _, rp := range s.Replies {
			if rp.ThreadTS == ts {
				out = append(out, map[string]string{"ts": rp.TS, "text": rp.Text})
			}
		}
		answer(map[string]any{"messages": out})
	case "users.info":
		answer(map[string]any{"user": map[string]any{"id": q.Get("user"), "profile": map[string]string{"email": s.Email}}})
	case "views.open":
		s.Views = append(s.Views, body["view"])
		answer(map[string]any{})
	case "files.getUploadURLExternal":
		s.seq++
		id := fmt.Sprintf("F%d", s.seq)
		answer(map[string]any{"upload_url": s.URL + "/upload/" + id, "file_id": id})
	case "files.completeUploadExternal":
		var files []map[string]string
		_ = json.Unmarshal(body["files"], &files)
		for _, f := range files {
			data, ok := s.pending[f["id"]]
			if !ok {
				fail("file_not_found")
				return
			}
			s.Uploads = append(s.Uploads, Upload{Filename: f["title"], Channel: str("channel_id"), ThreadTS: str("thread_ts"),
				Comment: str("initial_comment"), Data: data})
		}
		answer(map[string]any{})
	default:
		fail("unknown_method")
	}
}

func mustFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
