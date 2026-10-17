// Package where finds where a session lives — its terminal, and for a session
// hosted in tmux, the server socket and pane — so a caller can focus or attach
// it.
//
// It is strictly read-only. Every tmux call is a list command against a
// server that already exists; nothing here attaches, sends keys or starts a
// server. Locating is best-effort by design: an unknown location is reported
// as missing fields, never as an error that could fail a listing.
package where

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Location is where a process's session can be found.
type Location struct {
	// TTY is the controlling terminal, e.g. "/dev/ttys005".
	TTY string `json:"tty,omitempty"`
	// TerminalApp is the macOS app hosting the terminal, from the nearest
	// ancestor that runs out of an app bundle. For a tmux pane it comes from
	// an attached client's ancestry, since the server itself is a daemon.
	// Omitted when unknown.
	TerminalApp string `json:"terminal_app,omitempty"`
	Tmux        *Tmux  `json:"tmux,omitempty"`
}

// Empty reports whether nothing at all was found.
func (l Location) Empty() bool {
	return l.TTY == "" && l.TerminalApp == "" && l.Tmux == nil
}

// Tmux locates a tmux pane.
type Tmux struct {
	// Socket is the absolute server socket path, usable as `tmux -S`.
	Socket string `json:"socket"`
	// SocketName is the `-L` name when the socket is in the default tmux
	// directory, e.g. "muster".
	SocketName string `json:"socket_name,omitempty"`
	Session    string `json:"session"`
	// Window is the window index.
	Window string `json:"window"`
	// Pane is the pane id, e.g. "%38" — stable for the pane's life, unlike
	// its index, which shifts when a sibling pane closes.
	Pane string `json:"pane"`
	// Target is "session:window.pane-index", for `tmux -t`.
	Target string `json:"target"`
	// Attached says a client is attached to the session.
	Attached bool `json:"attached"`
}

// tmuxTimeout bounds each tmux call. A wedged server must not hang discovery.
const tmuxTimeout = 2 * time.Second

// tmuxBudget bounds every tmux call one Resolve makes, together. status calls
// Resolve while a caller waits, and a per-call bound alone would let several
// wedged servers in the socket directory add up to tens of seconds. Past it,
// what was found is reported and the rest is simply not located.
const tmuxBudget = 3 * time.Second

// Resolver locates processes. Every dependency is injectable so the logic is
// testable against fixture output rather than this machine's processes.
type Resolver struct {
	// PS returns `ps -Ao pid=,ppid=,tty=,comm=` output.
	PS func(ctx context.Context) ([]byte, error)
	// Tmux runs a tmux list command against the server at socket.
	Tmux func(ctx context.Context, socket string, args ...string) ([]byte, error)
	// SocketDir returns the default tmux socket directory.
	SocketDir func() (string, error)
	// Sockets lists the socket files in dir.
	Sockets func(dir string) ([]string, error)
}

// Default returns a Resolver backed by this machine's ps and tmux.
func Default() Resolver {
	return Resolver{PS: runPS, Tmux: runTmux, SocketDir: DefaultSocketDir, Sockets: listSockets}
}

// Of locates one process using this machine's ps and tmux.
func Of(pid int) (Location, error) {
	return Default().Of(pid)
}

// Of locates one process. The error is set only when the process table could
// not be read or holds no such pid.
func (r Resolver) Of(pid int) (Location, error) {
	locs, err := r.Resolve([]int{pid})
	if err != nil {
		return Location{}, err
	}
	loc, ok := locs[pid]
	if !ok {
		return Location{}, fmt.Errorf("no process with pid %d", pid)
	}
	return loc, nil
}

// Resolve locates many processes with one ps call and at most one list-panes
// per tmux socket, since discover lists every session at once. A pid absent
// from the process table is absent from the result; the error is set only
// when the process table could not be read.
func (r Resolver) Resolve(pids []int) (map[int]Location, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	out, err := r.PS(ctx)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	table := parsePS(out)

	result := map[int]Location{}
	var inTmux []int
	for _, pid := range pids {
		p, ok := table[pid]
		if !ok {
			continue
		}
		loc := Location{TTY: devTTY(p.tty)}
		if table.hasTmuxAncestor(pid) {
			// The pane, not this ancestry, leads to the terminal: the tmux
			// server is a daemon whose parent is launchd.
			if loc.TTY != "" {
				inTmux = append(inTmux, pid)
			}
		} else {
			loc.TerminalApp = table.terminalApp(pid)
		}
		result[pid] = loc
	}
	if len(inTmux) == 0 {
		return result, nil
	}

	budget, cancelBudget := context.WithTimeout(context.Background(), tmuxBudget)
	defer cancelBudget()
	panes := r.panes(budget)
	clients := map[string][]client{} // by socket, fetched only when needed
	for _, pid := range inTmux {
		loc := result[pid]
		pn, ok := panes[loc.TTY]
		if !ok {
			continue
		}
		t := pn.tmux
		loc.Tmux = &t
		if t.Attached {
			cs, fetched := clients[t.Socket]
			if !fetched {
				cs = r.clients(budget, t.Socket)
				clients[t.Socket] = cs
			}
			for _, c := range cs {
				if c.session != t.Session {
					continue
				}
				if app := table.terminalApp(c.pid); app != "" {
					loc.TerminalApp = app
					break
				}
			}
		}
		result[pid] = loc
	}
	return result, nil
}

