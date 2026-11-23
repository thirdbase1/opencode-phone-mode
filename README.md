# opencode-phone-mode

Switch a laptop between its graphical desktop and a headless phone mode for
piloting OpenCode from a phone, then restore its exact prior state.

Phone mode snapshots every setting it touches, applies the power and radio
changes, and removes the desktop. Running `off` restores the snapshot
byte-for-byte and deletes it.

Supported platforms:

| | Linux | Windows |
|---|---|---|
| Directory | `.` (this root) | [`windows/`](windows/) |
| Session switch | `systemctl isolate multi-user.target` | stop `explorer.exe` |
| Restore model | identical | identical |

Both share the same architecture -- read every setting, write a snapshot,
apply a plan of changes, then write the snapshot back verbatim on the way out
-- so the restore guarantees are the same on both. The mechanisms differ and
are listed in [windows/README.md](windows/README.md).

## Requirements (Linux)

- Linux with systemd, sysfs cpufreq, and `rfkill`
- `sudo` for `on` and `off` (reads for `status` do not need it)
- Optional: `powertop` for `powertop --auto-tune`, `bbswitch` for dGPU power-off
- Go 1.22 or newer to build

## Build and install

```sh
make check
make install
```

This installs `~/.local/bin/opencode-phone-mode` and writes a default
`~/.config/opencode-phone-mode/config.toml` if one does not exist yet.

## Windows

The Windows build lives in [`windows/`](windows/) and is a separate Go module,
so neither build affects the other:

```powershell
cd windows
make check     # vet + tests, on any OS
make install   # copies the exe and win.ps1 to %LOCALAPPDATA%\Programs\win-phone-mode
```

The first `win-phone-mode on` writes
`%APPDATA%\win-phone-mode\config.toml`. Settings, caveats, and the
Linux-to-Windows mechanism mapping are documented in
[windows/README.md](windows/README.md).

Unlike the Linux version, `on` stops the desktop shell, so there is no local
console left to type `off` into. Use RDP, SSH, or a scheduled task to run it,
and preview everything first with `win-phone-mode on --dry-run`.

## Usage

Run it as your normal user, not with `sudo`. It asks for sudo only for the
privileged steps; running the whole binary as root would read root's config and
operate on root's user services.

```sh
opencode-phone-mode on
opencode-phone-mode status
opencode-phone-mode off

opencode-phone-mode on --dry-run
opencode-phone-mode off --dry-run
opencode-phone-mode idle --dry-run
```

`--dry-run` prints every change as `setting: current -> new`, makes no
changes, does not ask for sudo, and does not write state.

`on` refuses to run when `state.json` already exists, because snapshotting
again would overwrite the values needed to restore the desktop. Run `off`
first, or delete the stale state file yourself.

`on` also refuses to go headless when the battery is below
`battery_exit_percent` and the machine is not charging. `status` prints the
same warning without blocking anything.

`off` is safe to run from the desktop. If `graphical.target` is already active,
it restores saved settings but does not restart `systemd-logind` or isolate a
target, so it cannot blank a working session or bounce SDDM. Radio restore uses
the numeric `rfkill` ID (falling back to the radio type), because device names
such as `hci0` are rejected by this system's `rfkill`.

## Telegram control path

Phone mode is only useful if you can still reach the machine after the desktop
is gone. Here the path is Telegram → `opencode-telegram.service` →
`opencode-serve.service` → OpenCode sessions. Both are `systemd --user` units
listed in `keep_services`, and Wi-Fi stays unblocked so the bot can reach the
Telegram API.

[TELEGRAM_SETUP.md](TELEGRAM_SETUP.md) covers the whole chain: creating the bot,
the `.env` values for OpenCode V2, the two systemd units, enabling linger, and
troubleshooting failed message delivery.

## Quiet-time idle profile

Standing by still costs power. After the bot log has been quiet for
`idle.quiet_minutes`, the idle profile lowers the CPU cap from the phone-mode
value (40% by default) to `idle.cpu_max_freq_percent` (20% by default). The bot
stays online and replies immediately; only the CPU ceiling changes. It never
suspends, because a sleeping machine cannot answer Telegram.

