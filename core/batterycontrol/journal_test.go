package batterycontrol

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/stretchr/testify/require"
)

type memoryJournal struct {
	record    Record
	writes    []Record
	err       error
	failStage string
}

func (m *memoryJournal) Load() (Record, error) { return m.record, m.err }
func (m *memoryJournal) Save(r Record) error {
	if m.err != nil {
		return m.err
	}
	if m.failStage == r.Stage {
		return errors.New("sync failed")
	}
	m.record = r
	m.writes = append(m.writes, r)
	return nil
}

type journalDevice struct {
	state                            api.BatteryControlState
	store                            *memoryJournal
	commands                         []string
	failRead, failWrite, failRestore bool
}

func (d *journalDevice) BatteryControlState() (api.BatteryControlState, error) {
	if d.failRead {
		return d.state, errors.New("offline")
	}
	s := d.state
	s.ObservedAt = time.Now()
	return s, nil
}
func (d *journalDevice) SetBatteryChargePower(p float64) error {
	if d.store.record.Stage != "intent" {
		panic("command before durable intent")
	}
	d.commands = append(d.commands, "charge")
	d.state.Mode = api.BatteryOperatingManual
	d.state.NativeMode = "1"
	d.state.ChargeSetpoint = new(p)
	if d.failWrite {
		return errors.New("lost acknowledgement")
	}
	return nil
}
func (d *journalDevice) RestoreBatteryMode(mode string) error {
	if d.store.record.Stage != "releasing" {
		panic("restore before durable intent")
	}
	d.commands = append(d.commands, "restore:"+mode)
	if d.failRestore {
		return errors.New("offline")
	}
	if mode == "" {
		mode = "2"
	}
	d.state.Mode = api.BatteryOperatingAuto
	d.state.NativeMode = mode
	d.state.ChargeSetpoint = nil
	d.state.Power = 0
	return nil
}
func journalFixture() (*JournalSession, *journalDevice, *memoryJournal) {
	m := new(memoryJournal)
	d := &journalDevice{store: m, state: api.BatteryControlState{Identity: "device", Mode: api.BatteryOperatingAuto, NativeMode: "10", Soc: 70}}
	j := NewJournalSession(new(Session), m, d, d, d)
	return j, d, m
}

func TestJournalIntentConfirmationAndRestartRecovery(t *testing.T) {
	j, d, m := journalFixture()
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, "intent", m.writes[0].Stage)
	require.Equal(t, "active", m.writes[1].Stage)
	require.Equal(t, 400.0, j.Power())
	restarted := NewJournalSession(new(Session), m, d, d, d)
	require.NoError(t, restarted.Step(inputs()))
	require.Equal(t, []string{"charge", "restore:10"}, d.commands)
	require.Equal(t, "idle", m.record.Stage)
	require.Zero(t, restarted.Power())
}

func TestJournalInterruptedCommandRequiresDecision(t *testing.T) {
	for _, manual := range []bool{false, true} {
		j, d, m := journalFixture()
		m.record = Record{Schema: 1, Revision: "saved", Identity: "device", Stage: "intent", PreviousMode: "10", Power: 400}
		if manual {
			d.state.Mode = api.BatteryOperatingManual
		}
		j = NewJournalSession(new(Session), m, d, d, d)
		require.NoError(t, j.Step(inputs()))
		require.Empty(t, d.commands)
		if manual {
			require.Equal(t, Conflict, j.Phase)
		} else {
			require.Equal(t, Observing, j.Phase)
		}
	}
}

func TestJournalUnknownManualDisableAndExplicitRecovery(t *testing.T) {
	j, d, m := journalFixture()
	d.state.Mode = api.BatteryOperatingManual
	d.state.NativeMode = "1"
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, Conflict, j.Phase)
	require.Error(t, j.Resolve("recover", "old-dialog"))
	require.NoError(t, j.Resolve("disable", j.Record.Revision))
	j = NewJournalSession(new(Session), m, d, d, d)
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, Disabled, j.Phase)
	require.Empty(t, d.commands)
	require.NoError(t, j.Resolve("recover", j.Record.Revision))
	require.Empty(t, d.commands, "decision alone never commands device")
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, []string{"restore:"}, d.commands)
	require.Equal(t, Observing, j.Phase, "no charge immediately after recovery")
}

func TestJournalNativeAndAutomaticReturnAreNotConflicts(t *testing.T) {
	j, d, _ := journalFixture()
	d.state.Power = -900
	d.state.NativeCharging = true
	for range 5 {
		require.NoError(t, j.Step(inputs()))
	}
	require.Equal(t, Native, j.Phase)
	require.Empty(t, d.commands)
	d.state.Power = 0
	d.state.NativeCharging = false
	require.NoError(t, j.Step(inputs()))
	d.state.Mode = api.BatteryOperatingAuto
	d.state.NativeMode = "2"
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, Observing, j.Phase)
	require.Equal(t, []string{"charge"}, d.commands)
}

