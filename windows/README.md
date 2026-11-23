# win-phone-mode

Switch a Windows laptop between its desktop and a headless **phone mode** for
piloting OpenCode from a phone, then restore its exact prior state.

This is a Windows port of
[codetesla51/opencode-phone-mode](https://github.com/codetesla51/opencode-phone-mode),
which does the same thing on Linux. The architecture is unchanged: read every
setting, write a snapshot, apply the changes, and on the way back write the
snapshot out verbatim.

```
on      snapshot -> apply phone mode
status  show mode, settings, battery   (no admin)
off     restore the snapshot exactly, then bring the desktop back
idle    quiet-time CPU cap while the bot stands by
```

Every command accepts `--dry-run`, which prints each change as
`setting: current -> new`, makes no changes, asks for no elevation, and writes
no state.

## Why a port, and what changed

The Linux original snapshots sysfs files, drives `rfkill` and `systemctl`, and
switches the session with `systemctl isolate multi-user.target`. Windows has no
`multi-user.target`, no sysfs, and no single place to turn the UI off. The
mechanisms were therefore replaced one-for-one:

| Linux original | Windows equivalent | Notes |
|---|---|---|
| `systemctl isolate multi-user.target` | stop `explorer.exe` | The closest real equivalent: the session keeps running, the UI goes away. |
| `/sys/class/backlight/*/brightness` | WMI `WmiSetBrightness` | Unsupported on desktops and some panels; treated as a normal condition, not an error. |
| `cpufreq` `scaling_max_freq` | `powercfg` *Maximum processor state* | Per-scheme AC/DC index pair. |
| `intel_pstate/no_turbo`, `cpufreq/boost` | `powercfg` *Processor performance boost mode* | |
| `rfkill block bluetooth` | `Disable-PnpDevice` on the Bluetooth class | |
| `systemctl stop bluetooth.service` | service state, when the device path is insufficient | |
| `logind.conf` lid drop-in | `powercfg` *Lid close action* | No logind restart needed. |
| `systemctl --user` keep-services + linger | Windows service run state + start mode | Start mode matters: a *disabled* service cannot be started at all. |
| `powertop --auto-tune` | *not ported* | No portable Windows equivalent, and no undo. |
| dGPU via `power/control` / bbswitch | disable the Display-class PnP device | Off by default; see caveats. |

Two things the Linux version had for free needed real work here:

- **Elevation.** Windows has no per-command `sudo`. The process re-launches
  itself once through the UAC prompt, remembers that it did, and forwards its
  own output back through a log file, because an elevated child gets a new
  console that closes the moment it exits. Without that handoff the user would
  see nothing at all.
- **Output parsing.** `powercfg` and `Get-PnpDevice` print human-readable text.
  Each parser is pinned by tests built from real command output, including the
  localised shapes, and refuses input it does not fully recognise rather than
  guessing — a guessed value would be written back to the user's machine.

## Requirements

- Windows 10 or 11
- Administrator rights for `on`, `off`, and `idle` (a UAC prompt appears)
- PowerShell 5.1, which ships with Windows
- Go 1.22 or newer to build

`status` and every `--dry-run` run unelevated.

## Build and install

```powershell
make check     # vet + tests
make install   # copies the exe and win.ps1 to %LOCALAPPDATA%\Programs\win-phone-mode
```

`win.ps1` **must** sit next to the executable; the tool refuses to run the
helper from anywhere else, because it executes PowerShell with administrator
rights. Copying the `.exe` alone somewhere and running it is a deliberate
failure rather than a silent security hole.

The first `on` writes a default config to
`%APPDATA%\win-phone-mode\config.toml` if none exists.

## Usage

Run it as your normal user. It asks for elevation itself; running the whole
thing from an already-elevated shell works too but is not required.

```powershell
win-phone-mode on --dry-run     # preview every change first
win-phone-mode on
win-phone-mode status
win-phone-mode off
```

`on` refuses to run when `state.json` already exists, because snapshotting
again would overwrite the values needed to bring the desktop back. Run `off`
first, or move the stale file aside yourself.

`on` also refuses when the battery is below `battery_exit_percent` and the
machine is not charging. `status` prints the same warning without blocking
anything.

Because `on` stops the shell, there is no console left to type `off` into
afterwards. Run `off` over RDP or SSH, or install it as a scheduled task
(`schtasks /create ... /tr "...\win-phone-mode.exe off"`) that you can trigger
from your phone. `win-phone-mode status` never needs a desktop.

## The biggest difference from the Linux version

Linux `off` re-isolates `graphical.target` and the display manager brings the
desktop back by itself. On Windows, `off` restarts `explorer.exe` only if the
session had one when phone mode was entered. That is recorded in the state
file. If you entered phone mode from a service or an SSH session there is no
desktop to return to, and none is fabricated.

## Restore guarantees, and the one case they cannot cover

`off` writes back every value it recorded, in the order that makes the result
work:

1. power indices, into the scheme they were read from
2. brightness
3. devices re-enabled
4. services restored (stop before disabling, since Windows refuses to disable a
   running service)
5. `explorer.exe` restarted, if the session had one
6. the original power scheme activated **last**, so the restored values are what
   take effect

If any step fails, the state file is **kept** and `off` can be run again; it is
only deleted after every step succeeded. `off` is designed to be re-runnable
after a partial failure.

If the state file exists but cannot be read (corrupted, or written by a newer
version), `off` **refuses** rather than applying the `[restore]` defaults. It
tells you to move the file aside. Overwriting real settings with guessed
defaults, and then leaving a file that blocks `on` forever, is the one outcome
this tool will not choose.

`powertop`-style kernel tuning is the one thing the Linux original could not
undo either. Nothing equivalent is applied here.

## Configuration

`%APPDATA%\win-phone-mode\config.toml`. Every key is optional; omitted keys use
the default shown in [`config.example.toml`](config.example.toml), which is also
the template installed on first run. Unknown keys are rejected, so a typo cannot
silently disable a feature. Values are validated when the config is loaded, and
a `manage_*` switch set to `false` means that setting is neither snapshotted nor
changed.

| Key | Default | Values and effect |
|---|---|---|
| `keep_services` | `["OpenCodeServe", "OpenCodeTelegram"]` | Windows service **short names** to keep running. A *disabled* service is re-enabled first, and `off` puts the mode back. |
| `keep_radios_on` | `["wlan"]` | Radio types left alone. `wlan` and `wwan` are never blocked; adding `bluetooth` keeps Bluetooth enabled. |
| `manage_desktop` | `true` | `false` leaves the shell and its state alone. |
| `desktop_shell` | `"explorer.exe"` | The shell process treated as "the desktop". |
| `isolate_desktop` | `true` | `false` keeps the UI: only power and radio settings change. This is the switch that makes phone mode non-disruptive. |
| `manage_display` | `true` | Snapshot/restore brightness. |
| `brightness` | `0` | Brightness percentage, 0-100. `-1` leaves it untouched. |
| `manage_power_scheme` | `true` | Switch to the phone-mode scheme. |
| `power_scheme` | `"saver"` | `saver`, `balanced`, `performance`, or a scheme GUID. |
| `manage_cpu` | `true` | Cap the CPU. |
| `cpu_max_percent` | `40` | 1-100% of the maximum processor state. |
| `manage_turbo` | `true` | Processor performance boost mode. |
| `turbo` | `false` | Boost on/off. |
| `manage_display_timeout` | `true` | Turn the panel off quickly. |
| `monitor_off_seconds` | `60` | Seconds until the display sleeps; `0` is never. |
| `manage_sleep` | `true` | Set *sleep* and *hibernate* to never, because a sleeping machine cannot answer messages. |
| `manage_lid` | `true` | Lid close action. |
| `lid_action` | `"ignore"` | `ignore`, `sleep`, `hibernate`, `shutdown`. |
| `manage_radios` | `true` | Disable Bluetooth devices. |
| `disable_dgpu` | `false` | Disable non-primary Display-class adapters. Off by default; see caveats. |
| `manage_services` | `true` | Snapshot and restore `keep_services`. |
| `battery_exit_percent` | `15` | 0-100; `0` disables the guard. Charging and full always pass. |

### Idle profile (`[idle]`)

Standing by still costs power. After the bot log has been quiet for
`quiet_minutes`, the profile lowers the CPU cap from the phone-mode value (40%
by default) to `cpu_max_percent` (20% by default). The bot stays online and
replies immediately; only the CPU ceiling changes. It never suspends.

Activity is the modification time of the newest `bot-*.log` in
`activity_log_dir` (`%APPDATA%\opencode-telegram-bot\logs` by default). A new
message or reply updates it. The next check writes back the exact values saved
before the cap was applied, then deletes `idle.json`. A failed restore keeps
that file so it can be retried; a file left behind after a *successful* restore
(process killed mid-cleanup) is detected and cleared.

This needs a scheduled task, because the power settings are machine-wide:

```powershell
schtasks /create /tn win-phone-mode-idle /sc minute /mo 1 /rl highest `
  /tr "%LOCALAPPDATA%\Programs\win-phone-mode\win-phone-mode.exe idle"
```

It does nothing outside phone mode. `status` shows whether the profile is
applied. To stop using it, set `enabled = false` under `[idle]` or delete the
task.

### Restore fallback (`[restore]`)

Used only when `off` runs with **no** state file. With a state file present the
recorded values are restored and this table is ignored.

| Key | Default | Effect |
|---|---|---|
| `brightness` | `100` | Backlight percentage. |
| `power_scheme` | `""` | Scheme name or GUID, or empty to leave it alone. |
| `cpu_max_percent` | `100` | Frequency cap, 1-100%. |
| `turbo` | `true` | Boost on/off. |
| `lid_action` | `"sleep"` | Lid action, or empty to leave it alone. |
| `bluetooth` | `true` | Leave Bluetooth enabled. |
| `services_running` | `true` | Start `keep_services`. |
| `desktop_shell` | `true` | Restart the shell. |

Sleep timeout is not in this table: without a snapshot the old value is
unknowable, so the fallback restores the Windows default rather than inventing
one.

## What is snapshotted

- the active power scheme GUID
- the AC and DC index for every power setting phone mode changes
- brightness
- the desktop shell's run state, and whether the session had one at all
- every `keep_services` run state and start mode
- Bluetooth (and, if enabled, Display) device state
- the phone-mode marker at `%LOCALAPPDATA%\win-phone-mode\active`

Anything that cannot be read is skipped and reported, never guessed: a wrong
restore value is worse than a missing one.

## Caveats

- **Discrete GPU disable is off by default.** `Disable-PnpDevice` on a display
  adapter can blank a multi-monitor setup, and some laptops do not bring the
  adapter back cleanly. Enable it only if you know your machine.
- **`off` is not a login-screen recovery tool.** If you cannot reach the machine
  at all, use RDP, an SSH server, or a scheduled task running `off`.
- **Stopping `explorer.exe` ends the visible session.** That is the point, but
  any unsaved application state in that session is at risk, exactly as the
  Linux version warns about the terminal that launched `on`.
- **Windows services need short names.** `OpenCodeServe`, not "OpenCode Serve".
  An unknown name is reported as "not installed" rather than silently ignored.
- **The power settings are machine-wide**, not per-user. A second user logged in
  at the same time shares the cap.
- Localised Windows is supported by parsing the GUIDs and numeric values instead
  of the English labels, but the fallback only fires on shapes it fully
  recognises. If `powercfg` output changes shape, `on` reports a read failure
  and changes nothing.

## Tests

```powershell
make test
```

The tests run without a Windows box: a fake host stands in for the machine, so
snapshot/restore round-trips, dry-run behaviour, state-file lifecycle,
fail-continue-and-retry semantics, the idle apply/restore decision table, and
localised `powercfg` parsing are all exercised here. Two properties are checked
rather than asserted: Windows argv quoting is fuzzed against an independently
written `CommandLineToArgvW`-rules parser, and the PowerShell helper's output
shape is pinned to strings captured from real runs with mocked cmdlets.
