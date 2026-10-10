# Active PV Battery Charging: journal-based control

Development status, 10 October 2026. No production Pi, database or token was accessed.
This build can issue real sonnen charging commands. It is no longer observation-only.
The hardware lease design remains available for other adapters, but sonnen now uses
an action journal under the explicit assumption that evcc is the intended sole controller.

## Observation and decisions

The sonnen adapter reads mode, charging flag, power and SoC from one uncached
`/api/v2/status` response. Mode 1 is manual; modes 2 and 10 are automatic.
Missing or unknown mode, non-OnGrid status, invalid measurements and observations
older than ten seconds prevent acquisition. Repeated identical timestamps do not
refresh freshness. Timestamps are change markers: the first response cannot be
independently dated. Configured identity is `sonnen:<host>`, not a hardware serial
number. Changing the configured host causes a conflict; swapping hardware at the
same address cannot be detected by this identity.

Automatic native charging is never taken over, even if export persists. A negative
measured battery power or native charging flag suffices to leave it alone.
An unexplained manual mode produces a persistent conflict without writing to the battery.

Start requires the configurable GUI **PV charging start value**, default 500 W,
automatic idle operation, sufficient SoC headroom, valid PV measurements and an
available positive charge-power limit. The start value is independent of that limit.
The 100 W reserve (or larger site reserve) is deducted after the threshold check:
500 W export initially requests at most 400 W charging. Other battery discharge
and newly allocated loads are excluded. Household demand is already in grid power.

During charging, measured charging power plus usable export minus reserve sets
the new request, capped by PV and the configured limit (currently 3300 W).
Between zero grid power and the export reserve, the last request is held.
Falling below the start value alone does not stop charging. Any positive valid
measured grid import, full battery/SoC limit, invalid budget, missing PV or revoked
permission starts return to automatic control. There is no arbitrary 80% taper.
This is cycle-based regulation, not a guarantee against short physical import transients.

## Durable action journal

Each battery has an append-only JSONL file next to the configured database:
`evcc.db.pv-<SHA256 of device config name>.jsonl`. Its `.lock` file holds an OS
exclusive lock for the process lifetime. Another process using that same journal
cannot claim it. Independent databases/hosts do not share this protection.
In-memory databases or unavailable journal storage disable journal-based control.
No database migration is added. The existing start-value setting remains in evcc settings.

Every command follows this order:

1. Append and sync intent (identity, previous automatic mode, requested watts,
   revision, timestamp and reason), including directory sync on Linux.
2. Send the battery command.
3. Observe actual manual mode and check a setpoint readback when supported.
4. Append and sync confirmation. An unchanged command is not rewritten each cycle.

Release and user decisions are persisted the same way. A torn final record,
unreadable file or failed write blocks further control. Corrupt records are not
silently discarded. The files contain no token. History currently has no automatic
rotation; monitor disk use. An evcc database export does not include these sidecar
files. Copy journal files with the stopped service when preserving recovery history.
If a journal is absent while the battery is manual, evcc reports a conflict.

## Restart and fault state machine

| Journal / observed state | Action |
| --- | --- |
| No active record, automatic idle | Observe; acquire only when all start conditions hold. |
| Automatic native charging | Leave it alone. |
| Confirmed active record after restart | Restore previous automatic mode before any new charging. |
| Unconfirmed intent, automatic | Close the interrupted operation without a device write. |
| Unconfirmed intent, manual/unknown | Persist conflict; ask the user. |
| Unknown manual mode / changed identity / different readable setpoint | Persist conflict; send no further commands. |
| Battery returns to automatic mode | End our session; this alone is not foreign control. |
| Read failure while active | Persist recovery; retry observation and release after reconnection. |
| Release fails | Keep recovery pending; do not resume charging. |
| Journal unavailable | Block writes, show the problem. |
| User disables control | Persist disabled state across restarts; no device write. |

A conflict is sticky across restart and is shown on the Battery page. The user can
choose **Yes / unsure: disable** or **No: allow evcc recovery**. Recovery permission
is persisted first and executed on the next cycle, after another observation.
It restores the recorded automatic mode (2 or 10), or the configured automatic
default when no previous mode is known. It does not immediately start charging.
The decision endpoint requires authentication and the displayed journal revision;
a stale dialog receives HTTP 409 and cannot approve a newer conflict.

