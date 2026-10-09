package core

import (
	"math"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/batterycontrol"
	"github.com/evcc-io/evcc/hems/hems"
)

func (site *Site) updatePVBatteryControl(state siteState, valid, allowed bool) {
	site.batteryPVMu.Lock()
	defer site.batteryPVMu.Unlock()
	if site.batteryPVStopped {
		return
	}
	if site.batteryPVSessions == nil {
		site.batteryPVSessions = make(map[string]*batterycontrol.Session)
	}
	if dimmed := hems.Dimmed(site.hems); dimmed != nil && *dimmed {
		allowed = false
	}
	valid = valid && site.gridMeter != nil && len(site.pvMeters) > 0 && finiteBatteryPVValue(state.gridPower) && finiteBatteryPVValue(state.pvPower)
	grid, pv := state.gridPower, state.pvPower
	if valid {
		current, err := site.gridMeter.Instance().CurrentPower()
		valid = err == nil && finiteBatteryPVValue(current)
		grid = math.Max(grid, current)
		var measuredPV float64
		for _, dev := range site.pvMeters {
			power, err := dev.Instance().CurrentPower()
			valid = valid && err == nil && finiteBatteryPVValue(power)
			measuredPV += max(0, power)
		}
		pv = min(pv, measuredPV)
	}
	discharges := make(map[string]float64)
	charges := make(map[string]float64)
	var totalDischarge float64
	for _, dev := range site.batteryMeters {
		power, err := dev.Instance().CurrentPower()
		valid = valid && err == nil && finiteBatteryPVValue(power)
		discharges[dev.Config().Name] = max(0, power)
		charges[dev.Config().Name] = max(0, -power)
		totalDischarge += max(0, power)
	}
	status := make(map[string]map[string]any)
	var allocated float64
	for _, dev := range site.batteryMeters {
		meter, name := dev.Instance(), dev.Config().Name
		reader, hasReader := api.Cap[api.BatteryControlStateReader](meter)
		ctrl, hasControl := api.Cap[api.BatteryPVLeaseController](meter)
		if hasControl {
			reader, hasReader = ctrl, true
		}
		if !hasReader && !api.HasCap[api.BatteryChargePowerController](meter) {
			continue
		}
		session := site.batteryPVSessions[name]
		if session == nil {
			session = new(batterycontrol.Session)
			site.batteryPVSessions[name] = session
		}
		maxSoc, maxPower := 100.0, 0.0
		if limiter, ok := api.Cap[api.BatterySocLimiter](meter); ok {
			_, maxSoc = limiter.GetSocLimits()
			if maxSoc == 0 {
				maxSoc = 100
			}
		}
		if limiter, ok := api.Cap[api.BatteryPowerLimiter](meter); ok {
			maxPower, _ = limiter.GetPowerLimits()
		}
		before, reason := session.Phase, session.Reason
		input := batterycontrol.Input{
			Valid: valid, Allowed: allowed, Grid: grid + totalDischarge - discharges[name] + allocated,
			PV: max(0, pv-allocated), Reserve: max(100, site.GetResidualPower()),
			StartPower: site.GetBatteryPVStartPower(), MaxPower: maxPower, MaxSoc: maxSoc,
		}
		if err := session.Step(reader, ctrl, input); err != nil {
			site.log.ERROR.Printf("active PV battery charging: %s: %v", name, err)
		}
		allocated += max(0, session.Power()-charges[name])
		if before != session.Phase || reason != session.Reason {
			site.log.INFO.Printf("active PV battery charging: battery=%s state=%s reason=%s", name, session.Phase, session.Reason)
		}
		site.log.DEBUG.Printf("active PV battery charging: battery=%s grid=%.0fW pv=%.0fW target=%.0fW state=%s actualMode=%s nativeMode=%s actualPower=%.0fW", name, grid, pv, session.Power(), session.Phase, session.ObservedMode, session.ObservedNativeMode, session.ObservedPower)
		status[name] = map[string]any{"state": session.Phase, "reason": session.Reason, "power": session.Power(), "actualMode": session.ObservedMode, "nativeMode": session.ObservedNativeMode, "actualPower": session.ObservedPower}
	}
	site.publish("batteryPVControl", status)
}

func (site *Site) stopPVBatteryControl() {
	site.batteryPVMu.Lock()
	defer site.batteryPVMu.Unlock()
	site.batteryPVStopped = true
	for _, dev := range site.batteryMeters {
		name := dev.Config().Name
		if session := site.batteryPVSessions[name]; session != nil {
			ctrl, _ := api.Cap[api.BatteryPVLeaseController](dev.Instance())
			if err := session.Stop(ctrl, "evcc shutdown"); err != nil {
				site.log.ERROR.Printf("active PV battery charging: %s shutdown release: %v; device lease must expire", name, err)
			}
		}
	}
}
