package catchup

import (
	"testing"
	"time"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

func TestPrevFireTime(t *testing.T) {
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC) // Monday

	tests := []struct {
		name     string
		expr     string
		now      time.Time
		lookback time.Duration
		want     time.Time
		wantErr  bool
	}{
		{
			name:     "daily at 6am, now is noon",
			expr:     "0 6 * * *",
			now:      now,
			lookback: 7 * 24 * time.Hour,
			want:     time.Date(2026, 3, 16, 6, 0, 0, 0, time.UTC),
		},
		{
			name:     "daily at 6am, now is 5am",
			expr:     "0 6 * * *",
			now:      time.Date(2026, 3, 16, 5, 0, 0, 0, time.UTC),
			lookback: 7 * 24 * time.Hour,
			want:     time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC),
		},
		{
			name:     "weekly sunday 6am, now is monday",
			expr:     "0 6 * * 0",
			now:      now,
			lookback: 7 * 24 * time.Hour,
			want:     time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC),
		},
		{
			name:     "every hour, now is 12:30",
			expr:     "0 * * * *",
			now:      time.Date(2026, 3, 16, 12, 30, 0, 0, time.UTC),
			lookback: 7 * 24 * time.Hour,
			want:     time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "monthly on the 1st, now is mid-month — wide lookback finds it",
			expr:     "0 6 1 * *",
			now:      time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC),
			lookback: 60 * 24 * time.Hour,
			want:     time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC),
		},
		{
			name:     "monthly on the 1st with 7d lookback — too narrow, returns zero",
			expr:     "0 6 1 * *",
			now:      time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC),
			lookback: 7 * 24 * time.Hour,
			want:     time.Time{},
		},
		{
			name:     "invalid expression",
			expr:     "bad",
			now:      now,
			lookback: 7 * 24 * time.Hour,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrevFireTime(tt.expr, tt.now, tt.lookback)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFindMissedYearlyClamp covers the lookback ceiling: a yearly schedule
// whose most recent fire is more than 90 days in the past falls outside the
// catchup window and is not flagged as missed.
func TestFindMissedYearlyClamp(t *testing.T) {
	// August 1 — Jan 1 is 212 days ago, well past the 90-day ceiling.
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	yearly := config.BackupConfig{Name: "yearly", Type: "ssh", Schedule: "0 6 1 1 *"}
	pastStart := now.Add(-2 * 365 * 24 * time.Hour)

	missed := FindMissed(
		[]config.BackupConfig{yearly},
		&storage.StatusLog{Records: []storage.BackupRecord{
			{Name: "yearly", Timestamp: time.Date(2024, 1, 1, 6, 5, 0, 0, time.UTC)},
		}},
		pastStart,
		now,
	)
	if len(missed) != 0 {
		t.Errorf("yearly outside 90-day clamp should not be missed; got %v", missed)
	}
}

func TestScheduleCycle(t *testing.T) {
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC) // Monday

	tests := []struct {
		name string
		expr string
		want time.Duration // approximate longest gap
	}{
		{name: "hourly", expr: "0 * * * *", want: time.Hour},
		{name: "daily", expr: "0 6 * * *", want: 24 * time.Hour},
		{name: "weekly", expr: "0 6 * * 0", want: 7 * 24 * time.Hour},
		{name: "weekdays only — Fri→Mon is the longest gap", expr: "0 6 * * 1-5", want: 3 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scheduleCycle(tt.expr, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastSuccessfulBackup(t *testing.T) {
	base := time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		records []storage.BackupRecord
		backup  string
		want    time.Time
	}{
		{name: "no records", records: nil, backup: "foo", want: time.Time{}},
		{name: "only errors", records: []storage.BackupRecord{
			{Name: "foo", Timestamp: base, Error: "failed"},
		}, backup: "foo", want: time.Time{}},
		{name: "single success", records: []storage.BackupRecord{
			{Name: "foo", Timestamp: base},
		}, backup: "foo", want: base},
		{name: "latest of multiple", records: []storage.BackupRecord{
			{Name: "foo", Timestamp: base},
			{Name: "foo", Timestamp: base.Add(time.Hour)},
			{Name: "foo", Timestamp: base.Add(2 * time.Hour), Error: "failed"},
		}, backup: "foo", want: base.Add(time.Hour)},
		{name: "different name", records: []storage.BackupRecord{
			{Name: "bar", Timestamp: base},
		}, backup: "foo", want: time.Time{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &storage.StatusLog{Records: tt.records}
			got := LastSuccessfulBackup(log, tt.backup)
			if !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLastAttempt(t *testing.T) {
	base := time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC)

	// LastAttempt counts both success and failure records
	records := []storage.BackupRecord{
		{Name: "foo", Timestamp: base},
		{Name: "foo", Timestamp: base.Add(time.Hour), Error: "failed"},
	}
	got := LastAttempt(&storage.StatusLog{Records: records}, "foo")
	want := base.Add(time.Hour)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFindMissed(t *testing.T) {
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC) // Monday

	weekly := config.BackupConfig{Name: "weekly", Type: "ssh", Schedule: "0 6 * * 0"}  // Sun 6am
	daily := config.BackupConfig{Name: "daily", Type: "ssh", Schedule: "0 6 * * *"}    // 6am
	monthly := config.BackupConfig{Name: "monthly", Type: "ssh", Schedule: "0 6 1 * *"} // 1st 6am
	yearly := config.BackupConfig{Name: "yearly", Type: "ssh", Schedule: "0 6 1 1 *"}  // Jan 1 6am

	// daemonStart well in the past unless overridden by test
	pastStart := now.Add(-365 * 24 * time.Hour)

	tests := []struct {
		name        string
		backups     []config.BackupConfig
		records     []storage.BackupRecord
		daemonStart time.Time
		want        []string
	}{
		{
			name:        "no backups configured",
			backups:     nil,
			daemonStart: pastStart,
			want:        nil,
		},
		{
			name:        "weekly: ran after last fire — not missed",
			backups:     []config.BackupConfig{weekly},
			records:     []storage.BackupRecord{{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)}},
			daemonStart: pastStart,
			want:        nil,
		},
		{
			name:        "weekly: last success a week before — missed",
			backups:     []config.BackupConfig{weekly},
			records:     []storage.BackupRecord{{Name: "weekly", Timestamp: time.Date(2026, 3, 8, 6, 5, 0, 0, time.UTC)}},
			daemonStart: pastStart,
			want:        []string{"weekly"},
		},
		{
			name:        "weekly: error-only record after fire — NOT missed (we attempted)",
			backups:     []config.BackupConfig{weekly},
			records:     []storage.BackupRecord{{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC), Error: "failed"}},
			daemonStart: pastStart,
			want:        nil,
		},
		{
			name:        "weekly: error-only record from PREVIOUS cycle — missed",
			backups:     []config.BackupConfig{weekly},
			records:     []storage.BackupRecord{{Name: "weekly", Timestamp: time.Date(2026, 3, 8, 6, 5, 0, 0, time.UTC), Error: "failed"}},
			daemonStart: pastStart,
			want:        []string{"weekly"},
		},
		{
			name:        "monthly: missed last 1st (15 days ago)",
			backups:     []config.BackupConfig{monthly},
			records:     []storage.BackupRecord{{Name: "monthly", Timestamp: time.Date(2026, 2, 1, 6, 5, 0, 0, time.UTC)}},
			daemonStart: pastStart,
			want:        []string{"monthly"},
		},
		{
			name:        "monthly: ran on the 1st — not missed",
			backups:     []config.BackupConfig{monthly},
			records:     []storage.BackupRecord{{Name: "monthly", Timestamp: time.Date(2026, 3, 1, 6, 5, 0, 0, time.UTC)}},
			daemonStart: pastStart,
			want:        nil,
		},
		{
			name:        "yearly: prev fire 75d ago — within 90d ceiling, detected",
			backups:     []config.BackupConfig{yearly},
			records:     []storage.BackupRecord{{Name: "yearly", Timestamp: time.Date(2025, 1, 1, 6, 5, 0, 0, time.UTC)}},
			daemonStart: pastStart,
			want:        []string{"yearly"},
		},
		{
			name:        "never-run, daemon up before fire — missed",
			backups:     []config.BackupConfig{weekly},
			records:     nil,
			daemonStart: time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC), // Saturday before Sunday's 6am
			want:        []string{"weekly"},
		},
		{
			name:        "never-run, daemon started after fire — not missed",
			backups:     []config.BackupConfig{weekly},
			records:     nil,
			daemonStart: time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), // Sunday afternoon, after 6am fire
			want:        nil,
		},
		{
			name:        "multiple backups mixed",
			backups:     []config.BackupConfig{weekly, daily},
			records: []storage.BackupRecord{
				{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)},
				{Name: "daily", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)},
			},
			daemonStart: pastStart,
			want:        []string{"daily"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &storage.StatusLog{Records: tt.records}
			got := FindMissed(tt.backups, log, tt.daemonStart, now)

			var names []string
			for _, b := range got {
				names = append(names, b.Name)
			}

			if len(names) != len(tt.want) {
				t.Fatalf("got %v, want %v", names, tt.want)
			}
			for i := range names {
				if names[i] != tt.want[i] {
					t.Errorf("got[%d] = %s, want %s", i, names[i], tt.want[i])
				}
			}
		})
	}
}
