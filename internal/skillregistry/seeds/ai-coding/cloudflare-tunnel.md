---
name: cloudflare-tunnel
description: Create and configure a Cloudflare Tunnel with DNS routing via the Cloudflare API using browser session authentication
---

Set up a Cloudflare Tunnel: $ARGUMENTS

## Overview

Creates a Cloudflare Tunnel that connects a server to the internet via Cloudflare's edge network (outbound-only, no port forwarding needed). Uses the Cloudflare dashboard's authenticated session to make API calls directly — avoids unreliable React form automation.

## Prerequisites

- Access to the Cloudflare dashboard (user will log in)
- The target service must be reachable from the `cloudflared` container (e.g., Docker network)
- SOPS + AGE key for secret storage (if using encrypted secrets)

## Inputs

Parse from `$ARGUMENTS` or ask the user:

| Parameter | Example | Description |
|-----------|---------|-------------|
| `account_name` | `Example Account` | Cloudflare account name |
| `domain` | `example.com` | Cloudflare-managed domain |
| `subdomain` | `staging` | Subdomain for the tunnel |
| `origin_service` | `https://traefik:443` | Origin service URL |
| `no_tls_verify` | `true` | Skip TLS verification on origin (for self-signed/internal certs) |
| `tunnel_name` | `staging-example` | Human-readable tunnel name |

## Step 1: Authenticate via Browser

Open the Cloudflare dashboard and wait for the user to log in.

```bash
cmux browser open "https://dash.cloudflare.com/"
cmux rename-tab --surface surface:N "browse: cloudflare-tunnel"
cmux browser surface:N wait --load-state complete --timeout 20
```

Check if logged in by reading page text. If login page shown, tell user:
> **Please log in to Cloudflare in the browser pane.** Let me know when you're in.

After login, find the target account link and navigate to it.

## Step 2: Get Account ID

```bash
cmux browser surface:N get text --selector 'body'
```

Extract the account URL from the page. The account ID is the hex string in the URL path:
`https://dash.cloudflare.com/<ACCOUNT_ID>/home/overview`

## Step 3: Create the Tunnel

**IMPORTANT**: POST/PUT requests via `fetch` get WAF-blocked on certain Cloudflare dashboard pages. The pattern that works:
- **Tunnel creation**: Use the dashboard UI (POST via API gets WAF-blocked)
- **Tunnel config PUT**: Works from the account root page (`/dash.cloudflare.com/<ACCOUNT_ID>`), but gets WAF-blocked from `/one/networks/connectors` pages
- **DNS API calls**: Work from the account root page
- **Always use relative URLs** (`/api/v4/...`) with `credentials: "include"` — absolute URLs to `api.cloudflare.com` get CORS-blocked

### Creating the tunnel via UI:

1. Navigate to `https://dash.cloudflare.com/<ACCOUNT_ID>/one/networks/connectors`
2. Click "Create a tunnel" → Select "Cloudflared" → Enter name → Save
   - **Note**: React forms resist programmatic fills. For the tunnel name input (`#name-your-tunnel-input`), use the React native setter pattern:
   ```bash
   cmux browser surface:N eval --script '
     var input = document.querySelector("#name-your-tunnel-input");
     var setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
     setter.call(input, "<TUNNEL_NAME>");
     input.dispatchEvent(new Event("input", {bubbles:true}));
     input.dispatchEvent(new Event("change", {bubbles:true}));
   '
   ```
3. On the "Install and run connectors" page, extract the token using clipboard interception:

```bash
cmux browser surface:N eval --script '
  var captured = null;
  var orig = navigator.clipboard.writeText;
  navigator.clipboard.writeText = function(text) {
    captured = text;
    return orig.call(this, text);
  };
  var btns = document.querySelectorAll("[data-testid=copy-code-block-button]");
  btns[btns.length - 1].click();
  window._tunnelToken = captured;
  "done"
'
```

The token after `--token` is the `TUNNEL_TOKEN`.

## Step 4: Decode the Token

The tunnel token is base64-encoded JSON:

```bash
echo '<TOKEN>' | base64 -d | python3 -m json.tool
```

Fields:
- `a` = Account ID
- `t` = Tunnel ID (UUID)
- `s` = Tunnel secret

## Step 5: Configure Tunnel Ingress

**CRITICAL**: Navigate to the account root page first (`https://dash.cloudflare.com/<ACCOUNT_ID>`). The PUT endpoint gets WAF-blocked when called from `/one/networks/connectors` pages.

```bash
cmux browser surface:N navigate "https://dash.cloudflare.com/<ACCOUNT_ID>"
cmux browser surface:N wait --load-state complete --timeout 15
```

Then configure:

