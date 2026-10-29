// Package slack is the part of the Slack Web API fogbell uses, over plain HTTP, and the request
// signature check for interactions.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Error is an API answer that is not ok, or a failed request.
type Error struct {
	Method string
	Code   string
}

func (e *Error) Error() string { return e.Method + ": " + e.Code }

// Is reports whether err is a Slack error with one of the codes.
func Is(err error, codes ...string) bool {
	var se *Error
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Code == c {
			return true
		}
	}
	return false
}

// Recorder counts API calls by method and result.
type Recorder func(method, result string)

// Client calls the Slack Web API with a bot token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Record  Recorder
	Log     *slog.Logger
	// Sleep waits out a rate limit; tests replace it.
	Sleep func(time.Duration)

	mu       sync.Mutex
	channels map[string]string
}

// New returns a client for the public Slack API.
func New(token string, httpClient *http.Client) *Client {
	return &Client{BaseURL: "https://slack.com/api/", Token: token, HTTP: httpClient}
}

func (c *Client) record(method, result string) {
	if c.Record != nil {
		c.Record(method, result)
	}
}

func (c *Client) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Call posts a JSON payload to a method and decodes the answer into out.
func (c *Client) Call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.do(ctx, method, http.MethodPost, nil, body, out)
}

// Get calls a method with query parameters and decodes the answer into out.
func (c *Client) Get(ctx context.Context, method string, params url.Values, out any) error {
	return c.do(ctx, method, http.MethodGet, params, nil, out)
}

