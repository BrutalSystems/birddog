package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrutalSystems/birddog/internal/hooks"
)

// hookStateDir is where hook handlers record what they saw, and where the
// Claude Code observer reads it.
func hookStateDir() string {
	if home := os.Getenv("BIRDDOG_HOME"); home != "" {
		return filepath.Join(home, "claude-hooks")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".birddog", "claude-hooks")
	}
	return filepath.Join(home, ".birddog", "claude-hooks")
}

// runHook is the hook handler. Claude Code runs it on its own critical path,
// so the contract here is narrow and absolute.
//
// It always exits 0. Exit 2 blocks the action — a PreToolUse hook exiting 2
// stops the tool call — and any other nonzero exit shows the operator a hook
// error notice. birddog observes: it must never block Claude's work, and must
// not clutter the transcript when its own state directory is unwritable.
//
// It writes nothing to stdout. On several events Claude Code reads a hook's
// stdout and adds it to the model's context; birddog has no business putting
// anything there.
func runHook(args []string, stderr io.Writer) error {
	// Nothing this function can discover is worth a nonzero status, so every
	// path below reports to stderr at most and returns nil.
	report := func(format string, a ...any) {
		fmt.Fprintf(stderr, "birddog hook: "+format+"\n", a...)
	}

	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		report("could not read the event: %v", err)
		return nil
	}

	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		report("could not parse the event: %v", err)
		return nil
	}

	store := &hooks.Store{Dir: hookStateDir()}
	if err := store.Apply(event); err != nil {
		report("%v", err)
	}
	return nil
}

func runHooks(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("hooks needs a subcommand: install, status or uninstall")
	}
	switch args[0] {
	case "install":
		return runHooksInstall(args[1:], stdout)
	case "uninstall":
		return runHooksUninstall(args[1:], stdout)
	case "status":
		return runHooksStatus(args[1:], stdout)
	default:
		return fmt.Errorf("unknown hooks subcommand %q", args[0])
	}
}

// Where a settings path came from, as reported in settings_source. An operator
// running several Claude Code profiles needs to see which rule picked the
// file, because the wrong profile's settings look exactly like the right one's.
const (
	sourceFlag          = "flag"
	sourceConfigDirFlag = "config_dir_flag"
	sourceConfigDirEnv  = "CLAUDE_CONFIG_DIR"
	sourceDefault       = "default"
)

// resolveSettingsPath picks the settings file the hooks commands act on.
//
// Claude Code reads its settings from $CLAUDE_CONFIG_DIR when that is set, so
// a profile launched that way never sees ~/.claude/settings.json. Defaulting
// to the home directory regardless would install hooks into a file the
// session does not read, and report success.
//
// --settings and --config-dir are mutually exclusive rather than ranked: two
// flags naming two files is a mistake worth stopping on, not a precedence
// rule to remember.
func resolveSettingsPath(settingsFlag, configDirFlag string) (path, source string, err error) {
	switch {
	case settingsFlag != "" && configDirFlag != "":
		return "", "", fmt.Errorf("%w: --settings and --config-dir both name a settings file; give one", errUsage)
	case settingsFlag != "":
		path, err := expandHome(settingsFlag)
		return path, sourceFlag, err
	case configDirFlag != "":
		dir, err := expandHome(configDirFlag)
		return filepath.Join(dir, "settings.json"), sourceConfigDirFlag, err
	}

	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		dir, err := expandHome(dir)
		return filepath.Join(dir, "settings.json"), sourceConfigDirEnv, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), sourceDefault, nil
}

// expandHome expands a leading ~. The shell does this for an unquoted
// argument but not for a quoted one, an environment variable, or the right
// side of --flag=~/x, and a literal "~" directory is never what was meant.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, path[1:]), nil
}

// readSettings parses a settings file, treating a missing one as empty.
//
// Numbers are decoded as json.Number so they are written back exactly as they
// were read. Decoding them as float64 would turn a large integer elsewhere in
// the operator's file into 1e+21 — a change to a key birddog has no business
// touching.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}

	// Refuse rather than risk rewriting a file we did not understand.
	refuse := func(err error) error {
		return fmt.Errorf("%s is not valid JSON, so birddog will not rewrite it: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var settings map[string]any
	if err := dec.Decode(&settings); err != nil {
		return nil, refuse(err)
	}
	// A second value after the first would be silently dropped on rewrite.
	if _, err := dec.Token(); err != io.EOF {
		return nil, refuse(fmt.Errorf("unexpected content after the settings object"))
	}
	if settings == nil {
		// The file said null. Treat it as empty rather than writing into nil.
		settings = map[string]any{}
	}
	return settings, nil
}

// writeSettings replaces a settings file, keeping a copy of what was there.
//
// This is the operator's own configuration. A backup is cheap, and being able
// to say exactly where the previous version went is the difference between a
// reversible change and an alarming one.
func writeSettings(path string, settings map[string]any) (backup string, err error) {
	if existing, err := os.ReadFile(path); err == nil {
		backup = path + ".birddog-backup"
		if err := os.WriteFile(backup, existing, 0o600); err != nil {
			return "", fmt.Errorf("write backup: %w", err)
		}
	}

	// HTML escaping is off so a hook command containing && or > is written
	// back as the operator wrote it, not as \u0026\u0026.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return "", err
	}
	encoded := buf.Bytes()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	// Write and rename, so an interrupted write cannot leave the operator
	// with a truncated settings file.
	tmp := path + ".birddog-tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return backup, nil
}