Activity is the modification time of the newest `bot-*.log` in
`~/.config/opencode-telegram-bot/logs`. A new message or reply updates it. On
the next check, the tool writes the exact `scaling_max_freq` values it saved
before capping, then deletes `idle.json`. A failed restore keeps that file so
it can be retried. `off` also restores the original values from `state.json`
and clears the idle state.

This needs a root systemd timer because CPU sysfs files are root-owned and
`sudo` needs a password here. After `make install`:

```sh
make install-idle-timer
```

It asks for your password once, installs `opencode-phone-mode-idle.service` and
`.timer` into `/etc/systemd/system`, substitutes your home and user into them,
and enables the timer. It checks every minute, does nothing outside phone mode,
and does not need a password at runtime. `status` shows whether the profile is
applied. To stop using it, set `enabled = false` under `[idle]`, or run
`sudo systemctl disable --now opencode-phone-mode-idle.timer`.

The saving is modest: the display and desktop are already off in phone mode.
This mainly lowers peak power and heat during long idle stretches.

## Battery life

The main saving is the display and the desktop session, not the CPU knobs.
On this machine (HP laptop, 34.6 Wh battery, iGPU only), a discharge run in
phone-mode-style idle use went from 65% to 6% in 9h15m:

- roughly **6.4% per hour**
- roughly **2.2 W average**
- extrapolated **14-15 hours** from 100% down to near-empty

A 30-minute check at 65% while piloting showed the gauge barely moving. At
~2 W that is under 2%, so a flat reading is expected rather than surprising.

What actually saves power here:

- backlight at 0 and no compositor (`multi-user.target`)
- Bluetooth off
- the `active` marker telling agents to stay light, so builds and test suites
  do not start in the background

What does not necessarily save energy:

- the governor is already `powersave` on this machine, so that step changes
  nothing
- capping max frequency to 40% lowers peak power and heat, but for a fixed
  amount of work it mostly makes the work take longer. "Race to idle" can use
  less total energy. This is why the cap is configurable.

These numbers come from one discharge run on one machine and are not a
guarantee. Battery wear, Wi-Fi traffic, and workload change the result. The
watt column in `battery.log` repeats a constant value and is not usable; the
percentage-over-time is the trustworthy part. To measure your own, charge to
full, run phone mode, and check `status` at the same time each hour.

`battery_exit_percent` defaults to 15: below that and unplugged, `on` refuses
so the phone link survives. At ~14-15 hours total, 15% leaves roughly two hours
of headroom. Lower it if you want more runway.

## Configuration

The file is `~/.config/opencode-phone-mode/config.toml`. Every key is optional:
omitted keys use the default shown in [`config.example.toml`](config.example.toml),
which is also the template installed by `make install`. Unknown keys are
rejected so typos do not silently disable a feature. Values are validated when
the config is loaded.

### Main options

| Key | Default | Values and effect |
|---|---|---|
| `keep_services` | `["opencode-serve.service", "opencode-telegram.service"]` | systemd **user** units to survive the switch. Active units are left running; inactive ones are enabled and started. Never restarted, so `on` cannot kill the session that launched it. |
| `keep_radios_on` | `["wlan"]` | rfkill types left unblocked: `wlan`, `bluetooth`, `wwan`, `gps`, `nfc`, `ufs`, `wimax`. `wifi` is an alias for `wlan`. Every other type is soft-blocked. |
| `manage_backlight` | `true` | `false` leaves the backlight untouched. |
| `brightness` | `0` | Backlight percentage, 0-100. `-1` leaves it untouched. |
| `backlight_device` | `""` | Device name under `/sys/class/backlight`, e.g. `intel_backlight`. Empty picks the first device. |
| `manage_governor` | `true` | `false` leaves the governor untouched. |
| `governor` | `"powersave"` | Any governor from `scaling_available_governors`, e.g. `powersave`, `performance`, `schedutil`, `ondemand`. |
| `manage_max_freq` | `true` | `false` leaves `scaling_max_freq` untouched. |
| `cpu_max_freq_percent` | `40` | Cap each CPU policy to 1-100% of its hardware maximum; clamped at `scaling_min_freq`. |
| `manage_turbo` | `true` | `false` leaves turbo untouched. |
| `turbo` | `false` | Turbo on/off. |
| `powertop` | `true` | Run `powertop --auto-tune`. Needs `powertop` installed and has no portable undo. `false` skips it. |
| `disable_dgpu` | `true` | Runtime-suspend non-boot GPUs, plus bbswitch/nvidia-persistenced when present. `false` leaves GPU settings alone. |
| `manage_lid` | `true` | `false` leaves logind lid settings untouched. |
| `lid_switch` | `"ignore"` | `ignore`, `suspend`, `hibernate`, `hybrid-sleep`, `suspend-then-hibernate`, `poweroff`, `reboot`, `halt`, `kexec`, `lock`. |
| `manage_radios` | `true` | `false` leaves every rfkill device as it was. |
| `manage_services` | `true` | `false` leaves linger and `keep_services` as they were. |
| `isolate_target` | `true` | `false` keeps the current target; no isolation happens. |
| `battery_exit_percent` | `15` | 0-100; `0` disables the guard. `status` warns below it and `on` refuses while discharging. Charging/full always pass. |

