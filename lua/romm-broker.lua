-- romm-broker.lua: the emulator half of the broker's command channel.
--
-- Flycast has no IPC socket, and its default keyboard mapping binds nothing to
-- save state, load state or slot selection -- so a broker driving it from
-- outside would have to synthesise keystrokes into a compositor that may or may
-- not be routing them to an Xwayland surface. It does, however, embed Lua with
-- the full standard library and expose saveState/loadState/exit directly.
--
-- So the broker writes a command file and this script executes it. Slots are
-- addressed by index, there is no selector to cycle, and the result is reported
-- back rather than inferred.
--
-- Protocol (see docs/CONTRACT.md D2):
--
--   ready    written here at load, contains the protocol version
--   command  written by the broker, atomically: "<seq> <verb> <arg>"
--   ack      written here after executing: "<seq> ok" or "<seq> err <reason>"
--   state    written here on start/terminate: "running" or "stopped"

local VERSION = "1"

local DIR = os.getenv("ROMM_BROKER_CHANNEL") or ((os.getenv("HOME") or "/config") .. "/.config/flycast/romm-broker")

local function path(name)
	return DIR .. "/" .. name
end

-- Written through a temporary file and renamed, so the broker can never read a
-- half-written line. os.rename is atomic within a directory.
local function write(name, text)
	local tmp = path(name .. ".tmp")
	local f = io.open(tmp, "w")
	if not f then
		return false
	end
	f:write(text)
	f:close()
	return os.rename(tmp, path(name)) ~= nil
end

local function slurp(name)
	local f = io.open(path(name), "r")
	if not f then
		return nil
	end
	local body = f:read("*a")
	f:close()
	return body
end

local function ack(seq, status, reason)
	local line = seq .. " " .. status
	if reason then
		-- The broker splits the ack on whitespace, so a multi-line reason
		-- would confuse it.
		line = line .. " " .. tostring(reason):gsub("%s+", " ")
	end
	write("ack", line .. "\n")
end

local function execute(verb, arg)
	if verb == "save" then
		flycast.emulator.saveState(arg)
	elseif verb == "load" then
		flycast.emulator.loadState(arg)
	elseif verb == "slot" then
		flycast.config.general.SavestateSlot = arg
	elseif verb == "exit" then
		flycast.emulator.exit()
	else
		return false, "unknown verb " .. tostring(verb)
	end
	return true
end

-- Polled from the `overlay` callback, which Flycast calls once per rendered
-- frame from gui_draw_osd on the render thread. That is the thread
-- saveState is shaped for: it calls gui_open_settings, which stops the
-- emulation thread, and driving that from a vblank callback would have the
-- emulation thread waiting on itself.
local function poll()
	local body = slurp("command")
	if not body then
		return
	end

	-- The broker sends one command at a time and waits for its ack, so
	-- removing the file here cannot drop a queued command.
	os.remove(path("command"))

	local seq, verb, arg = body:match("^%s*(%d+)%s+(%a+)%s*(-?%d*)")
	if not seq then
		return
	end
	arg = tonumber(arg) or 0

	local ok, err = pcall(execute, verb, arg)
	if not ok then
		ack(seq, "err", err)
		return
	end
	-- pcall succeeded, so `err` here is execute's own second return value.
	if err == false then
		ack(seq, "err", "refused")
		return
	end
	ack(seq, "ok")
end

flycast_callbacks = flycast_callbacks or {}

flycast_callbacks.overlay = poll

flycast_callbacks.start = function()
	write("state", "running\n")
end

flycast_callbacks.terminate = function()
	write("state", "stopped\n")
end

-- Announce last: the broker treats `ready` as proof the whole script loaded.
write("state", "stopped\n")
write("ready", VERSION .. "\n")