func (c *Client) do(ctx context.Context, method, verb string, params url.Values, body []byte, out any) error {
	target := strings.TrimRight(c.BaseURL, "/") + "/" + method
	if params != nil {
		target += "?" + params.Encode()
	}
	for attempt := 0; attempt < 3; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, verb, target, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			c.record(method, "http_error")
			return &Error{Method: method, Code: "request failed: " + err.Error()}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if err != nil {
			c.record(method, "http_error")
			return &Error{Method: method, Code: "read failed"}
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if wait <= 0 {
				wait = 2
			}
			c.sleep(time.Duration(wait) * time.Second)
			continue
		}
		if resp.StatusCode >= 300 {
			c.record(method, "http_error")
			return &Error{Method: method, Code: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		var head struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			c.record(method, "error")
			return &Error{Method: method, Code: "invalid answer"}
		}
		if !head.OK {
			code := head.Error
			if code == "" {
				code = "error"
			}
			c.record(method, code)
			return &Error{Method: method, Code: code}
		}
		c.record(method, "ok")
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	return &Error{Method: method, Code: "rate limited"}
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// ChannelID resolves a channel name to its id. Without the channels:read scope it returns "#name";
// chat.postMessage accepts that, and its answer teaches the id (Learn).
func (c *Client) ChannelID(ctx context.Context, name string) string {
	name = strings.TrimPrefix(name, "#")
	c.mu.Lock()
	id, ok := c.channels[name]
	c.mu.Unlock()
	if ok {
		return id
	}
	if isID(name) {
		return name
	}
	id, err := c.listChannels(ctx, name)
	if err != nil {
		c.log().Warn("conversations.list unavailable; posting by name", "channel", name, "error", err)
		return "#" + name
	}
	return id
}

// isID reports whether s is a channel id rather than a name: Slack names are lower case.
func isID(s string) bool {
	if len(s) < 9 || s[0] != 'C' && s[0] != 'G' {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// Learn records the id a channel name resolved to.
func (c *Client) Learn(name, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channels == nil {
		c.channels = map[string]string{}
	}
	c.channels[strings.TrimPrefix(name, "#")] = id
}

func (c *Client) listChannels(ctx context.Context, name string) (string, error) {
	cursor := ""
	for {
		params := url.Values{"types": {"public_channel,private_channel"}, "limit": {"1000"}, "exclude_archived": {"true"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var body struct {
			Channels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channels"`
			Meta struct {
				Next string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.Get(ctx, "conversations.list", params, &body); err != nil {
			return "", err
		}
		for _, ch := range body.Channels {
			c.Learn(ch.Name, ch.ID)
		}
		c.mu.Lock()
		id, ok := c.channels[name]
		c.mu.Unlock()
		if ok {
			return id, nil
		}
		cursor = body.Meta.Next
		if cursor == "" {
			return "", &Error{Method: "conversations.list", Code: "channel #" + name + " not found (is the app invited?)"}
		}
	}
}

// History is a channel's messages since oldest, newest first, with their metadata. Without the
// channels:history scope, or for a channel addressed by name, it is empty.
func (c *Client) History(ctx context.Context, channel string, oldest float64) ([]Message, error) {
	return c.history(ctx, channel, oldest, 0, 200, 10)
}

// Message is one message by its ts; nil when it cannot be read.
func (c *Client) Message(ctx context.Context, channel, ts string) *Message {
	at, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return nil
	}
	msgs, err := c.history(ctx, channel, at, at, 1, 1)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	return &msgs[0]
}

func (c *Client) history(ctx context.Context, channel string, oldest, latest float64, limit, pages int) ([]Message, error) {
	if strings.HasPrefix(channel, "#") {
		return nil, nil
	}
	var out []Message
	cursor := ""
	for range pages {
		params := url.Values{"channel": {channel}, "oldest": {stamp(oldest)}, "limit": {strconv.Itoa(limit)},
			"include_all_metadata": {"true"}}
		if latest != 0 {
			params.Set("latest", stamp(latest))
			params.Set("inclusive", "true")
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var body struct {
			Messages []Message `json:"messages"`
			Meta     struct {
				Next string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.Get(ctx, "conversations.history", params, &body); err != nil {
			return nil, err
		}
		out = append(out, body.Messages...)
		cursor = body.Meta.Next
		if cursor == "" {
			break
		}
	}
	return out, nil
}

func stamp(ts float64) string { return strconv.FormatFloat(ts, 'f', 6, 64) }

// Replies is the replies under a message, without the message itself.
func (c *Client) Replies(ctx context.Context, channel, ts string) ([]Message, error) {
	var body struct {
		Messages []Message `json:"messages"`
	}
	params := url.Values{"channel": {channel}, "ts": {ts}, "limit": {"200"}}
	if err := c.Get(ctx, "conversations.replies", params, &body); err != nil {
		return nil, err
	}
	var out []Message
	for _, m := range body.Messages {
		if m.TS != ts {
			out = append(out, m)
		}
	}
	return out, nil
}

// Post is a chat.postMessage request.
type Post struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Metadata    *Metadata    `json:"metadata,omitempty"`
	UnfurlLinks bool         `json:"unfurl_links"`
}

// PostMessage posts a message and returns the channel id and ts Slack gave it.
func (c *Client) PostMessage(ctx context.Context, p Post) (channel, ts string, err error) {
	var body struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := c.Call(ctx, "chat.postMessage", p, &body); err != nil {
		return "", "", err
	}
	if strings.HasPrefix(p.Channel, "#") && body.Channel != "" {
		c.Learn(p.Channel, body.Channel)
	}
	if body.Channel == "" {
		body.Channel = p.Channel
	}
	return body.Channel, body.TS, nil
}

// Update is a chat.update request.
type Update struct {
	Channel     string       `json:"channel"`
	TS          string       `json:"ts"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
	Metadata    *Metadata    `json:"metadata,omitempty"`
}

// UpdateMessage edits a message in place.
func (c *Client) UpdateMessage(ctx context.Context, u Update) error {
	return c.Call(ctx, "chat.update", u, nil)
}

// Reply posts text in a message's thread.
func (c *Client) Reply(ctx context.Context, channel, ts, text string) error {
	_, _, err := c.PostMessage(ctx, Post{Channel: channel, ThreadTS: ts, Text: text})
	return err
}

// OpenView opens a modal for a trigger.
func (c *Client) OpenView(ctx context.Context, triggerID string, view any) error {
	return c.Call(ctx, "views.open", map[string]any{"trigger_id": triggerID, "view": view}, nil)
}

// UserEmail is a user's email address (users:read.email); "" when Slack withholds it.
func (c *Client) UserEmail(ctx context.Context, user string) (string, error) {
	var body struct {
		User struct {
			Profile struct {
				Email string `json:"email"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.Get(ctx, "users.info", url.Values{"user": {user}}, &body); err != nil {
		return "", err
	}
	return body.User.Profile.Email, nil
}

// Upload puts a file into a thread with Slack's two-step external upload.
func (c *Client) Upload(ctx context.Context, channel, ts string, data []byte, filename, comment string) error {
	var up struct {
		URL    string `json:"upload_url"`
		FileID string `json:"file_id"`
	}
	params := url.Values{"filename": {filename}, "length": {strconv.Itoa(len(data))}}
	if err := c.Get(ctx, "files.getUploadURLExternal", params, &up); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, up.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("upload: HTTP %d", resp.StatusCode)
	}
	return c.Call(ctx, "files.completeUploadExternal", map[string]any{
		"files":           []map[string]string{{"id": up.FileID, "title": filename}},
		"channel_id":      channel,
		"thread_ts":       ts,
		"initial_comment": comment,
	}, nil)
}
