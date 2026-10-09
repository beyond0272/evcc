package core

import (
	"math"
	"slices"

	"github.com/evcc-io/evcc/api"
)

// dynamicPVBatteryCharging calculates PV-only watt setpoints.
// The start threshold is independent of the battery's maximum power.
func (site *Site) dynamicPVBatteryCharging(state siteState) map[string]float64 {
	result := make(map[string]float64)
	if site.batteryPVRecoveryRequired {
		site.log.WARN.Println("active PV battery charging: recovery pending")
		return result
	}

	if !site.batteryConfigured() || site.gridMeter == nil ||
		len(site.pvMeters) == 0 ||
		!finiteBatteryPVValue(state.gridPower) ||
		!finiteBatteryPVValue(state.pvPower) ||
		state.pvPower <= 0 {
		return result
	}

	// Recheck the grid meter: never control from an unavailable reading.
	gridNow, err := site.gridMeter.Instance().CurrentPower()
	if err != nil || !finiteBatteryPVValue(gridNow) {
		site.log.WARN.Println("active PV battery charging: grid reading unavailable")
		return result
	}

	// Conservative if the two grid readings differ.
	available := -math.Max(gridNow, state.gridPower)

	type candidate struct {
		name  string
		limit float64
	}
	var candidates []candidate

	for _, dev := range site.batteryMeters {
		meter := dev.Instance()
		power, err := meter.CurrentPower()
		if err != nil || !finiteBatteryPVValue(power) {
			site.log.WARN.Println("active PV battery charging: battery power unavailable")
			return map[string]float64{}
		}

		// Battery discharge must never masquerade as PV surplus.
		available -= math.Max(0, power)

		ctrl, hasCtrl := api.Cap[api.BatteryController](meter)
		_, hasSetter := api.Cap[api.BatteryChargePowerController](meter)
		bat, hasSoc := api.Cap[api.Battery](meter)
		limiter, hasLimit := api.Cap[api.BatteryPowerLimiter](meter)

		if !hasCtrl || !hasSetter || !hasSoc || !hasLimit {
			continue
		}
		if !slices.Contains(ctrl.BatteryModes(), api.BatteryNormal) ||
			!slices.Contains(ctrl.BatteryModes(), api.BatteryCharge) {
			continue
		}

		soc, err := bat.Soc()
		if err != nil || !finiteBatteryPVValue(soc) || soc < 0 || soc >= 100 {
			continue
		}

		maxSoc := 100.0
		if limits, ok := api.Cap[api.BatterySocLimiter](meter); ok {
			_, upper := limits.GetSocLimits()
			if !finiteBatteryPVValue(upper) || upper < 0 || upper > 100 {
				continue
			}
			if upper > 0 {
				maxSoc = upper
			}
		}
		if soc >= maxSoc {
			continue
		}

		limit, _ := limiter.GetPowerLimits()
		if !finiteBatteryPVValue(limit) || limit <= 0 {
			continue
		}

		// Respect independent charging if the battery already uses PV surplus.
		name := dev.Config().Name
		evccControls := site.batteryPVChargeActive &&
			site.batteryModeApplied[name] == api.BatteryCharge

		if power < -100 && !evccControls {
			if site.batteryPVNativeSurplus == nil {
				site.batteryPVNativeSurplus = make(map[string]int)
			}

			exported := math.Max(0, -math.Max(gridNow, state.gridPower))

			if exported < site.GetBatteryPVStartPower() {
				site.batteryPVNativeSurplus[name] = 0
				site.log.DEBUG.Printf(
					"active PV battery charging: %s charging independently %.0fW, export %.0fW",
					name, -power, exported,
				)
				continue
			}

			site.batteryPVNativeSurplus[name]++

			if site.batteryPVNativeSurplus[name] < 2 {
				site.log.DEBUG.Printf(
					"active PV battery charging: %s observing unused surplus %.0fW",
					name, exported,
				)
				continue
			}
		} else {
			delete(site.batteryPVNativeSurplus, name)
		}

		// Add back this controllable battery's present charge consumption.
		available += math.Max(0, -power)
		candidates = append(candidates, candidate{dev.Config().Name, limit})
	}

	reserve := site.GetResidualPower()
	if !finiteBatteryPVValue(available) || !finiteBatteryPVValue(reserve) {
		return result
	}
	reserve = math.Max(0, reserve)

	for _, bat := range candidates {
		surplus := math.Min(state.pvPower, available)

		continuing := site.batteryPVChargeActive &&
			site.batteryModeApplied[bat.name] == api.BatteryCharge

		if !continuing && surplus < site.GetBatteryPVStartPower() {
			continue
		}

		power := math.Floor(math.Min(bat.limit, surplus-reserve))
		if power <= 0 {
			continue
		}

		result[bat.name] = power
		available -= power

		site.log.INFO.Printf(
			"active PV battery charging: battery=%s pv=%.0fW grid=%.0fW surplus=%.0fW target=%.0fW continuing=%t",
			bat.name, state.pvPower, gridNow, surplus, power, continuing,
		)
	}

	return result
}
