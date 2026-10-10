package batterycontrol

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
)

const (
	Conflict Phase = "control-conflict"
	Disabled Phase = "disabled"
)

// Record stores intent before I/O and confirmation afterwards. It is evidence
// of evcc's actions, not a device-enforced exclusive ownership claim.
type Record struct {
	UserRecovery bool      `json:"userRecovery,omitempty"`
	Schema       int       `json:"schema"`
	Revision     string    `json:"revision"`
	Identity     string    `json:"identity"`
	Stage        string    `json:"stage"`
	PreviousMode string    `json:"previousMode"`
	Power        float64   `json:"power"`
	Updated      time.Time `json:"updated"`
	Reason       string    `json:"reason"`
}

type JournalSession struct {
	*Session
	Record   Record
	store    JournalStore
	reader   api.BatteryControlStateReader
	setter   api.BatteryChargePowerController
	restorer api.BatteryControlRestorer
	restart  bool
	storeErr error
}

func NewJournalSession(session *Session, store JournalStore, reader api.BatteryControlStateReader, setter api.BatteryChargePowerController, restorer api.BatteryControlRestorer) *JournalSession {
	j := &JournalSession{Session: session, store: store, reader: reader, setter: setter, restorer: restorer, restart: true}
	if store == nil {
		j.storeErr = fmt.Errorf("durable journal unavailable")
	} else {
		j.Record, j.storeErr = store.Load()
	}

	if j.storeErr == nil && j.Record.Schema != 0 && (!slices.Contains([]string{"idle", "intent", "active", "releasing", "conflict", "disabled"}, j.Record.Stage) || !finite(j.Record.Power) || j.Record.Power < 0 || j.Record.Identity == "") {
		j.storeErr = fmt.Errorf("invalid journal state")
	}
	return j
}

func (j *JournalSession) save(record Record) error {
	if j.storeErr != nil {
		j.transition(Blocked, "journal unavailable")
		return j.storeErr
	}
	record.Schema, record.Revision, record.Updated = 1, rand.Text(), time.Now().UTC()
	if err := j.store.Save(record); err != nil {
		j.storeErr = err
		j.transition(Blocked, "journal write failed")
		return err
	}
	j.Record = record
	return nil
}

func (j *JournalSession) conflict(reason string) error {
	r := j.Record
	r.Stage, r.Reason = "conflict", reason
	err := j.save(r)
	if err == nil {
		j.transition(Conflict, reason)
	}
	return err
}

func (j *JournalSession) observe() (api.BatteryControlState, error) {
	s, err := j.reader.BatteryControlState()
	if err != nil {
		return s, err
	}
	if !fresh(s, time.Now()) || s.Identity == "" || (s.Mode == api.BatteryOperatingAuto && s.NativeMode == "") || (s.ChargeSetpoint != nil && (!finite(*s.ChargeSetpoint) || *s.ChargeSetpoint < 0)) {
		return s, fmt.Errorf("invalid or stale battery observation")
	}
	j.ObservedMode, j.ObservedNativeMode, j.ObservedPower = s.Mode, s.NativeMode, s.Power
	return s, nil
}

func (j *JournalSession) finish(reason string) error {
	r := j.Record
	r.Stage, r.Reason, r.Power = "idle", reason, 0
	r.UserRecovery = false
	if err := j.save(r); err != nil {
		return err
	}
	j.power = 0
	j.transition(Observing, reason)
	return nil
}

// Release never writes on an unreadable, unknown or changed device. In contrast
// to a lease, same-mode external writes remain undetectable without setpoint I/O.
func (j *JournalSession) Release(reason string) error {
	if j.Record.Stage != "active" && j.Record.Stage != "releasing" {
		return nil
	}
	s, err := j.observe()
	if err != nil {
		j.transition(Recovering, "cannot observe battery for release")
		r := j.Record
		r.Stage, r.Reason = "releasing", reason
		return errors.Join(err, j.save(r))
	}
	if s.Identity != j.Record.Identity {
		return j.conflict("device identity changed")
	}
	if s.Mode == api.BatteryOperatingAuto {
		return j.finish("automatic operation observed")
	}
	if s.Mode != api.BatteryOperatingManual {
		return j.conflict("unknown operating mode")
	}
	if !j.Record.UserRecovery && s.ChargeSetpoint != nil && *s.ChargeSetpoint != j.Record.Power {
		return j.conflict("manual setpoint differs from journal")
	}
	r := j.Record
	r.Stage, r.Reason = "releasing", reason
	if err := j.save(r); err != nil {
		return err
	}
	j.transition(Recovering, reason)
	if err := j.restorer.RestoreBatteryMode(r.PreviousMode); err != nil {
		return err
	}
	s, err = j.observe()
	if err != nil {
		return err
	}
	if s.Identity != r.Identity {
		return j.conflict("device identity changed during release")
	}
	if s.Mode != api.BatteryOperatingAuto {
		return fmt.Errorf("automatic mode not yet confirmed")
	}
	if r.PreviousMode != "" && s.NativeMode != r.PreviousMode {
		return j.conflict("different automatic mode after release")
	}
	return j.finish("control returned to automatic operation")
}

