# The Slack app

Create the app at <https://api.slack.com/apps> → **Create New App** → **From a manifest**, paste this,
replace the request URL, and install it to the workspace. Copy the **Bot User OAuth Token** and, under
**Basic Information → App Credentials**, the **Signing Secret**.

```yaml
display_information:
  name: fogbell
  description: One thread per incident, from Alertmanager.
features:
  bot_user:
    display_name: fogbell
    always_online: true
oauth_config:
  scopes:
    bot:
      - chat:write
      - channels:read
      - groups:read
      - channels:history
      - groups:history
      - files:write
      - users:read
      - users:read.email
settings:
  interactivity:
    is_enabled: true
    request_url: https://fogbell.example.com/slack/interactions
  org_deploy_enabled: false
  socket_mode_enabled: false
```

Invite the app to every channel it posts in (`/invite @fogbell`).

## What each scope buys

fogbell degrades rather than fails when a scope is missing, and counts it.

| Scope | Without it |
| --- | --- |
| `chat:write` | Nothing works |
| `channels:read`, `groups:read` | fogbell posts by channel name and learns the id from the answer |
| `channels:history`, `groups:history` | A restart forgets open incidents (they get a new message on the next notification), the digest, the noise report and the rule's history are empty, and a postmortem draft has no timeline. `fogbell_history_unavailable_total` counts it |
| `files:write` | No chart in the thread |
| `users:read`, `users:read.email` | Tickets are created unassigned |

## Expose only the interactions path

Slack must reach `POST /slack/interactions`. Nothing else should be public: `/alertmanager` writes to
every channel the app is in. On a Gateway API route, match that exact path and method; fogbell
verifies the signature on every request.
