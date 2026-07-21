-- hammerspoon-mcp.lua — MCPlexer bridge.
-- Drop in ~/.hammerspoon/ and `require("hammerspoon-mcp")` from init.lua.
-- Reload Hammerspoon after edits.

local M = {}

-- Read shared password from ~/.hammerspoon/.mcp-password (created by mcplexer).
local function read_password()
    local f = io.open(os.getenv("HOME") .. "/.hammerspoon/.mcp-password", "r")
    if not f then return nil end
    local pw = f:read("*l")
    f:close()
    return pw and pw:gsub("%s+$", "") or nil
end

local PORT     = tonumber(os.getenv("MCPX_HS_PORT")) or 27123
local PASSWORD = read_password()
local LOG      = hs.logger.new("mcpx-hs", "info")

if not PASSWORD then
    LOG.w("no ~/.hammerspoon/.mcp-password — bridge disabled")
    return M
end

local server = hs.httpserver.new(false, false)  -- loopback-only, no SSL
server:setInterface("127.0.0.1")
server:setPort(PORT)
server:setPassword(nil)  -- we do our own bearer check

server:setCallback(function(method, path, headers, body)
    if path ~= "/exec" or method ~= "POST" then
        return hs.json.encode({ok=false, err="not found"}), 404, {}
    end
    local auth = headers["Authorization"] or headers["authorization"] or ""
    if auth ~= "Bearer " .. PASSWORD then
        return hs.json.encode({ok=false, err="unauthorized"}), 401, {}
    end
    local req = hs.json.decode(body or "{}") or {}
    local lua_src = req.lua or ""
    LOG.i(string.format("exec len=%d preview=%q", #lua_src, lua_src:sub(1,80)))

    local fn, ferr = load("return (function() " .. lua_src .. " end)()", "mcpx-exec", "t")
    if not fn then
        return hs.json.encode({ok=false, err="load: "..tostring(ferr)}), 200, {}
    end
    local ok, result = pcall(fn)
    if not ok then
        return hs.json.encode({ok=false, err=tostring(result)}), 200, {}
    end
    return hs.json.encode({ok=true, result=result}), 200, {["Content-Type"]="application/json"}
end)

server:start()
LOG.i("mcpx bridge listening on 127.0.0.1:" .. PORT)

-- Clean reload: stop the server before Hammerspoon swaps init.lua.
M.stop = function() server:stop() end
hs.shutdownCallback = M.stop
return M
