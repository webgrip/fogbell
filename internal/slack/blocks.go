package slack

import "encoding/json"

// Text is a Block Kit text object.
type Text struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PlainText is a plain_text object.
func PlainText(s string) *Text { return &Text{Type: "plain_text", Text: s} }

// Mrkdwn is a mrkdwn text object.
func Mrkdwn(s string) *Text { return &Text{Type: "mrkdwn", Text: s} }

// Option is an option of a select or an overflow menu.
type Option struct {
	Text  Text   `json:"text"`
	Value string `json:"value"`
	URL   string `json:"url,omitempty"`
}

// OptionGroup is a labelled group of options of a select.
type OptionGroup struct {
	Label   Text     `json:"label"`
	Options []Option `json:"options"`
}

// Element is an element of an actions or context block: a button, a select, an overflow menu, or a
// mrkdwn text in a context block.
type Element struct {
	Type         string        `json:"type"`
	ActionID     string        `json:"action_id,omitempty"`
	Label        *Text         `json:"-"`
	Content      string        `json:"-"`
	URL          string        `json:"url,omitempty"`
	Style        string        `json:"style,omitempty"`
	Value        string        `json:"value,omitempty"`
	Placeholder  *Text         `json:"placeholder,omitempty"`
	Options      []Option      `json:"options,omitempty"`
	OptionGroups []OptionGroup `json:"option_groups,omitempty"`
}

type elementJSON struct {
	Type         string          `json:"type"`
	ActionID     string          `json:"action_id,omitempty"`
	Text         json.RawMessage `json:"text,omitempty"`
	URL          string          `json:"url,omitempty"`
	Style        string          `json:"style,omitempty"`
	Value        string          `json:"value,omitempty"`
	Placeholder  *Text           `json:"placeholder,omitempty"`
	Options      []Option        `json:"options,omitempty"`
	OptionGroups []OptionGroup   `json:"option_groups,omitempty"`
}

// MarshalJSON writes text as a string for a text element and as a text object otherwise.
func (e Element) MarshalJSON() ([]byte, error) {
	j := elementJSON{Type: e.Type, ActionID: e.ActionID, URL: e.URL, Style: e.Style, Value: e.Value,
		Placeholder: e.Placeholder, Options: e.Options, OptionGroups: e.OptionGroups}
	var err error
	switch {
	case e.Type == "mrkdwn" || e.Type == "plain_text":
		j.Text, err = json.Marshal(e.Content)
	case e.Label != nil:
		j.Text, err = json.Marshal(e.Label)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads both shapes of text.
func (e *Element) UnmarshalJSON(b []byte) error {
	var j elementJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*e = Element{Type: j.Type, ActionID: j.ActionID, URL: j.URL, Style: j.Style, Value: j.Value,
		Placeholder: j.Placeholder, Options: j.Options, OptionGroups: j.OptionGroups}
	if len(j.Text) == 0 {
		return nil
	}
	if j.Text[0] == '"' {
		return json.Unmarshal(j.Text, &e.Content)
	}
	e.Label = &Text{}
	return json.Unmarshal(j.Text, e.Label)
}

// Button is a button that acts or, with a URL, a link.
func Button(actionID, label, url string) Element {
	return Element{Type: "button", ActionID: actionID, Label: PlainText(label), URL: url}
}

// TextElement is a mrkdwn element of a context block.
func TextElement(s string) Element { return Element{Type: "mrkdwn", Content: s} }

// Block is a Block Kit layout block.
type Block struct {
	Type     string    `json:"type"`
	BlockID  string    `json:"block_id,omitempty"`
	Text     *Text     `json:"text,omitempty"`
	Elements []Element `json:"elements,omitempty"`
}

// Attachment is a legacy attachment that carries a colour bar and blocks.
type Attachment struct {
	Color    string  `json:"color,omitempty"`
	Fallback string  `json:"fallback,omitempty"`
	Blocks   []Block `json:"blocks,omitempty"`
}

// Metadata is message metadata: fogbell's state on its own messages.
type Metadata struct {
	EventType    string          `json:"event_type"`
	EventPayload json.RawMessage `json:"event_payload"`
}

// Message is a channel message as conversations.history returns it.
type Message struct {
	TS          string       `json:"ts"`
	Text        string       `json:"text"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Metadata    *Metadata    `json:"metadata,omitempty"`
}

// Payload decodes the message's metadata payload into v when its event type is eventType.
func (m *Message) Payload(eventType string, v any) bool {
	if m == nil || m.Metadata == nil || m.Metadata.EventType != eventType || len(m.Metadata.EventPayload) == 0 {
		return false
	}
	return json.Unmarshal(m.Metadata.EventPayload, v) == nil
}
