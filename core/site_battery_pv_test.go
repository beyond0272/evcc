package core

import (
	"errors"
	"math"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type pvChargingBattery struct {
	power, soc, limit, maxSoc float64
	err, setErr               error
	modes                     []api.BatteryMode
	applied                   []api.BatteryMode
}

func (b *pvChargingBattery) CurrentPower() (float64, error)     { return b.power, b.err }
func (b *pvChargingBattery) Soc() (float64, error)              { return b.soc, b.err }
func (b *pvChargingBattery) GetPowerLimits() (float64, float64) { return b.limit, b.limit }
func (b *pvChargingBattery) GetSocLimits() (float64, float64)   { return 0, b.maxSoc }
func (b *pvChargingBattery) BatteryModes() []api.BatteryMode    { return b.modes }
func (b *pvChargingBattery) SetBatteryMode(mode api.BatteryMode) error {
	b.applied = append(b.applied, mode)
	return b.setErr
}

func pvChargingSite(b *pvChargingBattery) *Site {
	pv := &pvChargingBattery{power: 6000}
	return &Site{
		log:           util.NewLogger("test"),
		gridMeter:     config.NewStaticDevice[api.Meter](config.Named{Name: "grid"}, pv),
		pvMeters:      []config.Device[api.Meter]{config.NewStaticDevice[api.Meter](config.Named{Name: "pv"}, pv)},
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice[api.Meter](config.Named{Name: "battery"}, b)},
	}
}

func TestActivePVBatteryCharging(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		grid, power, soc, limit, maxSoc float64
		applied                         api.BatteryMode
		err                             error
		want                            bool
	}{
		{name: "surplus", grid: -3500, soc: 50, limit: 3300, want: true},
		{name: "house priority", grid: 100, soc: 50, limit: 3300},
		{name: "insufficient surplus", grid: -3299, soc: 50, limit: 3300},
		{name: "start margin", grid: -3450, soc: 50, limit: 3300},
		{name: "continue margin", grid: -150, power: -3300, soc: 50, limit: 3300, applied: api.BatteryCharge, want: true},
		{name: "charging counted once", grid: -50, power: -3300, soc: 50, limit: 3300, applied: api.BatteryCharge},
		{name: "grid import while charging", grid: 500, power: -3300, soc: 50, limit: 3300, applied: api.BatteryCharge},
		{name: "discharge is not PV", grid: -3500, power: 1000, soc: 50, limit: 3300},
		{name: "full", grid: -4000, soc: 100, limit: 3300},
		{name: "soc limit", grid: -4000, soc: 80, maxSoc: 80, limit: 3300},
		{name: "unknown power limit", grid: -4000, soc: 50},
		{name: "invalid soc", grid: -4000, soc: math.NaN(), limit: 3300},
		{name: "invalid power", grid: -4000, power: math.Inf(1), soc: 50, limit: 3300},
		{name: "invalid grid", grid: math.NaN(), soc: 50, limit: 3300},
		{name: "read error", grid: -4000, soc: 50, limit: 3300, err: errors.New("offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &pvChargingBattery{power: tc.power, soc: tc.soc, limit: tc.limit, maxSoc: tc.maxSoc, err: tc.err, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}}
			s := pvChargingSite(b)
			s.batteryModeApplied = map[string]api.BatteryMode{"battery": tc.applied}
			assert.Equal(t, tc.want, s.activePVBatteryCharging(siteState{gridPower: tc.grid})["battery"])
		})
	}
}

func TestActivePVBatteryChargingTransitions(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 3300, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}}
	s := pvChargingSite(b)
	step := func(grid float64) {
		s.batteryPVCharge = s.activePVBatteryCharging(siteState{gridPower: grid})
		s.updateBatteryMode(false, false, api.Rate{})
	}
	step(-4000)
	step(-4000)
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge}, b.applied)
	b.soc = 100
	step(-4000)
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge, api.BatteryNormal}, b.applied)
	b.soc = 50
	step(-4000)
	step(500)
	assert.Equal(t, api.BatteryNormal, b.applied[len(b.applied)-1])
	step(-4000)
	// A failed meter cycle clears the request instead of using the cached state.
	s.batteryPVCharge = nil
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, api.BatteryNormal, b.applied[len(b.applied)-1])
}

func TestActivePVBatteryChargingMultiple(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 2000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}}
	other := &pvChargingBattery{soc: 50, limit: 2000, modes: b.modes}
	s := pvChargingSite(b)
	s.batteryMeters = append(s.batteryMeters, config.NewStaticDevice[api.Meter](config.Named{Name: "other"}, other))
	s.batteryPVCharge = s.activePVBatteryCharging(siteState{gridPower: -3500})
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge}, b.applied)
	assert.Equal(t, []api.BatteryMode{api.BatteryNormal}, other.applied)
	b.soc = 100
	s.batteryPVCharge = s.activePVBatteryCharging(siteState{gridPower: -3500})
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, api.BatteryNormal, b.applied[len(b.applied)-1])
	assert.Equal(t, api.BatteryCharge, other.applied[len(other.applied)-1])
}