func TestJournalSetpointConflictNotMeasuredPower(t *testing.T) {
	j, d, _ := journalFixture()
	require.NoError(t, j.Step(inputs()))
	d.state.Power = -250 // physical response need not equal the 400W command
	in := inputs()
	in.Grid = 0
	require.NoError(t, j.Step(in))
	require.Equal(t, Active, j.Phase)
	d.state.ChargeSetpoint = new(900.0)
	require.NoError(t, j.Step(in))
	require.Equal(t, Conflict, j.Phase)
	require.Equal(t, []string{"charge"}, d.commands)
	require.NoError(t, j.Release("shutdown"))
	require.Equal(t, []string{"charge"}, d.commands)
}

func TestJournalWriteReadAndRestoreFailures(t *testing.T) {
	j, d, m := journalFixture()
	m.err = errors.New("disk full")
	require.Error(t, j.Step(inputs()))
	require.Empty(t, d.commands)
	j, d, _ = journalFixture()
	d.failWrite = true
	require.Error(t, j.Step(inputs()))
	require.Equal(t, Conflict, j.Phase)
	require.NoError(t, j.Step(inputs()))
	require.Len(t, d.commands, 1)
	j, d, m = journalFixture()
	require.NoError(t, j.Step(inputs()))
	d.failRead = true
	require.Error(t, j.Step(inputs()))
	require.Len(t, d.commands, 1)
	d.failRead = false
	d.failRestore = true
	in := inputs()
	in.Grid = 1
	require.Error(t, j.Step(in))
	require.Equal(t, Recovering, j.Phase)
	require.Equal(t, "releasing", m.record.Stage)
	d.failRestore = false
	restarted := NewJournalSession(new(Session), m, d, d, d)
	require.NoError(t, restarted.Step(inputs()))
	require.Equal(t, []string{"charge", "restore:10", "restore:10"}, d.commands)
}

func TestJournalConfiguredStartContinuationSocAndIdentity(t *testing.T) {
	j, d, _ := journalFixture()
	in := inputs()
	in.StartPower = 750
	require.NoError(t, j.Step(in))
	require.Empty(t, d.commands)
	in.Grid = -750
	require.NoError(t, j.Step(in))
	require.Equal(t, 650.0, j.Power())
	d.state.Power = -650
	in.Grid = 0
	require.NoError(t, j.Step(in))
	require.Equal(t, 650.0, j.Power())
	d.state.Soc = 100
	require.NoError(t, j.Step(in))
	require.Equal(t, "idle", j.Record.Stage)
	j, d, _ = journalFixture()
	require.NoError(t, j.Step(inputs()))
	d.state.Identity = "other-device"
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, Conflict, j.Phase)
	require.Len(t, d.commands, 1)
}

func TestFileJournalRoundTripAndTornTail(t *testing.T) {
	f := FileJournal{Path: filepath.Join(t.TempDir(), "journal.jsonl")}
	r, err := f.Load()
	require.NoError(t, err)
	require.Empty(t, r.Stage)
	want := Record{Schema: 1, Revision: "one", Stage: "intent", Identity: "device", Power: 400}
	require.NoError(t, f.Save(want))
	want.Revision, want.Stage = "two", "active"
	require.NoError(t, f.Save(want))
	r, err = f.Load()
	require.NoError(t, err)
	require.Equal(t, want, r)
	file, err := os.OpenFile(f.Path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"schema":1`)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = f.Load()
	require.Error(t, err, "never guess through a torn journal tail")
}

func TestFileJournalExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	first, err := OpenFileJournal(path)
	require.NoError(t, err)
	_, err = OpenFileJournal(path)
	require.Error(t, err)
	require.NoError(t, first.Close())
	second, err := OpenFileJournal(path)
	require.NoError(t, err)
	require.NoError(t, second.Close())
}

func TestJournalReleaseReadFailurePersistsRecovery(t *testing.T) {
	j, d, m := journalFixture()
	require.NoError(t, j.Step(inputs()))
	d.failRead = true
	require.Error(t, j.Release("shutdown"))
	require.Equal(t, "releasing", m.record.Stage)
	d.failRead = false
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, []string{"charge", "restore:10"}, d.commands)
	require.Equal(t, "idle", m.record.Stage)
}

func TestJournalConfirmationPersistenceFailureBlocks(t *testing.T) {
	j, d, m := journalFixture()
	m.failStage = "active"
	require.Error(t, j.Step(inputs()))
	require.Equal(t, Blocked, j.Phase)
	require.Equal(t, "intent", m.record.Stage)
	require.Error(t, j.Step(inputs()))
	require.Equal(t, []string{"charge"}, d.commands)
	m.failStage = ""
	restarted := NewJournalSession(new(Session), m, d, d, d)
	require.NoError(t, restarted.Step(inputs()))
	require.Equal(t, Conflict, restarted.Phase)
	require.Equal(t, []string{"charge"}, d.commands)
}

func TestJournalReconfiguredDeviceDoesNotReuseOldAdapter(t *testing.T) {
	j, original, m := journalFixture()
	require.NoError(t, j.Step(inputs()))
	replacement := &journalDevice{store: m, state: api.BatteryControlState{Identity: "replacement", Mode: api.BatteryOperatingAuto, NativeMode: "2", Soc: 70}}
	j.SetDevice(replacement, replacement, replacement)
	require.NoError(t, j.Step(inputs()))
	require.Equal(t, Conflict, j.Phase)
	require.Empty(t, replacement.commands)
	require.Equal(t, []string{"charge"}, original.commands)
}
