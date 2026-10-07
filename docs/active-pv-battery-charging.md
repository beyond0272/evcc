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