// HooksResult is what the hooks commands report.
type HooksResult struct {
	SettingsPath string `json:"settings_path"`

	// SettingsSource says which rule chose SettingsPath: "flag" (--settings),
	// "config_dir_flag" (--config-dir), "CLAUDE_CONFIG_DIR", or "default".
	SettingsSource string `json:"settings_source"`

	BackupPath string   `json:"backup_path,omitempty"`
	Changed    bool     `json:"changed"`
	Installed  bool     `json:"installed"`
	Partial    bool     `json:"partial,omitempty"`
	BinaryPath string   `json:"binary_path,omitempty"`
	Events     []string `json:"events,omitempty"`
	Missing    []string `json:"missing,omitempty"`

	// HandlerRunnable says whether the registered handler can actually be
	// run. Registration and runnability are different facts: a handler whose
	// binary has been removed leaves every hook firing and failing while the
	// installation still reads as complete.
	HandlerRunnable bool   `json:"handler_runnable"`
	HandlerProblem  string `json:"handler_problem,omitempty"`

	// BinaryPathWarning says the installed path is unlikely to keep working,
	// and why. It describes the path rather than the installation: the hooks
	// are correct now, and will stop being so when something that path
	// depends on moves.
	BinaryPathWarning string `json:"binary_path_warning,omitempty"`

	Note string `json:"note"`
}

const restartNote = "observed on Claude Code 2.1.267: sessions already running picked the hooks up without restarting, though a new session is the sure way"

func runHooksInstall(args []string, stdout io.Writer) error {
	fs := flags("hooks install")
	settingsPath := fs.String("settings", "", "settings file to edit (default: $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json)")
	configDir := fs.String("config-dir", "", "Claude Code config directory whose settings.json to edit; excludes --settings")
	binary := fs.String("binary", "", "path to the birddog binary (default: this one)")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	if err := parse(fs, args); err != nil {
		return err
	}

	path, source, err := resolveSettingsPath(*settingsPath, *configDir)
	if err != nil {
		return err
	}
	exe, err := expandHome(*binary)
	if err != nil {
		return err
	}
	if exe == "" {
		if exe, err = os.Executable(); err != nil {
			return fmt.Errorf("locate the birddog binary: %w", err)
		}
	}

	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	changed, err := hooks.Install(settings, exe)
	if err != nil {
		return err
	}

	// Checked here as status checks it. Install used to leave these fields at
	// their zero value, so every install — dry run or not, any binary —
	// reported handler_runnable: false. Not runnable is a warning, not a
	// refusal: the operator may be installing ahead of putting the binary in
	// place, and the hooks never block Claude when they fail.
	handler := hooks.CheckHandler(exe)

	result := HooksResult{
		SettingsPath:      path,
		SettingsSource:    source,
		HandlerRunnable:   handler.Runnable,
		HandlerProblem:    handler.Problem,
		BinaryPathWarning: hooks.FragilePath(exe),
		Changed:           changed,
		Installed:         true,
		BinaryPath:        exe,
		Events:            hooks.InstalledEvents,
		Note:              restartNote,
	}

	if changed && !*dryRun {
		if result.BackupPath, err = writeSettings(path, settings); err != nil {
			return err
		}
	}
	if *dryRun {
		result.Note = "dry run: nothing was written. " + restartNote
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		// Printed whether or not anything changed: an installation that is
		// already correct can still name a path that will not last.
		// Both, unlike status, which reports only the more urgent. Status
		// describes an installation that exists; install is the moment the
		// path is chosen, and a binary about to be put in place can still be
		// one that will not last.
		warn := func() {
			if result.HandlerProblem != "" {
				fmt.Fprintf(w, "\nThe handler these hooks run cannot run.\n  %s\n", result.HandlerProblem)
			}
			if result.BinaryPathWarning != "" {
				fmt.Fprintf(w, "\nThis path may not keep working.\n  %s\n", result.BinaryPathWarning)
			}
		}
		if !changed {
			fmt.Fprintf(w, "Already installed in %s — nothing to do.\n", path)
			warn()
			return nil
		}
		defer warn()
		if *dryRun {
			fmt.Fprintf(w, "Would add %d hook events to %s:\n", len(hooks.InstalledEvents), path)
		} else {
			fmt.Fprintf(w, "Installed %d hook events in %s.\n", len(hooks.InstalledEvents), path)
		}
		for _, e := range hooks.InstalledEvents {
			fmt.Fprintf(w, "  %s\n", e)
		}
		if result.BackupPath != "" {
			fmt.Fprintf(w, "\nYour previous settings: %s\n", result.BackupPath)
		}
		fmt.Fprintf(w, "\n%s.\n", capitalise(restartNote))
		fmt.Fprintf(w, "The handlers only report what they see: they cannot block a tool,\n")
		fmt.Fprintf(w, "approve anything, or change what Claude does.\n")
		return nil
	})
}

