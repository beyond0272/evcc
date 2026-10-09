package core

import (
	"errors"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/require"
)

type pvDynamicBattery struct {
	pvChargingBattery
	targets []float64
}

func (b *pvDynamicBattery) SetBatteryChargePower(power float64) error {
	b.targets = append(b.targets, power)
	return nil
}

func TestDynamicPVBatteryCharging(t *testing.T) {
	b := &pvDynamicBattery{
		pvChargingBattery: pvChargingBattery{
			soc:   21,
			limit: 3300,
			modes: []api.BatteryMode{
				api.BatteryNormal,
				api.BatteryCharge,
			},
		},
	}

	site := pvChargingSite(&b.pvChargingBattery)
	site.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](
			config.Named{Name: "battery"}, b,
		),
	}

	grid := &pvChargingBattery{}
	site.gridMeter = config.NewStaticDevice[api.Meter](
		config.Named{Name: "grid"}, grid,
	)

	site.BatteryPVStartPower = 500
	site.ResidualPower = 100

	step := func(gridPower, pvPower, batteryPower float64) {
		grid.power = gridPower
		b.power = batteryPower

		state := siteState{
			gridPower: gridPower,
			pvPower:   pvPower,
		}

		site.batteryPVSetpoints =
			site.dynamicPVBatteryCharging(state)

		site.batteryPVCharge = make(map[string]bool)
		for name := range site.batteryPVSetpoints {
			site.batteryPVCharge[name] = true
		}

		site.updateBatteryMode(false, false, api.Rate{})
	}

	// First start: 2530W export, 100W reserve.
	step(-2530, 2830, 0)
	require.Equal(t, []float64{2430}, b.targets)
	require.Empty(t, b.applied,
		"PV charging must not invoke fixed BatteryCharge mode")

	// Cloud: remain active below 500W start threshold.
	step(-100, 450, -300)
	require.Equal(t, []float64{2430, 300}, b.targets)
	require.Empty(t, b.applied)

	// No usable surplus: restore normal battery mode.
	step(400, 100, -300)
	require.Equal(t, api.BatteryNormal,
		b.applied[len(b.applied)-1])
}

type pvDynamicFailBattery struct {
	pvDynamicBattery
}

func (b *pvDynamicFailBattery) SetBatteryChargePower(power float64) error {
	return errors.New("simulated sonnen communication failure")
}

func TestDynamicPVBatteryChargingRollback(t *testing.T) {
	b := &pvDynamicFailBattery{
		pvDynamicBattery: pvDynamicBattery{
			pvChargingBattery: pvChargingBattery{
				soc:   21,
				limit: 3300,
				modes: []api.BatteryMode{
					api.BatteryNormal,
					api.BatteryCharge,
				},
			},
		},
	}

	site := pvChargingSite(&b.pvChargingBattery)
	site.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](
			config.Named{Name: "battery"}, b,
		),
	}

	site.batteryPVSetpoints = map[string]float64{
		"battery": 1000,
	}
	site.batteryPVCharge = map[string]bool{
		"battery": true,
	}

	site.updateBatteryMode(false, false, api.Rate{})

	require.Equal(t,
		[]api.BatteryMode{api.BatteryNormal},
		b.applied,
		"communication failure must trigger normal mode",
	)
	require.True(t, site.batteryPVChargePending)
}

func TestDynamicPVRespectsNativeCharging(t *testing.T) {
	b := &pvDynamicBattery{
		pvChargingBattery: pvChargingBattery{
			power: -900,
			soc:   25,
			limit: 3300,
			modes: []api.BatteryMode{
				api.BatteryNormal,
				api.BatteryCharge,
			},
		},
	}

	s := pvChargingSite(&b.pvChargingBattery)
	s.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](
			config.Named{Name: "battery"}, b,
		),
	}

	grid := &pvChargingBattery{power: -40}
	s.gridMeter = config.NewStaticDevice[api.Meter](
		config.Named{Name: "grid"}, grid,
	)

	s.BatteryPVStartPower = 500
	s.ResidualPower = 100

	// Sonnen already uses the available PV energy.
	result := s.dynamicPVBatteryCharging(siteState{
		gridPower: -40,
		pvPower:   1500,
	})
	require.Empty(t, result)

	// Sonnen continues charging, but leaves 800W unused.
	grid.power = -800
	state := siteState{
		gridPower: -800,
		pvPower:   2200,
	}

	// First observation: don't intervene yet.
	require.Empty(t, s.dynamicPVBatteryCharging(state))

	// Second observation: take over unused surplus.
	result = s.dynamicPVBatteryCharging(state)
	require.Equal(t, 1600.0, result["battery"])

	s.batteryPVSetpoints = result
	s.batteryPVCharge = map[string]bool{"battery": true}
	s.updateBatteryMode(false, false, api.Rate{})

	require.Equal(t, []float64{1600}, b.targets)
	require.Empty(t, b.applied,
		"must not request fixed-power BatteryCharge mode")
}

