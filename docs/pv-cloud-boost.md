# PV cloud support for fixed-phase vehicle charging

Development update, 10 October 2026. No production deployment.

## Policy

This path applies when battery boost is enabled and the loadpoint uses fixed
phases (Bluey: three). Automatic phase-switching boost retains its existing
implementation. The EV minimum is derived from effective minimum current and
active phases, not hard-coded to 4200 W. At 6 A, 230 V and three phases it is
4140 W. The user explicitly selected the calculated minimum.

- Start and restart require enough PV alone for the minimum EV charging power.
  Existing enable delay remains. Battery support cannot pass the start gate.
- While actually charging, use PV plus the permitted battery discharge budget.
- Preserve the signed PV balance after house demand, other EV consumption and
  reserve. Negative PV balance first consumes battery capacity for the house.
- Pause in the current control cycle when the combined budget is below the EV
  minimum. The normal disable delay, buffer-start setting and minimum-current
  overrides must not keep charging from a planned grid contribution.
- At or below the configured battery-boost SoC limit (currently 50%), hold boost.
  Continue on PV alone if sufficient; otherwise pause. Existing hold semantics
  prevent oscillation until disconnect or an explicitly relaxed SoC limit.
- Respect known discharge limits, maximum EV current and downstream circuit
  limits. Missing/non-finite discharge limits mean no planned battery support.
- An unavailable current SoC reading contributes no discharge budget, even when
  the dashboard retains the last measured SoC.
- This changes the PV branch of smart charging. Explicit fast mode, scheduled
  charging and cheap-tariff overrides keep their existing priorities. It does not
  promise zero instantaneous import during physical response delays.

## Accounting

The measured site balance adds battery discharge back to grid power. Subtracting
that balance from the current measured EV power reconstructs the PV contribution
available to this EV, without counting existing battery discharge as solar.
For boost, auxiliary demand and other EV power remain in the balance rather than
being treated as reclaimable power.

Conceptually (excluding DC adjustments):

```
PV balance = PV production - house - other EVs - reserve
running EV budget = PV balance + allowed battery discharge
```

Do not add measured battery discharge again. Do not clamp a negative PV balance
to zero before adding battery support: that would allocate the house's portion
to the EV too.

The reference weather sequence, without reserve/losses and with 3300 W allowed
discharge, is 4500+3300=7800, 3000+3300=6300, 1500+3300=4800 W, then pause at
500+3300=3800 W. These are power budgets; actual commands follow supported current
steps, so a charger restricted to whole amps may draw less.

## Separate home-battery start setting

The home-battery PV charge start value remains configurable in the GUI and
persisted in evcc settings. 500 W is only its default. The car's minimum power
and the battery's maximum charge/discharge power do not replace this setting.
Tests cover 250, 500, 750 and 1500 W start settings and changing the setting while
battery charging is already active.

## Tests and remaining integration

`go test ./core/... ./api/... -count=1` passes. New tests cover the weather
sequence, start/restart gate, enable delay, exact SoC boundary, PV-only continuation,
house priority, independence from actual battery discharge, reserve, missing
limits/SoC, immediate pause and EV current cap.

This budget controller does not send new manual sonnen discharge commands. It
relies on the battery supplying demand through its native regulation and on an
accurate allowed discharge limit. Simulated tests do not verify the physical
battery response. The action journal and GUI confirmation are implemented separately; see
[PV battery control](active-pv-battery-charging.md). Manual or unobserved batteries
contribute no discharge capacity and their charging demand is not reclaimable PV.
Active EV boost returns owned PV charging to automatic mode. No token or production
database was accessed.
