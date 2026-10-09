package api

import "time"

// BatteryOperatingMode describes the actual device mode, not evcc's requested mode.
type BatteryOperatingMode string

const (
	BatteryOperatingUnknown BatteryOperatingMode = "unknown"
	BatteryOperatingAuto    BatteryOperatingMode = "automatic"
	BatteryOperatingManual  BatteryOperatingMode = "manual"
)

// BatteryControlState is a fresh device observation. Power is positive for discharge.
// LeaseID must come from an enforcing device/controller, never from local command history.
type BatteryControlState struct {
	Mode           BatteryOperatingMode
	NativeMode     string
	Power          float64
	Soc            float64
	NativeCharging bool
	ObservedAt     time.Time
	LeaseID        string
	LeaseUntil     time.Time
}

// BatteryControlStateReader reports actual operating mode and battery measurements.
type BatteryControlStateReader interface {
	BatteryControlState() (BatteryControlState, error)
}

// BatteryPVLeaseController provides fenced, expiring control of PV charging.
// Acquire must atomically refuse manual mode, native charging and another owner.
// All writes are conditional on the lease ID; expiry independently stops forced
// charging and restores the prior automatic mode even if evcc crashes or loses power.
// An unfenced HTTP mode/setpoint sequence does not satisfy this contract.
type BatteryPVLeaseController interface {
	BatteryControlStateReader
	AcquireBatteryPVControl(id string, power float64, ttl time.Duration) error
	RenewBatteryPVControl(id string, power float64, ttl time.Duration) error
	ReleaseBatteryPVControl(id string) error
}