func TestDynamicPVToGridCharging(t *testing.T) {
	b := &pvDynamicBattery{
		pvChargingBattery: pvChargingBattery{
			soc:   25,
			limit: 3300,
			modes: []api.BatteryMode{
				api.BatteryNormal,
				api.BatteryCharge,
			},
		},
	}

	s := pvChargingSite(&b.pvChargingBattery)
	s.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](
			config.Named{Name: "battery"}, b,
		),
	}

	s.batteryPVSetpoints = map[string]float64{"battery": 1000}
	s.batteryPVCharge = map[string]bool{"battery": true}

	// PV charging must use the dynamic power setter.
	s.updateBatteryMode(false, false, api.Rate{})
	require.Equal(t, []float64{1000}, b.targets)
	require.Empty(t, b.applied)

	// Grid charging now takes priority.
	s.batteryPVSetpoints = nil
	s.batteryPVCharge = nil
	s.updateBatteryMode(true, false, api.Rate{})

	require.Equal(t,
		[]api.BatteryMode{api.BatteryCharge},
		b.applied,
		"grid charging must explicitly restore the fixed charge mode",
	)
	require.False(t, s.batteryPVChargeActive)
}

// A failed charge command followed by a failed rollback must prevent
// further PV charging until normal mode has been restored successfully.
func TestDynamicPVRecoveryLockout(t *testing.T) {
	b := &pvDynamicFailBattery{
		pvDynamicBattery: pvDynamicBattery{
			pvChargingBattery: pvChargingBattery{
				soc:    25,
				limit:  3300,
				setErr: errors.New("simulated rollback failure"),
				modes: []api.BatteryMode{
					api.BatteryNormal,
					api.BatteryCharge,
				},
			},
		},
	}

	s := pvChargingSite(&b.pvChargingBattery)
	s.batteryMeters = []config.Device[api.Meter]{
		config.NewStaticDevice[api.Meter](
			config.Named{Name: "battery"}, b,
		),
	}

	grid := &pvChargingBattery{power: -1500}
	s.gridMeter = config.NewStaticDevice[api.Meter](
		config.Named{Name: "grid"}, grid,
	)

	s.BatteryPVStartPower = 500
	s.ResidualPower = 100

	s.batteryPVSetpoints = map[string]float64{"battery": 1000}
	s.batteryPVCharge = map[string]bool{"battery": true}

	// Charging fails, and immediate rollback also fails.
	s.updateBatteryMode(false, false, api.Rate{})

	require.True(t, s.batteryPVRecoveryRequired)
	require.Equal(t,
		[]api.BatteryMode{api.BatteryNormal},
		b.applied,
	)

	state := siteState{
		gridPower: -1500,
		pvPower:   1800,
	}

	// PV surplus exists, but recovery blocks new requests.
	require.Empty(t, s.dynamicPVBatteryCharging(state))

	s.batteryPVSetpoints = nil
	s.batteryPVCharge = nil

	// Recovery still fails.
	s.updateBatteryMode(false, false, api.Rate{})
	require.True(t, s.batteryPVRecoveryRequired)
	require.Len(t, b.applied, 2)

	// Communication is restored.
	b.setErr = nil
	s.updateBatteryMode(false, false, api.Rate{})

	require.False(t, s.batteryPVRecoveryRequired)
	require.Equal(t, api.BatteryNormal, b.applied[len(b.applied)-1])
	require.Len(t, b.applied, 3)

	// PV charging may now resume.
	result := s.dynamicPVBatteryCharging(state)
	require.Equal(t, 1400.0, result["battery"])
}
