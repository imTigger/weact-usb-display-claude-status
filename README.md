# claude-display

Shows what Claude Code is doing on a **WeAct Studio Display FS 0.96"** USB screen
(160×80, USB `1a86:fe0c`).

```
Claude Code sessions ──HTTP hooks──▶ claude-display (systemd user service) ──serial──▶ panel
```

The daemon is the only process that writes to the panel. It tracks every open
session and shows the most urgent one:

| Screen | When |
|---|---|
| amber **APPROVE?** / **Question** / **Plan ready** / **INPUT?** / **Waiting**, pulsing | a session is waiting on you |
| red **✖ Error** | the turn ended on an API error (rate limit, overload, …) |
| orange **✻ Thinking…** / **✻ tool name** | Claude is working |
| purple **✻ Compacting** | context compaction |
| green **✔ Done** | the turn finished (turns into Ready after 5 min) |
| dark **Ready** | the session is idle |
| dim clock, then off after 10 min | no sessions open |

Working, Done and Error screens follow Claude Code's spinner line:

```
✻ Thinking…
qinheng-display        2:14    ← project, turn time
12 tools · 2 agents   ▪ ▪ ▪    ← this turn's tool calls and running subagents; one square per open session
```

Each square is an open session in the order they started, coloured by its
state; the one on screen is underlined.

With several sessions, the screen shows the most urgent one (needs you >
error > working > done > ready) and stays on it until something more urgent
happens, so busy sessions don't swap places at every tool call. Several open
prompts come up oldest first, one at a time, with a count (`2 waiting`) in
place of the squares. Only tool names are shown, never
commands or file contents.

## Setup

1. udev rule, so ModemManager leaves the panel alone and it gets a stable name
   (needs sudo):

   ```sh
   sudo install -m644 deploy/70-weact-display.rules /etc/udev/rules.d/
   sudo udevadm control --reload && sudo udevadm trigger
   ```

2. Daemon: `make install` (builds, installs to `~/.local/bin`, enables the
   `claude-display` user service).
3. Hooks: `make plugin` (adds this repo as a local plugin marketplace and
   installs `claude-display@qinheng-display`). Sessions started afterwards
   report to the daemon; sessions already open are mirrored from
   `~/.claude/sessions` as Thinking/Ready until restarted.

Turn the hooks off with `/plugin disable claude-display`, or remove them with
`make unplugin`. When the daemon stops, the panel goes back to its own screen.

## Debugging

```sh
curl -s localhost:47800/state      # what the daemon thinks
journalctl --user-unit claude-display -f
scripts/sim.sh                     # walk a fake session through every screen
make samples                       # render every screen to samples/*.png
```

## Panel protocol notes

Verified on firmware V1.0.0.2 (see `device.go`):

- Commands are `[cmd][args, little-endian][0x0A]`, pixels are RGB565 little-endian.
- **The `0x05` bitmap header must be its own USB transfer.** Sending it in the
  same write as the pixels makes the firmware drop the frame and keep showing
  its standalone screen.
- Orientation 3 (landscape rotated 180°) suits this mount; pass `-orientation 2`
  to flip it.
- The standalone ("unconnected") orientation and brightness (`0x10`/`0x11`) are
  stored in flash; this project never writes them.
