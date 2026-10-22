-- neonmux vim kata harness. Neovim runs it with --clean -u: it loads the
-- missions that neonmux writes into $NEONMUX_KATA_DIR/mission.json, counts
-- the keys typed (VimGolf rules), and tells neonmux through OSC 7777
-- ("kata=…") when the text matches the target.

local dir = vim.env.NEONMUX_KATA_DIR

local function send(msg)
  local s = '\027]7777;kata=' .. msg .. '\007'
  if vim.api.nvim_ui_send then
    vim.api.nvim_ui_send(s)
  else
    vim.api.nvim_chan_send(2, s)
  end
end

if vim.fn.has('nvim-0.10') == 0 then
  local v = vim.fn.execute('version'):match('NVIM v([%w%.%-]+)') or '?'
  send('old:' .. v)
  vim.cmd('qa!')
  return
end

vim.o.mouse = ''
vim.o.swapfile = false
vim.o.report = 9999
vim.o.modeline = false
vim.o.laststatus = 0
vim.o.relativenumber = true
vim.opt.shortmess:append('I')

local ns = vim.api.nvim_create_namespace('neonmux-kata')
local ignored = { ['<FocusGained>'] = true, ['<FocusLost>'] = true, ['<Ignore>'] = true }
local esc = vim.api.nvim_replace_termcodes('<Esc>', true, false, true)

local state = { counting = false }

local function feed(keys)
  return pcall(vim.api.nvim_feedkeys, vim.api.nvim_replace_termcodes(keys, true, false, true), 'xt', false)
end

-- play runs keys on a copy of the mission in a hidden window, without
-- counting them, and returns the lines, the cursor and the mode.
local function play(lines, cursor, keys)
  local buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  local win = vim.api.nvim_open_win(buf, false, { relative = 'editor', row = 0, col = 0, width = 100, height = 30, hide = true })
  local out
  vim.api.nvim_win_call(win, function()
    pcall(vim.api.nvim_win_set_cursor, win, cursor)
    feed(keys)
    out = { lines = vim.api.nvim_buf_get_lines(buf, 0, -1, false), cursor = vim.api.nvim_win_get_cursor(win), mode = vim.api.nvim_get_mode().mode }
    vim.api.nvim_feedkeys(esc, 'xt', false)
  end)
  vim.api.nvim_win_close(win, true)
  pcall(vim.api.nvim_buf_delete, buf, { force = true })
  return out
end

local function at_goal(cur, goal)
  return goal == nil or (cur[1] == goal[1] and cur[2] == goal[2])
end

-- forget removes what the check of the par left behind, so that . @: & ;
-- ctrl+a in insert mode or the command-line and search histories cannot
-- replay the solution for a few keys.
local function forget()
  play({ 'xy' }, { 1, 0 }, 'fyx:s/^//e<CR>:let _=0<CR>i<Esc>')
  vim.fn.histdel(':')
  vim.fn.histdel('/')
  for _, r in ipairs(vim.split('abcdefghijklmnopqrstuvwxyz0123456789"-', '')) do
    vim.fn.setreg(r, '')
  end
  vim.fn.setreg('/', '')
  vim.cmd('nohlsearch')
  vim.api.nvim_echo({}, false, {}) -- clear the command line
end

local function nilable(v)
  if v == vim.NIL then
    return nil
  end
  return v
end

local function check()
  if not state.counting or state.done then
    return
  end
  local mode = vim.api.nvim_get_mode()
  if mode.mode ~= 'n' or mode.blocking or vim.api.nvim_get_current_buf() ~= state.buf then
    return
  end
  if not vim.deep_equal(vim.api.nvim_buf_get_lines(state.buf, 0, -1, false), state.target) then
    return
  end
  if not at_goal(vim.api.nvim_win_get_cursor(0), state.goal) then
    return
  end
  state.done = true
  send('done:' .. vim.base64.encode(vim.json.encode({ keys = state.n, seq = table.concat(state.seq) })))
end

local function load()
  local ok, data = pcall(vim.fn.readfile, dir .. '/mission.json')
  if not ok or #data == 0 then
    return
  end
  local okj, m = pcall(vim.json.decode, table.concat(data, '\n'))
  if not okj or m.nonce == state.nonce then
    return
  end
  state = { counting = false, nonce = m.nonce }
  local goal = nilable(m.goal)
  local res = play(m.lines, m.cursor, m.par)
  if res.mode ~= 'n' or not vim.deep_equal(res.lines, m.target) or not at_goal(res.cursor, goal) then
    send('badpar:' .. m.id)
    return
  end
  forget()
  local buf = vim.api.nvim_create_buf(false, true)
  vim.bo[buf].bufhidden = 'wipe'
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, m.lines)
  vim.api.nvim_win_set_buf(0, buf)
  pcall(vim.api.nvim_win_set_cursor, 0, m.cursor)
  local hl = nilable(m.highlight)
  if hl then
    pcall(vim.api.nvim_buf_set_extmark, buf, ns, hl[1] - 1, hl[2], { end_col = hl[3], hl_group = 'Search' })
  end
  state = {
    counting = true, nonce = m.nonce, id = m.id, buf = buf, target = m.target, goal = goal,
    n = 0, seq = {}, len = 0, done = false,
  }
  send('loaded:' .. m.id)
end

vim.on_key(function(_, typed)
  if not state.counting or state.done or typed == nil or typed == '' then
    return
  end
  local k = vim.fn.keytrans(typed)
  if ignored[k] then
    return
  end
  state.n = state.n + 1
  if state.len < 400 then
    table.insert(state.seq, k)
    state.len = state.len + #k
  end
  send('keys:' .. state.n)
  vim.schedule(check)
end, ns)

vim.api.nvim_create_autocmd('VimLeavePre', {
  callback = function()
    if state.counting and not state.done then
      send('quit:' .. vim.base64.encode(vim.json.encode({ keys = state.n, seq = table.concat(state.seq) })))
    end
  end,
})

local uv = vim.uv or vim.loop
local watcher = uv.new_fs_event()
watcher:start(dir, {}, vim.schedule_wrap(function(_, name)
  if name == 'mission.json' then
    load()
  end
end))

send('ready')
load()