type pane struct{ tmux Tmux }

type client struct {
	pid     int
	session string
}

// panes maps each pane tty to its pane, across every live server in the
// default socket directory. A socket whose server has gone answers "no server
// running"; it is skipped, since a stale socket file is ordinary.
func (r Resolver) panes(budget context.Context) map[string]pane {
	found := map[string]pane{}
	dir, err := r.SocketDir()
	if err != nil {
		return found
	}
	names, err := r.Sockets(dir)
	if err != nil {
		return found
	}
	for _, name := range names {
		if budget.Err() != nil {
			break
		}
		socket := filepath.Join(dir, name)
		ctx, cancel := context.WithTimeout(budget, tmuxTimeout)
		// session_name goes last: it is the one field that may hold any
		// character, so it takes the remainder of the line.
		out, err := r.Tmux(ctx, socket, "list-panes", "-a", "-F",
			"#{pane_tty}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{session_attached}\t#{session_name}")
		cancel()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.SplitN(line, "\t", 6)
			if len(f) != 6 || f[0] == "" {
				continue
			}
			attached, _ := strconv.Atoi(f[4])
			found[f[0]] = pane{tmux: Tmux{
				Socket:     socket,
				SocketName: name,
				Session:    f[5],
				Window:     f[1],
				Pane:       f[3],
				Target:     f[5] + ":" + f[1] + "." + f[2],
				Attached:   attached > 0,
			}}
		}
	}
	return found
}

func (r Resolver) clients(budget context.Context, socket string) []client {
	ctx, cancel := context.WithTimeout(budget, tmuxTimeout)
	defer cancel()
	out, err := r.Tmux(ctx, socket, "list-clients", "-F", "#{client_pid}\t#{session_name}")
	if err != nil {
		return nil
	}
	var cs []client
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cs = append(cs, client{pid: pid, session: f[1]})
	}
	return cs
}

type proc struct {
	ppid int
	tty  string
	comm string
}

type procTable map[int]proc

// parsePS reads `pid ppid tty comm` lines. comm is the executable path and
// may contain spaces, so it is the remainder of the line after three fields.
func parsePS(out []byte) procTable {
	table := procTable{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		// Recover comm with its own spacing intact.
		rest := strings.TrimLeft(line, " ")
		for i := 0; i < 3; i++ {
			rest = strings.TrimLeft(rest[strings.IndexAny(rest, " \t"):], " \t")
		}
		table[pid] = proc{ppid: ppid, tty: f[2], comm: strings.TrimRight(rest, " \t\r")}
	}
	return table
}

// ancestors yields pid's ancestors, nearest first, stopping at launchd.
func (t procTable) ancestors(pid int) []proc {
	var chain []proc
	seen := map[int]bool{pid: true}
	cur := t[pid].ppid
	for cur > 1 && !seen[cur] {
		p, ok := t[cur]
		if !ok {
			break
		}
		seen[cur] = true
		chain = append(chain, p)
		cur = p.ppid
	}
	return chain
}

func (t procTable) hasTmuxAncestor(pid int) bool {
	for _, p := range t.ancestors(pid) {
		if filepath.Base(p.comm) == "tmux" {
			return true
		}
	}
	return false
}

// terminalApp names the nearest app bundle among pid and its ancestors.
// Best-effort: an app whose binary does not live in Contents/MacOS, or a
// terminal reached over ssh, goes unnamed.
func (t procTable) terminalApp(pid int) string {
	chain := append([]proc{t[pid]}, t.ancestors(pid)...)
	for _, p := range chain {
		if app := bundleName(p.comm); app != "" {
			return app
		}
	}
	return ""
}

func bundleName(comm string) string {
	i := strings.Index(comm, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return filepath.Base(comm[:i])
}

// devTTY turns ps's "ttys005" into "/dev/ttys005"; "??" means no terminal.
func devTTY(tty string) string {
	if tty == "" || tty == "??" || tty == "-" {
		return ""
	}
	return "/dev/" + tty
}

// DefaultSocketDir is where tmux puts sockets named with -L: $TMUX_TMPDIR, or
// /tmp, then tmux-<uid>. Symlinks are resolved because tmux resolves them
// (/tmp is /private/tmp on macOS), and a -S path should match what tmux says.
func DefaultSocketDir() (string, error) {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	dir := filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir, nil
}

func listSockets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type()&os.ModeSocket != 0 {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func runPS(ctx context.Context) ([]byte, error) {
	// argv-based, never a shell: no quoting or path-with-spaces hazard.
	return exec.CommandContext(ctx, "ps", "-Ao", "pid=,ppid=,tty=,comm=").Output()
}

func runTmux(ctx context.Context, socket string, args ...string) ([]byte, error) {
	// Only list commands reach here. -S names an existing server; a list
	// command never starts one.
	return exec.CommandContext(ctx, "tmux", append([]string{"-S", socket}, args...)...).Output()
}
