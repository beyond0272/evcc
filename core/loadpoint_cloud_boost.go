package core

import "math"

func (lp *Loadpoint) usesCloudBoost() bool {
	return lp.GetBatteryBoost() != boostDisabled && (!lp.hasPhaseSwitching() || lp.phasesConfigured != 0)
}

// pvCloudBoostCurrent keeps fixed-phase charging within a PV + battery budget.
// sitePower includes the current EV load and adds measured battery discharge
// back to grid power. Subtracting it from measured EV power therefore gives
// PV minus all other consumption and reserve, independent of actual discharge.
func (lp *Loadpoint) pvCloudBoostCurrent(sitePower float64) float64 {
	minCurrent, maxCurrent := lp.effectiveMinCurrent(), lp.effectiveMaxCurrent()
	phases := lp.ActivePhases()
	minimum := currentToPower(minCurrent, phases)
	if !finiteBatteryPVValue(sitePower) || !finiteBatteryPVValue(lp.chargePower) || minimum <= 0 {
		lp.resetPVTimer()
		return 0
	}

	// A negative residual setting must not authorize planned grid imports
	// in this PV-only start / PV+battery continuation policy.
	pvBudget := lp.chargePower - sitePower + min(0, lp.site.GetResidualPower())
	running := lp.enabled && lp.charging()
	var support float64
	soc := lp.site.GetBatterySoc()
	limit := lp.GetBatteryBoostLimit()
	if lp.GetBatteryBoost() != boostHold && finiteBatteryPVValue(soc) && soc <= float64(limit) {
		lp.log.DEBUG.Printf("battery boost hold: soc %.1f%% <= %d%%", soc, limit)
		lp.setBatteryBoost(boostHold)
	}
	if running && lp.GetBatteryBoost() != boostHold && finiteBatteryPVValue(soc) && soc > float64(limit) {
		if power := lp.site.GetBatteryMaxDischargePower(); power != nil && finiteBatteryPVValue(*power) {
			support = math.Max(0, *power)
		}
		lp.setBatteryBoost(boostContinue)
	}

	// Do not clamp pvBudget before adding support: a negative balance is the
	// house's unmet demand and must consume battery capacity before the EV does.
	budget := pvBudget + support
	lp.log.DEBUG.Printf("cloud boost: pv budget=%.0fW battery limit=%.0fW total=%.0fW minimum=%.0fW running=%t", pvBudget, support, budget, minimum, running)
	if budget < minimum {
		// No disable delay or minimum-current override may plan grid imports.
		lp.resetPVTimer()
		return 0
	}
	if !lp.enabled {
		// support is zero before charging, so the start gate is PV-only.
		if lp.pvTimer.IsZero() {
			lp.pvTimer = lp.clock.Now()
		}
		lp.publishTimer(pvTimer, lp.GetEnableDelay(), pvEnable)
		if lp.clock.Since(lp.pvTimer) < lp.GetEnableDelay() {
			return 0
		}
		lp.resetPVTimer()
		return minCurrent
	}
	lp.resetPVTimer()
	return min(maxCurrent, powerToCurrent(budget, phases))
}
