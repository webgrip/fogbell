package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// ClickUp files tickets in one ClickUp list. The token is read on every use: its file is optional and
// can arrive after start-up.
type ClickUp struct {
	API       string
	TokenFile string
	List      string
	HTTP      *http.Client
}

// Name is how messages name the tracker.
func (c *ClickUp) Name() string { return "ClickUp" }

// Available reports whether a token is present.
func (c *ClickUp) Available() bool { return c != nil && c.token() != "" }

func (c *ClickUp) token() string {
	if c.TokenFile == "" {
		return ""
	}
	b, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *ClickUp) api() string {
	if c.API != "" {
		return c.API
	}
	return "https://api.clickup.com/api/v2"
}

// Member is the id of the user with this address in a workspace the token sees; "" when none.
func (c *ClickUp) Member(ctx context.Context, email string) (string, error) {
	var teams struct {
		Teams []struct {
			Members []struct {
				User struct {
					ID    json.Number `json:"id"`
					Email string      `json:"email"`
				} `json:"user"`
			} `json:"members"`
		} `json:"teams"`
	}
	if err := do(ctx, c.HTTP, http.MethodGet, c.api()+"/team", map[string]string{"Authorization": c.token()}, nil, &teams); err != nil {
		return "", err
	}
	for _, t := range teams.Teams {
		for _, m := range t.Members {
			if m.User.Email != "" && strings.EqualFold(m.User.Email, email) {
				return m.User.ID.String(), nil
			}
		}
	}
	return "", nil
}

// Ticket is a created ticket.
type Ticket struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Create files a ticket, assigned to member when it is not "".
func (c *ClickUp) Create(ctx context.Context, name, markdown, member string) (Ticket, error) {
	body := map[string]any{"name": name, "markdown_content": markdown}
	if member != "" {
		body["assignees"] = []json.Number{json.Number(member)}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Ticket{}, err
	}
	var t Ticket
	err = do(ctx, c.HTTP, http.MethodPost, c.api()+"/list/"+c.List+"/task",
		map[string]string{"Authorization": c.token(), "Content-Type": "application/json"}, raw, &t)
	return t, err
}
