# birddog — opencode plugin

Makes a live opencode session observable by
[birddog](https://github.com/BrutalSystems/birddog).

A default `opencode` TUI opens no TCP port and writes no port file, lockfile or
pid file. Nothing outside the process can find it. This plugin closes that gap
from the inside: it publishes one JSON record per live session to
`~/.birddog/opencode/`, and birddog reads them.

**Without this plugin there is nothing to observe** — an opencode session is
invisible to birddog even when birddog is installed.

## What it reports

| | |
|---|---|
| `active` / `idle` | from `session.status` and `session.idle` |
| `running_tool` | a tool is executing, with its name and start time |
| `waiting_input` | a permission request is outstanding, with its id |
| session identity | id, slug, title, working directory, opencode version |

opencode is the only runtime where birddog can see a permission request at
all. For every other provider it reports input-request visibility as
*unavailable* — which is not the same as reporting that there is none.

## What it does not do

It observes. It sends no prompts, approves no requests, changes no tool
arguments, and never throws.

That last one is load-bearing. opencode's `tool.execute.before` hook can
**block a tool** by throwing — that is how its own env-protection example
works. Every hook here is guarded, so a failure inside birddog costs birddog
its visibility and never costs the session its work.

## Install

Prefer letting [muster](https://github.com/BrutalSystems/muster) install it at
launch, by npm specifier:

```toml
[plugins.birddog]
npm = "@brutalsystems/birddog-opencode"
```

```sh
muster run opencode --plugin birddog --prompt '...'
```

A copied file goes stale in silence — the package updates, the copy does not,
and the old code keeps running.

A specifier is better but not immune: opencode resolves it once into
`~/.cache/opencode/packages/@brutalsystems/birddog-opencode@latest` and does
not revisit it, so a new release is not picked up until that directory is
removed. Check what is actually loaded rather than assuming:

```sh
grep plugin_version ~/.birddog/opencode/*.json
rm -rf ~/.cache/opencode/packages/@brutalsystems/birddog-opencode@latest
```

To install by hand anyway, note that opencode's loader globs
`{plugin,plugins}/*.{ts,js}` one level deep only, so it is two copies:

```sh
mkdir -p ~/.config/opencode/plugin
cp birddog.ts ~/.config/opencode/plugin/
cp -r birddog-lib ~/.config/opencode/plugin/
```

`birddog-lib/` is never globbed by the loader, which is the point of the
name: `~/.config/opencode/plugin/` is shared with every other plugin, so a
directory called `lib/` would be ambiguous.

## Verify it is loaded

```sh
ls ~/.birddog/opencode/          # one ses_*.json per live session
birddog discover                 # the sessions birddog can now see
```

If nothing appears, check `~/.birddog/opencode-plugin.log`. The plugin cannot
print to the terminal: `console.error` would land in the TUI opencode is
drawing its interface on, and nowhere readable afterwards.

Confirm the loaded version matches what is installed — a hand-copied plugin
lagging its package is the failure the npm specifier exists to prevent:

```sh
grep plugin_version ~/.birddog/opencode/*.json
```

## Requirements

- opencode 1.18.31 (verified; other versions untested)
- Node 22 or later

## Where the records go

`~/.birddog/opencode/<session_id>.json`, owner-only, written by rename so a
reader never sees a half-written record. Set `BIRDDOG_HOME` to move both
halves somewhere else.

Each record carries the writing process's pid and a heartbeat, which is how
birddog tells a session that has gone quiet from a process that died —
including when the pid has since been reused by something else.

## License

MIT
