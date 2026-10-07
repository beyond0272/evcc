package core

import (
	"math"
	"slices"

	"github.com/evcc-io/evcc/api"
)

func finiteBatteryPVValue(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// activePVBatteryCharging reserves complete charge power because BatteryController has no watt setpoint.
func (site *Site) activePVBatteryCharging(state siteState) map[string]bool {
	if !site.batteryConfigured() || site.gridMeter == nil || len(site.pvMeters) == 0 || !finiteBatteryPVValue(state.gridPower) {
		return nil
	}

	var pv float64
	for _, dev := range site.pvMeters {
		power, err := dev.Instance().CurrentPower()
		if err != nil || !finiteBatteryPVValue(power) {
			site.log.WARN.Printf("active PV battery charging: invalid PV measurement, returning to normal")
			return nil
		}
		pv += max(0, power)
	}

	type candidate struct {
		name              string
		power, soc, limit float64
	}
	var candidates []candidate
	available := -state.gridPower
	for _, dev := range site.batteryMeters {
		meter := dev.Instance()
		power, err := meter.CurrentPower()
		if err != nil || !finiteBatteryPVValue(power) {
			site.log.WARN.Printf("active PV battery charging: battery %s power unavailable, returning to normal", dev.Config().Name)
			return nil
		}
		// Discharged energy is not PV; charging by other batteries remains reserved.
		available -= max(0, power)
		ctrl, controllable := api.Cap[api.BatteryController](meter)
		bat, hasSoc := api.Cap[api.Battery](meter)
		limiter, hasLimit := api.Cap[api.BatteryPowerLimiter](meter)
		if !controllable || !hasSoc || !hasLimit || !slices.Contains(ctrl.BatteryModes(), api.BatteryCharge) || !slices.Contains(ctrl.BatteryModes(), api.BatteryNormal) {
			site.log.DEBUG.Printf("active PV battery charging: battery %s skipped (requires charge/normal, soc and power limit)", dev.Config().Name)
			continue
		}
		soc, err := bat.Soc()
		limit, _ := limiter.GetPowerLimits()
		maxSoc := 100.0
		if sl, ok := api.Cap[api.BatterySocLimiter](meter); ok {
			_, upper := sl.GetSocLimits()
			if !finiteBatteryPVValue(upper) || upper < 0 || upper > 100 {
				continue
			}
			if upper > 0 {
				maxSoc = upper
			}
		}
		if err != nil || !finiteBatteryPVValue(soc) || soc < 0 || soc >= maxSoc || !finiteBatteryPVValue(limit) || limit <= 0 {
			site.log.DEBUG.Printf("active PV battery charging: battery %s normal (soc=%.1f%% max=%.1f%% limit=%.0fW error=%v)", dev.Config().Name, soc, maxSoc, limit, err)
			continue
		}
		available += max(0, -power)
		candidates = append(candidates, candidate{dev.Config().Name, power, soc, limit})
	}
	residual := site.GetResidualPower()
	if !finiteBatteryPVValue(residual) || !finiteBatteryPVValue(available) || !finiteBatteryPVValue(pv) {
		return nil
	}
	available = min(pv, available) - max(0, residual)
	res := make(map[string]bool)
	for _, bat := range candidates {
		margin := 200.0
		if site.batteryModeApplied[bat.name] == api.BatteryCharge {
			margin = 100
		}
		charge := available >= bat.limit+margin && pv > 0
		site.log.DEBUG.Printf("active PV battery charging: battery=%s pv=%.0fW grid=%.0fW available=%.0fW power=%.0fW soc=%.1f%% limit=%.0fW charge=%t", bat.name, pv, state.gridPower, available, bat.power, bat.soc, bat.limit, charge)
		if charge {
			res[bat.name] = true
			available -= bat.limit
		}
	}
	return res
}
