package kata

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// Test helpers that run the real Neovim: the reference for every recipe
// and every motion. Without nvim (or with one older than 0.10), the tests
// that need it are skipped.

var nvimVersion = regexp.MustCompile(`NVIM v(\d+)\.(\d+)`)

func needNvim(t testing.TB) {
	t.Helper()
	out, err := exec.Command("nvim", "--version").Output()
	if err != nil {
		t.Skip("nvim not found")
	}
	m := nvimVersion.FindSubmatch(out)
	if m == nil {
		t.Skip("unknown nvim version")
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	if major == 0 && minor < 10 {
		t.Skip("nvim older than 0.10")
	}
}

// nvimLua runs in a headless nvim: it reads $KATA_IN, sets up a hidden
// window per job and writes the answers to $KATA_OUT.
const nvimLua = `
local function main()
  local input = vim.json.decode(table.concat(vim.fn.readfile(vim.env.KATA_IN), "\n"))
  local buf = vim.api.nvim_create_buf(false, true)
  local win = vim.api.nvim_open_win(buf, true, {relative='editor', row=0, col=0, width=120, height=40, hide=true})
  local out = {}
  local function load(lines)
    vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  end
  if input.jobs then
    local esc = vim.api.nvim_replace_termcodes('<Esc><Esc>', true, false, true)
    for i, job in ipairs(input.jobs) do
      load(job.lines)
      vim.api.nvim_win_set_cursor(win, {job.cursor[1] + 1, job.cursor[2]})
      local ok, err = pcall(vim.api.nvim_feedkeys, vim.api.nvim_replace_termcodes(job.keys, true, false, true), 'xt', false)
      local c = vim.api.nvim_win_get_cursor(win)
      out[i] = {lines = vim.api.nvim_buf_get_lines(buf, 0, -1, false), cursor = {c[1] - 1, c[2]}, mode = vim.api.nvim_get_mode().mode, err = ok and '' or tostring(err)}
      vim.api.nvim_feedkeys(esc, 'xt', false)
    end
  elseif input.keys then
    load(input.lines)
    vim.api.nvim_win_set_cursor(win, {input.cursor[1] + 1, input.cursor[2]})
    vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes(input.keys, true, false, true), 'xt', false)
    local c = vim.api.nvim_win_get_cursor(win)
    out = {lines = vim.api.nvim_buf_get_lines(buf, 0, -1, false), cursor = {c[1] - 1, c[2]}, mode = vim.api.nvim_get_mode().mode}
  else
    load(input.lines)
    for _, m in ipairs(input.motions) do
      local keys = vim.api.nvim_replace_termcodes(m, true, false, true)
      local res = {}
      for i, s in ipairs(input.states) do
        vim.api.nvim_win_set_cursor(win, {s[1] + 1, s[2]})
        local want = s[3]
        if want < 0 then want = vim.v.maxcol end
        vim.fn.winrestview({lnum = s[1] + 1, col = s[2], curswant = want})
        pcall(vim.api.nvim_feedkeys, keys, 'xt', false)
        vim.cmd('silent! nohlsearch')
        local c = vim.api.nvim_win_get_cursor(win)
        local w = vim.fn.winsaveview().curswant
        if w >= vim.v.maxcol then w = -1 end
        res[i] = {c[1] - 1, c[2], w}
        local esc = vim.api.nvim_replace_termcodes('<Esc>', true, false, true)
        vim.api.nvim_feedkeys(esc, 'xt', false)
      end
      out[m] = res
    end
  end
  vim.fn.writefile({vim.json.encode(out)}, vim.env.KATA_OUT)
end
local ok, err = pcall(main)
if not ok then vim.fn.writefile({vim.json.encode({error = tostring(err)})}, vim.env.KATA_OUT) end
vim.cmd('qa!')
`

// runNvim runs nvimLua on input and decodes its answer into out.
func runNvim(t testing.TB, input any, out any) {
	t.Helper()
	dir := t.TempDir()
	in, script, res := filepath.Join(dir, "in.json"), filepath.Join(dir, "kata.lua"), filepath.Join(dir, "out.json")
	data, _ := json.Marshal(input)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(nvimLua), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvim", "--headless", "--clean", "-n", "-i", "NONE", "-u", script)
	cmd.Env = append(os.Environ(), "KATA_IN="+in, "KATA_OUT="+res)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim: %v\n%s", err, b)
	}
	data, err := os.ReadFile(res)
	if err != nil {
		t.Fatal(err)
	}
	var e struct{ Error string }
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		t.Fatalf("nvim script: %s", e.Error)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

// playInNvim types keys on lines from cur and returns the text, the
// cursor and the mode at the end.
func playInNvim(t testing.TB, lines []string, cur Pos, keys string) ([]string, Pos, string) {
	t.Helper()
	var out struct {
		Lines  []string
		Cursor [2]int
		Mode   string
	}
	runNvim(t, map[string]any{"lines": lines, "cursor": [2]int{cur.Line, cur.Col}, "keys": keys}, &out)
	if out.Lines == nil {
		out.Lines = []string{}
	}
	return out.Lines, Pos{out.Cursor[0], out.Cursor[1]}, out.Mode
}

// job is one recipe to play; result what nvim made of it.
type job struct {
	Lines  []string `json:"lines"`
	Cursor [2]int   `json:"cursor"`
	Keys   string   `json:"keys"`
}

type result struct {
	Lines  []string
	Cursor [2]int
	Mode   string
	Err    string
}

// playAll plays many recipes in one nvim run.
func playAll(t testing.TB, jobs []job) []result {
	t.Helper()
	if len(jobs) == 0 {
		return nil
	}
	var out []result
	runNvim(t, map[string]any{"jobs": jobs}, &out)
	for i := range out {
		if out[i].Lines == nil {
			out[i].Lines = []string{}
		}
	}
	return out
}

func jobFor(m Mission, r Recipe) job {
	return job{Lines: m.Start, Cursor: [2]int{m.Cursor.Line, m.Cursor.Col}, Keys: r.Keys()}
}

// nvimMotions plays every motion from every state and returns where nvim
// lands, in the order of states.
func nvimMotions(t testing.TB, lines []string, states []State, motions []string) map[string][]State {
	t.Helper()
	ss := make([][3]int, len(states))
	for i, s := range states {
		ss[i] = [3]int{s.Line, s.Col, s.Want}
	}
	var raw map[string][][3]int
	runNvim(t, map[string]any{"lines": lines, "states": ss, "motions": motions}, &raw)
	out := map[string][]State{}
	for m, rs := range raw {
		for _, r := range rs {
			out[m] = append(out[m], State{Pos{r[0], r[1]}, r[2]})
		}
	}
	return out
}

func TestNvimHelpers(t *testing.T) {
	needNvim(t)
	lines, _, mode := playInNvim(t, []string{"a (b c) d"}, Pos{0, 0}, "f(ci(x<Esc>")
	if len(lines) != 1 || lines[0] != "a (x) d" || mode != "n" {
		t.Errorf("ci( gave %q in mode %q", lines, mode)
	}
	paras := []string{"one", "", "two", "", "three"}
	lines, _, _ = playInNvim(t, paras, Pos{0, 0}, "dap}p")
	if want := []string{"two", "", "one", "", "three"}; len(lines) != len(want) || lines[0] != "two" || lines[2] != "one" {
		t.Errorf("dap}p gave %q", lines)
	}
}
