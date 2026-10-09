package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/batterycontrol"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/require"
)

type observedPVBattery struct {
	state  api.BatteryControlState
	writes []api.BatteryMode
}

func (b *observedPVBattery) CurrentPower() (float64, error) { return b.state.Power, nil }
func (b *observedPVBattery) Soc() (float64, error)          { return b.state.Soc, nil }
func (b *observedPVBattery) BatteryModes() []api.BatteryMode {
	return []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}
}
func (b *observedPVBattery) SetBatteryMode(m api.BatteryMode) error {
	b.writes = append(b.writes, m)
	return nil
}
func (b *observedPVBattery) SetBatteryChargePower(p float64) error {
	panic("unfenced charge must not run")
}
func (b *observedPVBattery) BatteryControlState() (api.BatteryControlState, error) {
	s := b.state
	s.ObservedAt = time.Now()
	return s, nil
}
func guardedSite(b *observedPVBattery) *Site {
	grid := &observedPVBattery{state: api.BatteryControlState{Power: -1000}}
	pv := &observedPVBattery{state: api.BatteryControlState{Power: 2000}}
	s := NewSite()
	s.log = util.NewLogger("test")
	s.gridMeter = config.NewStaticDevice[api.Meter](config.Named{Name: "grid"}, grid)
	s.pvMeters = []config.Device[api.Meter]{config.NewStaticDevice[api.Meter](config.Named{Name: "pv"}, pv)}
	s.batteryMeters = []config.Device[api.Meter]{config.NewStaticDevice[api.Meter](config.Named{Name: "battery"}, b)}
	return s
}
func TestDynamicPVRespectsNativeCharging(t *testing.T) {
	b := &observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingAuto, Soc: 50, Power: -900}}
	s := guardedSite(b)
	for range 5 {
		s.updatePVBatteryControl(siteState{gridPower: -1000, pvPower: 2000}, true, true)
		s.updateBatteryMode(false, false, api.Rate{})
	}
	require.Empty(t, b.writes)
	require.Equal(t, batterycontrol.Native, s.batteryPVSessions["battery"].Phase)
}
func TestDynamicPVManualRestartAndShutdown(t *testing.T) {
	b := &observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingManual, Soc: 50, Power: -900}}
	s := guardedSite(b)
	s.updatePVBatteryControl(siteState{gridPower: -1000, pvPower: 2000}, true, true)
	s.batteryMode = api.BatteryCharge
	s.stopPVBatteryControl()
	require.NoError(t, s.applyBatteryMode(api.BatteryNormal))
	require.Empty(t, b.writes)
	require.Equal(t, batterycontrol.Foreign, s.batteryPVSessions["battery"].Phase)
}
func TestDynamicPVNoUnsafeFallback(t *testing.T) {
	b := &observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingAuto, Soc: 50}}
	s := guardedSite(b)
	s.updatePVBatteryControl(siteState{gridPower: -1000, pvPower: 2000}, true, true)
	s.updateBatteryMode(true, false, api.Rate{})
	require.Empty(t, b.writes)
	require.Equal(t, batterycontrol.Blocked, s.batteryPVSessions["battery"].Phase)
}

func TestDynamicPVShutdownBlocksLaterUpdates(t *testing.T) {
	b := &observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingAuto, Soc: 50}}
	s := guardedSite(b)
	s.stopPVBatteryControl()
	s.updatePVBatteryControl(siteState{gridPower: -1000, pvPower: 2000}, true, true)
	require.Empty(t, s.batteryPVSessions)
	require.Empty(t, b.writes)
}

// This fake enforces the future adapter contract; it is not a sonnen emulator.
type leasedPVBattery struct {
	observedPVBattery
	target float64
}

func (b *leasedPVBattery) GetPowerLimits() (float64, float64) { return 3300, 3300 }
func (b *leasedPVBattery) AcquireBatteryPVControl(id string, power float64, ttl time.Duration) error {
	if b.state.Mode != api.BatteryOperatingAuto || b.state.Power < 0 || b.state.NativeCharging || b.state.LeaseID != "" {
		return fmt.Errorf("foreign control")
	}
	b.state.Mode = api.BatteryOperatingManual
	b.state.LeaseID = id
	return b.RenewBatteryPVControl(id, power, ttl)
}
func (b *leasedPVBattery) RenewBatteryPVControl(id string, power float64, ttl time.Duration) error {
	if b.state.LeaseID != id {
		return fmt.Errorf("foreign control")
	}
	b.state.LeaseUntil = time.Now().Add(ttl)
	b.target = power
	return nil
}
func (b *leasedPVBattery) ReleaseBatteryPVControl(id string) error {
	if b.state.LeaseID == id {
		b.state.LeaseID = ""
		b.state.Mode = api.BatteryOperatingAuto
		b.target = 0
	}
	return nil
}
func TestDynamicPVAllocatesMeasuredBudgetAndReleases(t *testing.T) {
	a := &leasedPVBattery{observedPVBattery: observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingAuto, Soc: 50}}}
	b := &leasedPVBattery{observedPVBattery: observedPVBattery{state: api.BatteryControlState{Mode: api.BatteryOperatingAuto, Soc: 50}}}
	s := guardedSite(&a.observedPVBattery)
	s.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](config.Named{Name: "a"}, a),
		config.NewStaticDevice[api.Meter](config.Named{Name: "b"}, b),
	}
	state := siteState{gridPower: -1000, pvPower: 2000}
	s.updatePVBatteryControl(state, true, true)
	require.Equal(t, 900.0, a.target)
	require.Zero(t, b.target)
	// The first battery has not yet reached the request: reserve the entire
	// measured-to-request delta, not merely the change in requested power.
	a.state.Power = -100
	s.updatePVBatteryControl(state, true, true)
	require.Equal(t, 1000.0, a.target)
	require.Zero(t, b.target)
	s.updatePVBatteryControl(state, false, true)
	require.Zero(t, a.target)
	require.Equal(t, api.BatteryOperatingAuto, a.state.Mode)
	a.state.Power = 0
	s.updatePVBatteryControl(state, true, true)
	require.Positive(t, a.target)
	s.updatePVBatteryControl(state, true, false)
	require.Zero(t, a.target)
	s.updatePVBatteryControl(state, true, true)
	require.Positive(t, a.target)
	s.stopPVBatteryControl()
	require.Zero(t, a.target)
	require.Empty(t, a.writes)
	require.Empty(t, b.writes)
}