```bash
cmux browser surface:N eval --script '
  var config = {
    config: {
      ingress: [
        {
          hostname: "<SUBDOMAIN>.<DOMAIN>",
          service: "<ORIGIN_SERVICE>",
          originRequest: { noTLSVerify: <NO_TLS_VERIFY> }
        },
        { service: "http_status:404" }
      ]
    }
  };
  fetch("/api/v4/accounts/<ACCOUNT_ID>/cfd_tunnel/<TUNNEL_ID>/configurations", {
    method: "PUT",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config)
  })
  .then(function(r) { return r.json(); })
  .then(function(d) { window._cfConfigResult = JSON.stringify(d); });
  "configuring..."
'
```

Wait 2 seconds, then read `window._cfConfigResult`. Verify `success: true`.

## Step 6: Update DNS

### Get Zone ID

```bash
cmux browser surface:N eval --script '
  fetch("/api/v4/zones?name=<DOMAIN>", { credentials: "include" })
  .then(function(r) { return r.json(); })
  .then(function(d) { window._zoneResult = JSON.stringify(d); });
  "fetching..."
'
```

### Delete existing record (if any)

```bash
# First get existing records
fetch("/api/v4/zones/<ZONE_ID>/dns_records?name=<SUBDOMAIN>.<DOMAIN>", ...)

# Delete if found
fetch("/api/v4/zones/<ZONE_ID>/dns_records/<RECORD_ID>", { method: "DELETE", ... })
```

### Create CNAME

```bash
cmux browser surface:N eval --script '
  fetch("/api/v4/zones/<ZONE_ID>/dns_records", {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      type: "CNAME",
      name: "<SUBDOMAIN>",
      content: "<TUNNEL_ID>.cfargotunnel.com",
      proxied: true,
      comment: "Cloudflare Tunnel for <TUNNEL_NAME>"
    })
  })
  .then(function(r) { return r.json(); })
  .then(function(d) { window._dnsResult = JSON.stringify(d); });
  "creating CNAME..."
'
```

## Step 7: Output

Print the tunnel token and configuration summary for the user to store in their secrets:

```
=== Cloudflare Tunnel Created ===
Tunnel name:  <TUNNEL_NAME>
Tunnel ID:    <TUNNEL_ID>
Hostname:     <SUBDOMAIN>.<DOMAIN>
Origin:       <ORIGIN_SERVICE>
DNS:          CNAME → <TUNNEL_ID>.cfargotunnel.com (proxied)

TUNNEL_TOKEN: <TOKEN>

Add to your Docker Compose:
  tunnel:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel run
    environment:
      - TUNNEL_TOKEN=${CLOUDFLARE_TUNNEL_TOKEN}
    networks:
      - internal
    depends_on:
      - traefik
```

## Important Notes

### Browser API Calls
- **Always use relative URLs** (`/api/v4/...`) — absolute URLs to `api.cloudflare.com` get CORS-blocked
- **Always use `credentials: "include"`** to send the session cookies
- **Navigate to account root** before making PUT/POST API calls — `/one/networks/connectors` pages trigger WAF blocks
- **Use ES5-compatible JavaScript** in `eval --script` — WKWebView may error on arrow functions, `Array.from()`, etc. Use `var`, `for` loops, and `.then(function(x) { ... })`
- **Wait 2-3 seconds** after async API calls before reading `window._result` variables

### React Form Filling
- Cloudflare dashboard uses React controlled inputs — `input.value = x` alone won't work
- Use the native setter pattern: `Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(input, value)` followed by `input.dispatchEvent(new Event("input", {bubbles:true}))`
- For React select dropdowns: type into the input to trigger the dropdown, then click the `[id*=react-select][id*=option]` element
- **Skip the Route UI form entirely** — the service URL field resists all input methods. Use the PUT API (Step 5) instead

### Docker Networking
- `cloudflared` must be on the same Docker network as the origin service
- Use the Docker service name (e.g., `traefik`, `nginx`) not `localhost`
- `localhost` inside the cloudflared container points to itself, not the host

### SOPS Secrets
- When adding the tunnel token to a SOPS YAML file, **always wrap the value in double quotes**
- Long base64 strings can get line-wrapped by YAML, introducing spaces that break the token
- Correct: `CLOUDFLARE_TUNNEL_TOKEN: "eyJhIjoiZW..."`

### Cloudflare WAF Behaviour
- `GET` requests via browser session work reliably on all endpoints
- `PUT` to tunnel config endpoint works via `fetch` with relative URLs
- `POST` to most endpoints (Access apps, tunnel creation) gets WAF-blocked — use the dashboard UI instead
- `XMLHttpRequest` always gets blocked — never use it, always use `fetch`

### Cleanup
```bash
cmux close-surface --surface surface:N
```
