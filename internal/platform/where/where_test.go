package where

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const psFixture = `    1     0 ??       /sbin/launchd
  500     1 ??       /Applications/Ghostty.app/Contents/MacOS/ghostty
  501   500 ttys001  /usr/bin/login
  502   501 ttys001  -zsh
  503   502 ttys001  claude
  600     1 ??       /Applications/Utilities/Terminal.app/Contents/MacOS/Terminal
  601   600 ttys002  -zsh
  602   601 ttys002  tmux
87801     1 ??       tmux
30959 87801 ttys005  /Users/me/.asdf/installs/nodejs/24.16.0/bin/node
31055 30959 ttys005  claude
 4000     1 ??       tmux
 4001  4000 ttys007  -zsh
 4002  4001 ttys007  claude
 7000     1 ??       /usr/local/bin/codex app-server
`

type fakeTmux struct {
	panes   map[string]string // socket -> list-panes output; absent = dead
	clients map[string]string
	calls   []string
}

func (f *fakeTmux) run(_ context.Context, socket string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, socket+" "+args[0])
	src := f.panes
	if args[0] == "list-clients" {
		src = f.clients
	}
	out, ok := src[socket]
	if !ok {
		return nil, errors.New("no server running on " + socket)
	}
	return []byte(out), nil
}

func resolver(ft *fakeTmux, sockets ...string) Resolver {
	return Resolver{
		PS:        func(context.Context) ([]byte, error) { return []byte(psFixture), nil },
		Tmux:      ft.run,
		SocketDir: func() (string, error) { return "/private/tmp/tmux-501", nil },
		Sockets:   func(string) ([]string, error) { return sockets, nil },
	}
}

func TestOfNamesTheTerminalAppOfAPlainTerminalSession(t *testing.T) {
	ft := &fakeTmux{}
	loc, err := resolver(ft, "default").Of(503)
	if err != nil {
		t.Fatal(err)
	}
	if loc.TTY != "/dev/ttys001" || loc.TerminalApp != "Ghostty" || loc.Tmux != nil {
		t.Errorf("got %+v, want Ghostty on /dev/ttys001 without tmux", loc)
	}
	// No tmux in the ancestry means no tmux server is asked anything.
	if len(ft.calls) != 0 {
		t.Errorf("tmux was queried for a non-tmux session: %v", ft.calls)
	}
}

func TestOfFindsAPaneUnderAMusterSocket(t *testing.T) {
	ft := &fakeTmux{panes: map[string]string{
		"/private/tmp/tmux-501/muster": "/dev/ttys003\t0\t0\t%34\t0\tmuster-other\n" +
			"/dev/ttys005\t0\t0\t%38\t0\tmuster-bafad08e-66ea\n",
	}}
	loc, err := resolver(ft, "muster").Of(31055)
	if err != nil {
		t.Fatal(err)
	}
	want := Tmux{
		Socket: "/private/tmp/tmux-501/muster", SocketName: "muster",
		Session: "muster-bafad08e-66ea", Window: "0", Pane: "%38",
		Target: "muster-bafad08e-66ea:0.0", Attached: false,
	}
	if loc.Tmux == nil || *loc.Tmux != want {
		t.Fatalf("tmux = %+v, want %+v", loc.Tmux, want)
	}
	if loc.TTY != "/dev/ttys005" {
		t.Errorf("tty = %q", loc.TTY)
	}
	// Unattached: no client to lead to a terminal, and the server's own
	// ancestry (launchd) must not be mistaken for one.
	if loc.TerminalApp != "" {
		t.Errorf("terminal_app = %q, want none for an unattached pane", loc.TerminalApp)
	}
	for _, c := range ft.calls {
		if strings.HasSuffix(c, "list-clients") {
			t.Errorf("list-clients called for an unattached session: %v", ft.calls)
		}
	}
}

func TestOfFindsAnAttachedPaneOnTheDefaultSocketAndItsTerminal(t *testing.T) {
	ft := &fakeTmux{
		panes: map[string]string{
			"/private/tmp/tmux-501/default": "/dev/ttys007\t2\t1\t%9\t1\twork\n",
		},
		clients: map[string]string{
			"/private/tmp/tmux-501/default": "602\twork\n",
		},
	}
	loc, err := resolver(ft, "default").Of(4002)
	if err != nil {
		t.Fatal(err)
	}
	if loc.Tmux == nil || loc.Tmux.Target != "work:2.1" || loc.Tmux.Pane != "%9" || !loc.Tmux.Attached {
		t.Fatalf("tmux = %+v", loc.Tmux)
	}
	if loc.Tmux.SocketName != "default" {
		t.Errorf("socket_name = %q, want default", loc.Tmux.SocketName)
	}
	if loc.TerminalApp != "Terminal" {
		t.Errorf("terminal_app = %q, want Terminal from the attached client", loc.TerminalApp)
	}
}

func TestResolveSkipsADeadSocketSilently(t *testing.T) {
	ft := &fakeTmux{panes: map[string]string{
		"/private/tmp/tmux-501/muster": "/dev/ttys005\t0\t0\t%38\t0\tm\n",
	}}
	// "stale" has a socket file but no server.
	locs, err := resolver(ft, "stale", "muster").Resolve([]int{31055, 4002})
	if err != nil {
		t.Fatalf("a dead socket failed the lookup: %v", err)
	}
	if locs[31055].Tmux == nil || locs[31055].Tmux.SocketName != "muster" {
		t.Errorf("31055 = %+v, want found under muster", locs[31055])
	}
	// In tmux, but on no live server: the tty alone is still reported.
	if got := locs[4002]; got.Tmux != nil || got.TTY != "/dev/ttys007" {
		t.Errorf("4002 = %+v, want tty only", got)
	}
	// One list-panes per socket, however many pids.
	panes := 0
	for _, c := range ft.calls {
		if strings.HasSuffix(c, "list-panes") {
			panes++
		}
	}
	if panes != 2 {
		t.Errorf("list-panes calls = %d, want 2 (one per socket): %v", panes, ft.calls)
	}
}

func TestOfReportsNothingForAProcessWithNoTerminal(t *testing.T) {
	ft := &fakeTmux{}
	loc, err := resolver(ft).Of(7000)
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Empty() {
		t.Errorf("got %+v, want an empty location for tty ??", loc)
	}
}

func TestOfErrorsOnlyForAnUnknownPid(t *testing.T) {
	if _, err := resolver(&fakeTmux{}).Of(99999); err == nil {
		t.Error("want an error for a pid not in the process table")
	}
	locs, err := resolver(&fakeTmux{}).Resolve([]int{99999})
	if err != nil || len(locs) != 0 {
		t.Errorf("Resolve = %v, %v; want empty and no error", locs, err)
	}
}

func TestParsePSKeepsSpacesInTheExecutablePath(t *testing.T) {
	table := parsePS([]byte("  336   324 ??       /Applications/Google Chrome.app/Contents/MacOS/Google Chrome\n"))
	if got := table[336].comm; got != "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" {
		t.Errorf("comm = %q", got)
	}
	if got := bundleName(table[336].comm); got != "Google Chrome" {
		t.Errorf("bundle = %q", got)
	}
}