## Limits of the ownership evidence

The journal proves what evcc recorded and attempted, not exclusive hardware ownership.
The sonnen adapter has no verified setpoint readback, owner token or independent
command watchdog. HTTP success plus manual-mode observation is recorded honestly
as acknowledgement, not as verification of actual requested watts. Another writer
changing a setpoint while remaining in manual mode is currently undetectable.
An optional `ChargeSetpoint` readback is supported generically and tested, but measured
battery power must never be substituted for it: taper, limits and response lag
naturally change actual watts.

A powered-off or disconnected evcc cannot stop a previously issued manual charge.
Recovery runs when evcc and communications return. Battery firmware protections
remain in effect, but are not a substitute for a verified independent watchdog.
A read/write race against another controller is not fenced by the local journal.

## EV coordination and existing mode features

Manual or unobserved batteries contribute no EV discharge capacity. Their existing
charging demand is not advertised as reclaimable PV. Active EV battery boost
revokes PV battery charging permission so automatic battery operation can resume.
The cloud-boost controller itself sends no manual discharge commands; see
[pv-cloud-boost.md](pv-cloud-boost.md).

Other site-level battery commands still do not bypass the ownership guard:
legacy tariff charging, external mode requests and discharge holds are suppressed
for guarded devices. These features have not been migrated to the journal.
`batteryMode` is a requested site mode; use `batteryPVControl` for actual observations.
This restriction is material when assessing a controlled test of this development build.

## Verification and ARM64 build

Tests cover write ordering, confirmed and interrupted restart paths, native charging,
conflict decisions, stale revisions, failed observations/writes/releases, identity
changes, measured-power versus setpoint differences, torn journals, file locking,
SoC/grid/start-value boundaries and EV budget exclusion. HTTP tests exercise sonnen
status and exact restoration of modes 2/10, plus refusal of arbitrary/manual restore
modes. GUI tests verify explicit decisions, persistent errors and disabled state.
These are simulated tests, not live hardware validation.

```sh
go test ./core/... ./api/... ./server/... -count=1
go test ./meter -skip '^TestTemplates$' -count=1
go test ./meter -run '^TestTemplates/sonnenbatterie' -count=1
vp test run --maxWorkers=1 --no-file-parallelism
vp build
mkdir -p build
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags=release -trimpath \
  -ldflags="-X github.com/evcc-io/evcc/util.Version=0.316.2-pv-journal-dev -X github.com/evcc-io/evcc/util.Commit=$(git rev-parse --short HEAD) -s -w" \
  -o build/evcc-pv-journal-linux-arm64 .
sha256sum build/evcc-pv-journal-linux-arm64
```

The full meter template matrix has unrelated platform-specific connection error
assertions on Windows; the sonnen template is checked separately. Building does
not install anything. Production deployment remains a separate controlled step.

---

## Historical prototype description (superseded)

The following text records the initial prototype. Its activation, limits and
no-UI/no-persistence statements are historical and do not describe this version.

# Active PV Battery Charging prototype

This branch enables the rule in code. It adds no settings, database migration, API endpoint or UI. It uses `api.BatteryController`, `api.Battery`, `api.BatteryPowerLimiter` and optionally `api.BatterySocLimiter`. Device templates and protocols are unchanged.

## Control rule

The site evaluates the rule after a successful meter cycle. An actual grid meter and at least one PV meter are required. PV and battery power are read again to reject unavailable or non-finite values that the existing optional meter collection otherwise replaces with zero. These reads use the existing device cache where available.

Eligible batteries must support both `charge` and `normal`, report a valid SoC below 100% and any configured maximum, and expose a finite positive maximum charge power. A missing charge power limit disables forced PV charging for that device.

`BatteryController` does not offer a power setpoint. `charge` can force the configured maximum power, including grid import. The prototype therefore reserves the entire declared maximum charge power before requesting `charge`. It cannot actively charge a 3300 W battery using only 1000 W of surplus. A proportional PV controller would need an additional generic writable power interface.