// Resolve persists the explicit decision, never a device command. A revision
// prevents an old browser dialog from resolving a newer conflict.
func (j *JournalSession) Resolve(action, revision string) error {
	if j.storeErr != nil {
		return j.storeErr
	}
	if revision == "" || revision != j.Record.Revision {
		return fmt.Errorf("stale journal revision; reload battery state")
	}
	if j.Record.Stage != "conflict" && j.Record.Stage != "disabled" {
		return fmt.Errorf("no conflict or disabled control to resolve")
	}
	r := j.Record
	switch action {
	case "disable":
		r.Stage, r.Reason = "disabled", "disabled by user"
	case "recover":
		s, err := j.observe()
		if err != nil {
			return err
		}
		if s.Mode != api.BatteryOperatingAuto && s.Mode != api.BatteryOperatingManual {
			return fmt.Errorf("unknown device mode; recovery refused")
		}
		if r.Identity != s.Identity {
			r.PreviousMode = ""
		}
		r.Identity = s.Identity
		r.Stage, r.Reason = "releasing", "automatic recovery explicitly authorized by user"
		r.UserRecovery = true
	default:
		return fmt.Errorf("invalid decision")
	}
	if err := j.save(r); err != nil {
		return err
	}
	if action == "disable" {
		j.transition(Disabled, r.Reason)
	} else {
		j.transition(Recovering, r.Reason)
	}
	return nil
}

func (j *JournalSession) Step(in Input) error {
	if j.storeErr != nil {
		j.transition(Blocked, "journal unavailable")
		return j.storeErr
	}
	if j.Record.Stage == "disabled" {
		j.transition(Disabled, j.Record.Reason)
		return nil
	}
	if j.Record.Stage == "conflict" {
		j.transition(Conflict, j.Record.Reason)
		return nil
	}
	s, err := j.observe()
	if err != nil {
		j.transition(Recovering, "battery observation failed")
		if j.Record.Stage == "active" {
			r := j.Record
			r.Stage, r.Reason = "releasing", "observation failed; return control after reconnect"
			return errors.Join(err, j.save(r))
		}
		return err
	}
	if j.Record.Identity != "" && j.Record.Identity != s.Identity {
		return j.conflict("device identity changed")
	}
	if j.Record.Identity == "" {
		j.Record.Identity = s.Identity
	}
	if j.restart {
		j.restart = false
		switch j.Record.Stage {
		case "active", "releasing":
			return j.Release("restart: return previous control before any new charging")
		case "intent":
			if s.Mode == api.BatteryOperatingAuto {
				return j.finish("interrupted command; automatic mode observed")
			}
			return j.conflict("interrupted command without confirmation")
		}
	}
	if j.Record.Stage == "releasing" {
		return j.Release(j.Record.Reason)
	}
	owned := j.Record.Stage == "active"
	if owned && s.Mode == api.BatteryOperatingAuto {
		return j.finish("battery returned to automatic operation")
	}
	if owned && (s.Mode != api.BatteryOperatingManual || (s.ChargeSetpoint != nil && *s.ChargeSetpoint != j.Record.Power)) {
		return j.conflict("manual state differs from confirmed journal")
	}
	if !owned {
		if s.Mode != api.BatteryOperatingAuto {
			return j.conflict("manual or unknown operation without own journal")
		}
		if s.NativeCharging || s.Power < 0 {
			j.transition(Native, "battery is charging autonomously")
			return nil
		}
	}
	valid := in.Valid && in.Allowed && finite(in.Grid) && finite(in.PV) && in.PV > 0 && finite(in.UnavailablePower) && in.UnavailablePower >= 0 && finite(in.Reserve) && in.Reserve >= 0 && finite(in.StartPower) && in.StartPower > 0 && finite(in.MaxPower) && in.MaxPower > 0 && finite(in.MaxSoc) && in.MaxSoc > 0 && in.MaxSoc <= 100 && s.Soc < in.MaxSoc
	if !valid || in.Grid > 0 {
		if owned {
			return j.Release("no safe PV budget, grid import or SoC limit")
		}
		j.transition(Observing, "no safe charging budget")
		return nil
	}
	grid := in.Grid + in.UnavailablePower
	export := -grid - max(0, s.Power)
	if !owned && export < in.StartPower {
		j.transition(Observing, "below configured start threshold")
		return nil
	}
	target := math.Floor(min(in.MaxPower, in.PV, max(0, max(0, -s.Power)+export-in.Reserve)))
	if owned && grid <= 0 && grid >= -in.Reserve {
		target = min(j.Record.Power, in.MaxPower, in.PV)
	}
	if target <= 0 {
		if owned {
			return j.Release("no usable surplus")
		}
		return nil
	}
	if owned && target == j.Record.Power {
		j.power = target
		j.transition(Active, "journal-confirmed manual control")
		return nil
	}
	r := j.Record
	if !owned {
		r.PreviousMode = s.NativeMode
	}
	r.Stage, r.Power, r.Reason = "intent", target, "charge command prepared"
	if err := j.save(r); err != nil {
		return err
	}
	if err := j.setter.SetBatteryChargePower(target); err != nil {
		return errors.Join(err, j.conflict("charge command failed; outcome uncertain"))
	}
	confirmed, err := j.observe()
	if err != nil {
		return errors.Join(err, j.conflict("charge acknowledgement unavailable"))
	}
	if confirmed.Identity != r.Identity || confirmed.Mode != api.BatteryOperatingManual || (confirmed.ChargeSetpoint != nil && *confirmed.ChargeSetpoint != target) {
		return j.conflict("charge command not confirmed by device state")
	}
	r.Stage, r.Reason = "active", "charge command acknowledged; manual mode observed"
	if err := j.save(r); err != nil {
		return err
	}
	j.power = target
	j.transition(Active, "journal-confirmed manual control")
	return nil
}

func (j *JournalSession) Close() error {
	if closer, ok := j.store.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// SetDevice refreshes adapters after configuration reload; observation still
// checks the journal identity before any command can reach the replacement.
func (j *JournalSession) SetDevice(reader api.BatteryControlStateReader, setter api.BatteryChargePowerController, restorer api.BatteryControlRestorer) {
	j.reader, j.setter, j.restorer = reader, setter, restorer
}