func TestActivePVBatteryChargingMeterRequirements(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 2000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}}
	for _, tc := range []struct {
		name   string
		change func(*Site)
	}{
		{"no grid meter", func(s *Site) { s.gridMeter = nil }},
		{"no PV meter", func(s *Site) { s.pvMeters = nil }},
		{"no PV generation", func(s *Site) { s.pvMeters[0].Instance().(*pvChargingBattery).power = 0 }},
		{"insufficient actual PV", func(s *Site) { s.pvMeters[0].Instance().(*pvChargingBattery).power = 1000 }},
		{"PV error", func(s *Site) { s.pvMeters[0].Instance().(*pvChargingBattery).err = api.ErrNotAvailable }},
		{"invalid PV", func(s *Site) { s.pvMeters[0].Instance().(*pvChargingBattery).power = math.NaN() }},
		{"residual reserve", func(s *Site) { s.ResidualPower = 2000 }},
		{"invalid residual", func(s *Site) { s.ResidualPower = math.NaN() }},
		{"missing capabilities", func(s *Site) {
			s.batteryMeters[0] = config.NewStaticDevice[api.Meter](config.Named{Name: "battery"}, &mockMeter{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := pvChargingSite(b)
			tc.change(s)
			assert.Empty(t, s.activePVBatteryCharging(siteState{gridPower: -4000}))
		})
	}
	b.modes = []api.BatteryMode{api.BatteryNormal, api.BatteryHold}
	assert.Empty(t, pvChargingSite(b).activePVBatteryCharging(siteState{gridPower: -4000}))
}

func TestActivePVBatteryChargingPartialWriteRecovery(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 1000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}}
	other := &pvChargingBattery{soc: 50, limit: 1000, modes: b.modes, setErr: errors.New("offline")}
	s := pvChargingSite(b)
	s.batteryMeters = append(s.batteryMeters, config.NewStaticDevice[api.Meter](config.Named{Name: "other"}, other))
	s.batteryPVCharge = map[string]bool{"battery": true, "other": true}
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, api.BatteryUnknown, s.batteryMode)
	assert.True(t, s.batteryPVChargePending)
	s.batteryPVCharge = nil
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge, api.BatteryNormal}, b.applied)
	assert.True(t, s.batteryPVChargePending)
	other.setErr = nil
	s.updateBatteryMode(false, false, api.Rate{})
	assert.False(t, s.batteryPVChargePending)
	assert.Equal(t, api.BatteryNormal, s.batteryMode)
}

func TestActivePVBatteryChargingHemsDimmed(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 1000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryHold, api.BatteryCharge}}
	s := pvChargingSite(b)
	ctrl := gomock.NewController(t)
	hems := api.NewMockHEMS(ctrl)
	limit := 1000.0
	hems.EXPECT().MaxConsumptionPower().Return(&limit).AnyTimes()
	hems.EXPECT().CurtailedPercent().Return(nil).AnyTimes()
	s.hems = hems
	s.batteryPVCharge = map[string]bool{"battery": true}
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, []api.BatteryMode{api.BatteryHold}, b.applied)
}

func TestActivePVBatteryChargingLostAcknowledgement(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 1000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge}, setErr: errors.New("lost acknowledgement")}
	s := pvChargingSite(b)
	s.batteryMode = api.BatteryNormal
	s.batteryModeApplied = map[string]api.BatteryMode{"battery": api.BatteryNormal}
	s.batteryPVCharge = map[string]bool{"battery": true}
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Empty(t, s.batteryModeApplied)
	assert.True(t, s.batteryPVChargePending)
	b.setErr = nil
	s.batteryPVCharge = nil
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, []api.BatteryMode{api.BatteryCharge, api.BatteryNormal}, b.applied)
	assert.False(t, s.batteryPVChargePending)
}

func TestActivePVBatteryChargingPriorityAndFailure(t *testing.T) {
	b := &pvChargingBattery{soc: 50, limit: 2000, modes: []api.BatteryMode{api.BatteryNormal, api.BatteryCharge, api.BatteryHold, api.BatteryDischarge}}
	s := pvChargingSite(b)
	s.batteryPVCharge = map[string]bool{"battery": true}
	s.batteryModeExternal = api.BatteryHold
	assert.Equal(t, api.BatteryHold, s.requiredBatteryMode(false, false, api.Rate{}))
	assert.False(t, s.batteryPVChargeActive)
	s.batteryModeExternal = api.BatteryUnknown
	assert.Equal(t, api.BatteryCharge, s.requiredBatteryMode(true, false, api.Rate{}))
	assert.False(t, s.batteryPVChargeActive)
	assert.Equal(t, api.BatteryDischarge, s.requiredBatteryMode(false, true, api.Rate{}))
	assert.False(t, s.batteryPVChargeActive)
	b.setErr = errors.New("write failed")
	s.updateBatteryMode(false, false, api.Rate{})
	assert.Equal(t, api.BatteryUnknown, s.batteryMode)
	assert.Empty(t, s.batteryModeApplied)
	b.setErr = nil
	s.updateBatteryMode(false, false, api.Rate{})
	require.Equal(t, api.BatteryCharge, s.batteryMode)
	assert.Len(t, b.applied, 2)
}