The available budget is:

```text
available = min(measured PV,
                -grid power
                -all battery discharge power
                +current charging power of eligible batteries)
            -max(0, configured residual power)
```

Grid power is positive for import; battery power is positive for discharge. Household loads, EVs and charging by ineligible batteries already reduce the grid export and stay reserved. Battery discharge is excluded as a source of PV energy. Existing eligible charging is added back once, preventing charge/normal oscillation as export falls when the battery starts charging.

Batteries are considered in configured order. Each selected battery reserves its maximum charge power, preventing multiple batteries from claiming the same surplus. Starting requires another 200 W of headroom; an already charging battery requires 100 W. No time delay is imposed on stopping. At full SoC, the configured maximum, insufficient surplus or invalid measurements, the device receives `normal` when PV control owns the mode.

Existing external control, tariff grid charge, discharge hold and tariff grid discharge retain priority. HEMS dimming can still change charging to `hold`. The existing mode dispatcher handles supported modes, per-device SoC checks, command deduplication and shutdown restoration. During PV control, failures on one device do not prevent restoration attempts on the other devices. A pending flag retains responsibility for restoring `normal` after a partially failed write cycle; failed writes are retried.

The advertised maximum must match or exceed what the device actually draws in `charge`. Sampling intervals, device response time, unrelated load changes and asynchronous EV control can still cause transient grid import. `normal` returns control to the device EMS; it does not prohibit autonomous charging. This prototype is conservative mode switching, not continuous watt regulation.

The current usage decorator exposes `BatteryPowerLimiter` only when both existing maximum charge and discharge powers are configured. For templates such as sonnen, a charge setpoint alone is not enough to expose this capability. Inspect the rendered configuration before a real test. No configuration is modified by this branch.

## Validation

```sh
go test ./core/... ./api/... -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build ./core
```

Tests cover surplus thresholds and hysteresis, house priority, charging feedback, exclusion of battery discharge, full and limited SoC, invalid and missing measurements, missing capabilities, multiple batteries, mode deduplication, external/tariff priority, HEMS dimming, failed writes and partial-write restoration.

The ARM command above compiles the core package; it does not create an executable or run tests on ARM hardware.

## Build a complete Pi executable

Use Go matching `go.mod` (currently Go 1.27 or newer), Node matching `package.json` (currently Node 26 or newer), Vite+ and GNU Make on a separate build machine. Build the unmodified frontend because the executable embeds `dist`:

```sh
git clone --branch feature/active-pv-battery-charging https://github.com/beyond0272/evcc.git
cd evcc
make install-ui
make ui
go test ./core/... ./api/... -count=1
mkdir -p build
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 \
  go build -tags=release -trimpath \
  -ldflags="-X github.com/evcc-io/evcc/util.Version=active-pv-prototype -X github.com/evcc-io/evcc/util.Commit=$(git rev-parse --short HEAD) -s -w" \
  -o build/evcc-test-linux-armv6 .
sha256sum build/evcc-test-linux-armv6
```

ARMv6 follows the upstream release target and runs on a Pi 3 with 32-bit userspace. For 64-bit userspace, use `GOARCH=arm64`, omit `GOARM`, and choose an `arm64` output name. Check the OS userspace architecture, not only the CPU model.

## Controlled hardware test

No installation is performed as part of this branch. For a later test, retain the official executable and service definition. Determine the existing service arguments and installed evcc version first. This feature adds no migrations, but a newer upstream base can contain its own database migrations and normal runtime writes. Use a separate copy of the production database and configuration for the prototype; do not assume an older official version can read a database touched by this upstream version.

Stop the official instance before launching the prototype with the copied files and core logging at debug level. Start with the EV unplugged and observe PV, grid power, battery SoC and actual charging power. Verify surplus start, loss-of-surplus stop, the SoC limit and process shutdown. A graceful shutdown uses upstream's existing return-to-normal path. A process crash, power loss or unreachable battery cannot guarantee a reset command. Confirm device mode before returning to the official instance.

Terminate the prototype before restarting the original service. Never run both against the same hardware simultaneously.