### Idle options (`[idle]`)

| Key | Default | Values and effect |
|---|---|---|
| `enabled` | `true` | `false` disables the quiet-time profile entirely. |
| `quiet_minutes` | `10` | Minimum quiet time before applying it. Must be at least 1 when enabled. |
| `cpu_max_freq_percent` | `20` | Idle CPU cap, 1-100% of hardware maximum, clamped at `scaling_min_freq`. Set below `cpu_max_freq_percent` to have an effect. |
| `activity_log_dir` | `""` | Directory containing `bot-*.log`. Empty uses `~/.config/opencode-telegram-bot/logs`. Only mtimes are read. |

Any `manage_*` switch set to `false` means that setting is neither snapshotted
nor changed, so `off` will not restore it either.

### Restore fallback (`[restore]`)

Used only when `off` runs without `state.json`. With a state file present,
`off` restores the recorded values exactly and ignores this table.

| Key | Default | Effect |
|---|---|---|
| `brightness` | `100` | Backlight percentage, 0-100. |
| `governor` | `""` | Governor name, or empty to leave it alone. |
| `cpu_max_freq_percent` | `100` | Frequency cap, 1-100% of hardware maximum. |
| `turbo` | `true` | Turbo on/off. |
| `lid_switch` | `"suspend"` | Lid action (same values as above), or empty to remove our drop-in and use the system default. |
| `bluetooth` | `true` | Leave Bluetooth unblocked and its service running. |
| `services_running` | `true` | Enable linger and start `keep_services`. |

`off` prints a warning when it falls back to these values. Restores are
attempted independently; if any step fails, the state file is kept so `off`
can be retried. Use `on --dry-run` and `off --dry-run` to preview the effect of
a config change before applying it.

## What is snapshotted

- backlight raw value
- effective lid action and the exact contents of
  `/etc/systemd/logind.conf.d/99-opencode-phone-mode.conf`
- every cpufreq policy's governor and `scaling_max_freq`
- turbo control value (`no_turbo` or `boost`)
- rfkill soft-block state per device
- Bluetooth service state
- `keep_services` active/enabled state and logind linger
- non-boot GPU `power/control`, `bbswitch`, and `nvidia-persistenced`
- the phone-mode marker at `~/.cache/opencode-phone-mode/active`

## Caveats

- `powertop --auto-tune` changes kernel tunables with no portable undo. `off`
  cannot restore them; disable `powertop` if that matters.
- "Discrete GPU off" uses runtime suspend (`power/control=auto`) and, when
  present, `bbswitch` plus stopping `nvidia-persistenced`. Vendor-specific
  power-off tools are not invoked.
- `on` isolates `multi-user.target` as its last step. If the command is running
  from a graphical terminal, that terminal closes.
- If a keep_service was inactive before `on`, `off` stops it to restore the
  snapshot. If that unit hosts your session, run `off` from a terminal or SSH
  so the stop cannot interrupt the restore.
- The old `active` marker is still maintained alongside `state.json` for the
  existing agent battery-mode rule.

## Tests

```sh
make test
```

The tests use a fake host and cover config parsing and validation, snapshot
round-trips, dry-run behavior, fallback restores, exact lid-file restoration,
and continue-after-failure semantics. See [`TEST_NOTES.md`](TEST_NOTES.md) for
known gaps.
