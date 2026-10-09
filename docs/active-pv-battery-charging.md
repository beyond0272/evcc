# Active PV Battery Charging: ownership-aware development version

Status: development only. Based on `e04a3b5e2`; not approved for production.

## What works and what remains blocked

The sonnen integration reads the actual mode, charging flag, power and SoC from
one uncached `/api/v2/status` response. It does not infer ownership from a previous
`charge` command, a local boolean, or a persisted setpoint. Mode 1 is manual;
2 and 10 are automatic. Missing/unknown modes and non-OnGrid operation are
ineligible. Native charging, including a negative battery power observation,
is never taken over, regardless of how long additional export persists.

**The existing sonnen write API has no verified ownership fencing and independent
command-expiry mechanism in this implementation. Consequently this version only
observes sonnen and does not initiate active PV charging.** The configurable
500 W threshold, persistence and UI remain. A UI notice explains the restriction.
The raw charge-power interface remains available for compatibility, but is not
used as an unsafe fallback by the PV controller.

Other site-level mode commands are also suppressed for devices using the new
observation/lease interfaces: tariff grid charging, discharge holds, external
mode requests and shutdown must not bypass the ownership check. Other battery
integrations retain their existing behavior. This deliberate restriction is
why this development version must not replace the production installation yet.
`batteryMode` remains evcc's requested mode, not proof of the actual device mode.
Use `batteryPVControl` diagnostics / logs for the observed mode and decision.

## State machine

`core/batterycontrol.Session` separates observation from site arbitration and
hardware access:

| State | Behavior |
| --- | --- |
| observing | Valid automatic idle battery; wait for sufficient export / permission. |
| native-charging | Automatic battery already charging: observe, never take over. |
| foreign-or-unknown-control | Manual/unknown mode without this process's verified lease: no writes. |
| safe-control-unavailable | Missing safe controller or invalid observation: no acquisition. |
| active | Device confirms this process's unexpired lease; adjust and renew. |
| recovering | Command/release failed: retry only conditional release; never resume charging in this cycle. |

`BatteryPVLeaseController` requires atomic acquisition that rejects native
charging/manual mode, device-enforced owner checks on every write, and independent
expiry (90 seconds requested). Expiry must stop forced charging and restore the
previous automatic mode even after evcc host power loss. A new process never
adopts an old ID. A delayed release for an old ID cannot affect a new owner.
A mode-write/setpoint-write HTTP sequence does not implement this contract.
No real sonnen adapter claims to implement it yet. Tests use an enforcing fake;
they do not establish that sonnen hardware has such a watchdog.

For a device meeting that contract:

- Start at configured export threshold (default 500 W), before deducting reserve.
- Reserve at least 100 W; larger configured site residual power is respected.
- Exclude battery discharge from the PV budget and account for other batteries.
- Initial 500 W export therefore requests at most 400 W charging.
- While active, calculate measured charging power + usable export - reserve,
  capped by PV production and the configured charge-power limit.
- Between zero grid import and the export reserve, hold the previous setting.
- Any valid positive measured grid import releases control immediately in that
  control cycle; no smaller charging command is sent first. Zero grid power keeps
  the current request. Falling below 500 W export alone does not stop charging.
- Charge-power limits cap the request only; they never set the start threshold.
  Budget reservations for other batteries are not measured grid import.
- SoC limit, invalid measurements, missing PV, HEMS limits, tariff control,
  external requests or fast EV charging end the session through fenced release.
- No arbitrary 80% taper. The existing sonnen charge limit remains 3300 W.

The state reader rejects missing measurements and non-finite values. Repeated
identical device timestamps do not refresh sample age (10-second maximum for
control). The timestamp is used as a change marker, not parsed as a trusted UTC
clock: an old response on the very first read cannot be dated reliably. This is
an additional reason that observation alone must never establish ownership.

## Agreed next ownership revision (not implemented yet)

The next ownership design will use a durable write-ahead action journal and
startup reconciliation, plus a persistent GUI conflict lock requiring user
interaction. Unknown manual operation must not be overwritten; automatic native
charging must remain untouched. An unexpected return to automatic operation ends
our session and is not itself evidence of a foreign manual controller. Measured
charge power differing from a requested setpoint is not proof of a conflict.

This agreement supersedes the earlier requirement to solve all ownership through
a hardware lease before proceeding. The current code still uses the lease guard;
the journal and GUI confirmation flow have not been implemented by the threshold
correction. A journal enables recovery after restart but cannot issue a stop while
the evcc host is powered off. That limitation remains separate and explicit.

## Verification and next hardware requirement

Verified on 9 October 2026:

- `go test ./core/... ./api/... ./server/... -count=1`: passed.
- `go test ./meter -skip '^TestTemplates$' -count=1`: passed.
- Targeted sonnen template and HTTP mapping tests: passed.
- All 23 frontend test files / 227 tests: passed with one worker and a workspace
  temporary directory. The initial parallel run failed loading temporary module
  files (`ENOENT`) in the Windows sandbox; the serial retry passed unchanged.
- `vue-tsc --noEmit` and `vp build`: passed.
- Linux ARM64 release cross-compilation: passed. Not executed on a Pi.



Tests cover native charging across repeated cycles, manual/unknown mode on
startup, old process ownership, 499/500 W boundary, continuation below threshold,
balanced grid, import, power/SoC caps, faults, missing acknowledgements, recovery
lockout, owner replacement, release, and simulated crash expiry. Template HTTP
tests exercise actual status mapping without any real battery or token.

To enable real sonnen control, obtain firmware-specific primary documentation
for conditional ownership and automatic expiry, or design an independently
powered local controller that enforces these semantics and excludes bypassing
writers. A second process on the same Pi alone does not cover Pi power loss.
Do not substitute an evcc-local owner flag for this missing hardware guarantee.

Reference: manufacturer JSON API v2 status document, mirrored at
https://doc.musicaloris.de/sonnenBatterie_JSON_API_v2_status.pdf . It documents
status fields; it does not establish a fenced ownership/expiry protocol.

Build/test commands (development machine only):

```sh
go test ./core/... ./api/... ./server/... -count=1
go test ./meter -skip '^TestTemplates$' -count=1
go test ./meter -run '^TestTemplates/sonnenbatterie' -count=1
vp exec vue-tsc --noEmit
vp test run --maxWorkers=1 --no-file-parallelism
vp build
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags=release -trimpath \
  -ldflags="-X github.com/evcc-io/evcc/util.Version=0.316.2-pv-ownership-dev -X github.com/evcc-io/evcc/util.Commit=$(git rev-parse --short HEAD)" \
  -o build/evcc-pv-observe-linux-arm64 .
```

The general meter template matrix has platform-dependent connection-error
assertions on Windows; the targeted sonnen template check is separate.
Building an artifact does not authorize production installation. Keep the token
and production database on the production Pi.

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