func runHooksUninstall(args []string, stdout io.Writer) error {
	fs := flags("hooks uninstall")
	settingsPath := fs.String("settings", "", "settings file to edit (default: $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json)")
	configDir := fs.String("config-dir", "", "Claude Code config directory whose settings.json to edit; excludes --settings")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}

	path, source, err := resolveSettingsPath(*settingsPath, *configDir)
	if err != nil {
		return err
	}

	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	changed, err := hooks.Uninstall(settings)
	if err != nil {
		return err
	}

	result := HooksResult{SettingsPath: path, SettingsSource: source, Changed: changed, Note: restartNote}
	if changed {
		if result.BackupPath, err = writeSettings(path, settings); err != nil {
			return err
		}
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		if !changed {
			fmt.Fprintf(w, "No birddog hooks in %s — nothing to do.\n", path)
			return nil
		}
		fmt.Fprintf(w, "Removed birddog's hooks from %s.\n", path)
		fmt.Fprintf(w, "Everything else in that file was left alone.\n")
		if result.BackupPath != "" {
			fmt.Fprintf(w, "Your previous settings: %s\n", result.BackupPath)
		}
		return nil
	})
}

func runHooksStatus(args []string, stdout io.Writer) error {
	fs := flags("hooks status")
	settingsPath := fs.String("settings", "", "settings file to read (default: $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json)")
	configDir := fs.String("config-dir", "", "Claude Code config directory whose settings.json to read; excludes --settings")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}

	path, source, err := resolveSettingsPath(*settingsPath, *configDir)
	if err != nil {
		return err
	}

	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	st := hooks.Status(settings)
	handler := hooks.CheckHandler(st.BinaryPath)

	result := HooksResult{
		SettingsPath:      path,
		SettingsSource:    source,
		BinaryPathWarning: hooks.FragilePath(st.BinaryPath),
		Installed:         st.Installed,
		Partial:           st.Partial,
		BinaryPath:        st.BinaryPath,
		Events:            st.Events,
		Missing:           st.Missing,
		HandlerRunnable:   handler.Runnable,
		HandlerProblem:    handler.Problem,
		Note:              restartNote,
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		// Reported before the event list: a handler that cannot run makes
		// every line after it describe coverage that does not exist.
		if handler.Problem != "" {
			fmt.Fprintf(w, "The registered handler cannot run.\n  %s\n\n", handler.Problem)
		} else if result.BinaryPathWarning != "" {
			// Only when it currently runs. A handler that is already broken
			// has a more urgent thing to say about the same path.
			fmt.Fprintf(w, "The registered handler runs, but this path may not keep working.\n  %s\n\n", result.BinaryPathWarning)
		}

		switch {
		case st.Installed:
			fmt.Fprintf(w, "Installed in %s, running %s.\n", path, st.BinaryPath)
		case st.Partial:
			fmt.Fprintf(w, "Partly installed in %s — some events are missing, so coverage\n", path)
			fmt.Fprintf(w, "is incomplete in a way that looks complete. Re-run `birddog hooks install`.\n")
		default:
			fmt.Fprintf(w, "Not installed in %s.\n", path)
			fmt.Fprintf(w, "Without hooks, birddog cannot see tool calls or permission requests\n")
			fmt.Fprintf(w, "in a Claude Code session, and reports that visibility as unavailable.\n")
			return nil
		}
		for _, e := range st.Events {
			fmt.Fprintf(w, "  %s\n", e)
		}
		for _, e := range st.Missing {
			fmt.Fprintf(w, "  %s (missing)\n", e)
		}
		fmt.Fprintf(w, "\n%s.\n", capitalise(restartNote))
		return nil
	})
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
