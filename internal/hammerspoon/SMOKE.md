# Hammerspoon bridge — manual smoke checklist

These steps require an actual Mac with Hammerspoon installed. They cover
the install + probe + tool surface end-to-end against the real
`hs.httpserver`, which the Docker integration harness can't exercise.

Pre-req: an mcplexer daemon running locally and reachable from a browser at
the usual port (default `http://127.0.0.1:13333`).

## 1. Install Hammerspoon

1. Download Hammerspoon from <https://www.hammerspoon.org/> and drag it
   to `/Applications`.
2. Launch Hammerspoon.app. The first launch will prompt for Accessibility
   permission — grant it now (System Settings → Privacy & Security →
   Accessibility → toggle Hammerspoon on).
3. Confirm the Hammerspoon menu-bar icon is visible.

## 2. Enable the integration in the mcplexer dashboard

1. Open the mcplexer dashboard.
2. Navigate to **Servers** → **Hammerspoon**.
3. Toggle the server on. The card flips to "Enabled — needs setup."
4. Restart the daemon (the manager is built once at boot — `kill -HUP` or
   `make restart`; whichever your local workflow uses).

## 3. Install the bridge

1. In the Hammerspoon card, click **Install bridge**.
2. Verify the response shows two `files_written` entries: one for
   `~/.hammerspoon/hammerspoon-mcp.lua` and one for
   `~/.hammerspoon/.mcp-password`.
3. Confirm on disk:
   ```sh
   stat -f "%Op %N" ~/.hammerspoon/hammerspoon-mcp.lua
   # → expect: 100644 .../hammerspoon-mcp.lua
   stat -f "%Op %N" ~/.hammerspoon/.mcp-password
   # → expect: 100600 .../.mcp-password
   ```
4. Confirm `~/.hammerspoon/init.lua` contains the line:
   ```lua
   require("hammerspoon-mcp")
   ```
   The installer is idempotent — re-running it should not duplicate the
   line. If the file already existed with other content, a backup
   `init.lua.mcplexer-bak.<timestamp>` should sit alongside it.
5. In the Hammerspoon console (menu-bar icon → Console), run
   `hs.reload()` if the installer didn't trigger it automatically. The
   console should print `mcpx bridge listening on 127.0.0.1:27123`.

## 4. Probe the bridge

1. Click **Probe** in the dashboard.
2. Expect five green checks:
   - `app_running` ok
   - `bridge_reachable` ok
   - `auth_ok` ok
   - `accessibility` ok (or red if you skipped step 1 — the remediation
     card tells you exactly where to fix it; do that and re-probe)
   - `smoke` ok with `N windows` detail

## 5. Tool surface

Open the mcplexer code-mode tool (or `mcpx__execute_code` from a Claude
session) and try each:

### 5.1 `list_windows`

```js
const r = hammerspoon.list_windows();
print(JSON.stringify(r));
```

Expect a JSON array of window objects, one per visible window. Verify
your current frontmost window is in the list with `frontmost: true`.

### 5.2 `screenshot`

```js
const r = hammerspoon.screenshot({});  // default: full screen, base64
print(r.width, r.height, r.base64_png?.length || 0);
```

Expect width/height of your main display and a base64 payload (`<2 MB`)
or a `path` referencing `~/.mcplexer/hammerspoon-screenshots/<ts>.png`
when the image was over the inline cap.

### 5.3 `send_keys`

1. Open TextEdit and create a new blank document. Click into the doc so
   it has focus.
2. Run:
   ```js
   hammerspoon.send_keys({ text: "hello from mcplexer" });
   ```
3. Expect "hello from mcplexer" to be typed into TextEdit.

### 5.4 `notify`

```js
hammerspoon.notify({ title: "mcplexer test", body: "from the dashboard" });
```

Expect a macOS notification banner. (If banners are off in your Focus
settings, check Notification Center.)

## 6. `exec_lua` gate

### 6.1 Gate off (default)

1. Confirm `HAMMERSPOON_ALLOW_EXEC_LUA` is unset (or `false`) in the
   `hammerspoon-bridge` auth scope.
2. From the code-mode tool, attempt:
   ```js
   hammerspoon.exec_lua({ lua: "return hs.host.localizedName()" });
   ```
3. Expect an `isError` envelope mentioning `HAMMERSPOON_ALLOW_EXEC_LUA`.
4. Also verify `exec_lua` is absent from the tool list:
   ```js
   const tools = mcpx.search_tools({ q: "hammerspoon" });
   print(tools.map(t => t.name).filter(n => n.includes("exec_lua")));
   // → expect: []
   ```

### 6.2 Gate on

1. In the dashboard, flip **Allow exec_lua** to on.
2. Restart the daemon (env reads are cold-boot only).
3. Re-run the call from 6.1 step 2. Expect the result to be your
   machine's name (e.g. `"my-laptop"`).
4. Re-run the tool-list check. Expect `exec_lua` to appear.

## 7. Password rotation

1. Click **Regenerate password** in the dashboard.
2. Verify `~/.hammerspoon/.mcp-password` content changed:
   ```sh
   md5 ~/.hammerspoon/.mcp-password
   # compare against the value before the rotation
   ```
3. The bridge reloads the password on each request via the embedded
   `read_password()` helper — no Hammerspoon reload needed.
4. Re-run **Probe**. Expect five green checks again (i.e. the new
   password is the one being sent by mcplexer and accepted by the
   bridge).

## 8. Tear-down

To remove the bridge:

```sh
rm ~/.hammerspoon/hammerspoon-mcp.lua
rm ~/.hammerspoon/.mcp-password
# Manually remove the require("hammerspoon-mcp") line from
# ~/.hammerspoon/init.lua (or restore the backup the installer wrote).
```

Then disable the integration in the dashboard and restart the daemon.
