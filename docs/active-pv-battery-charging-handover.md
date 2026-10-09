# Active PV Battery Charging – Development Handover

Status: Work in progress – NOT approved for production.

## Goal

Extend evcc with dynamic charging control for a sonnenBatterie.

Use otherwise exported PV energy to charge the home battery,
while respecting native sonnen control, EV charging priority,
battery limits and grid import.

Development repository:
beyond0272/evcc

Branch:
feature/active-pv-battery-charging

## Existing implementation

- Optional BatteryChargePowerController interface
- Dynamic charge power support in the meter plugin
- sonnenBatterie API charge setpoint integration
- Configurable PV charging start threshold
- Default start threshold: 500 W
- Persistent threshold setting in evcc database
- WebUI setting for the start threshold
- German and English translations
- Grid reserve: 100 W
- Dynamic adjustment of battery charging power
- Battery charge power and SoC limitations
- Initial handling of native sonnen charging
- Error rollback and recovery lockout
- Unit tests for dynamic charging and failure cases

The existing implementation is a prototype.
Its control logic must be revised before deployment.

## Revised control philosophy

The primary requirement is to detect the actual operating
state of the battery BEFORE attempting to control it.

Do not assume that manual mode means evcc owns control.

### Step 1: Determine battery state

Read the current sonnen operating mode and charging power.

If battery is in manual mode:
- Determine whether evcc owns the manual control.
- If evcc owns control, evaluate and adjust charging.
- If ownership is unknown or belongs to another controller,
  do not interfere.

If battery is in automatic mode:
- If sonnen is already charging, do not interfere.
- If sonnen is not charging, evaluate whether evcc should start.

### Step 2: Evaluate new charging

Only take over if:
- Battery has remaining charging capacity.
- Battery state and measurements are valid.
- Grid export exceeds the configured start threshold.
- Native sonnen charging is not active.
- EV charging priority is preserved.

Initial charge power:
Available grid export minus the configured grid reserve,
limited by the permitted battery charging power.

### Step 3: Control active evcc charging

If importing from grid:
- Reduce charge power or stop charging.

If grid flow is balanced:
- Keep current charge power.

If exporting to grid:
- Increase charge power within permitted limits.

Account for battery charging power already in progress
when calculating the new setpoint.

### Step 4: Stop and return control

Return control to native sonnen operation when:
- Charging is no longer useful.
- Battery reaches its applicable charging limit.
- Measurement quality becomes insufficient.
- A relevant error prevents safe control.

Return to the appropriate pre-control operating mode.
Do not unconditionally overwrite third-party manual control.

## Important decisions

1. Native sonnen charging has priority.
   Do not take over merely because native charging appears
   to underutilize available PV power.

2. Configurable PV charging start threshold:
   default 500 W.

3. Preserve a 100 W grid reserve.

4. During active evcc charging, dynamically regulate
   the charging power using measured grid flow.

5. No arbitrary charge power reduction above 80% SoC.
   Respect the battery management system and verified
   manufacturer limits.

6. Battery specifications identified via local WebUI:
   - sonnenBatterie 10
   - 5.5 kWh, one battery module
   - Power Unit sB10s sI1 9010 IP30
   - Inverter maximum power: 3400 W

7. Retain the conservative configured 3300 W limit
   until the relevant capabilities are verified.

## Critical open issues

- Verify read-only sonnen operating-mode API access.
- Verify actual operating mode and ownership detection.
- Handle evcc restart/crash during manual battery control.
- Ensure safe recovery without overwriting external control.
- Validate effects of repeated manual mode API requests.
- Prevent grid import caused by stale measurements.
- Respect simultaneous vehicle charging and house loads.
- Confirm behaviour during communication failures.
- Complete translation support for other languages.
- Add tests for the revised state machine.
- Re-run Go tests, frontend type checks and frontend build.
- Verify on test setup before any production deployment.

## Previous development tests

Previously passed:
- TestDynamicPVBatteryCharging
- TestDynamicPVBatteryChargingRollback
- TestDynamicPVRespectsNativeCharging
- TestDynamicPVToGridCharging
- TestDynamicPVRecoveryLockout

Go core/API/meter/server tests passed with template-test
exclusion. Frontend type check and build also passed.

These results apply to the earlier implementation,
not to the proposed revised state machine.

## Environment separation

Build Raspberry Pi:
- Source code, tests and compilation.
- No production sonnen API token.

Production Raspberry Pi:
- Existing evcc installation and real sonnen API token.
- Must remain unchanged during development.

Do not commit API tokens, production database contents
or local build artifacts.

## Next development milestone

Implement reliable, read-only battery operating-mode
detection and controller ownership tracking.

Then refactor the existing dynamic PV charging prototype
into an explicit, testable state machine.

Only deploy after safety checks and tests pass.
