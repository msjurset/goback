package catchup

import (
	"testing"
	"time"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

func TestPrevFireTime(t *testing.T) {
	// Fixed reference time: 2026-03-16 12:00 UTC (Monday)
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		expr    string
		now     time.Time
		want    time.Time
		wantErr bool
	}{
		{
			name: "daily at 6am, now is noon",
			expr: "0 6 * * *",
			now:  now,
			want: time.Date(2026, 3, 16, 6, 0, 0, 0, time.UTC),
		},
		{
			name: "daily at 6am, now is 5am",
			expr: "0 6 * * *",
			now:  time.Date(2026, 3, 16, 5, 0, 0, 0, time.UTC),
			want: time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC),
		},
		{
			name: "weekly sunday 6am, now is monday",
			expr: "0 6 * * 0",
			now:  now,
			want: time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC),
		},
		{
			name: "every hour, now is 12:30",
			expr: "0 * * * *",
			now:  time.Date(2026, 3, 16, 12, 30, 0, 0, time.UTC),
			want: time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			name:    "invalid expression",
			expr:    "bad",
			now:     now,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrevFireTime(tt.expr, tt.now)
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

func TestLastSuccessfulBackup(t *testing.T) {
	base := time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		records []storage.BackupRecord
		backup  string
		want    time.Time
	}{
		{
			name:    "no records",
			records: nil,
			backup:  "foo",
			want:    time.Time{},
		},
		{
			name: "only errors",
			records: []storage.BackupRecord{
				{Name: "foo", Timestamp: base, Error: "failed"},
			},
			backup: "foo",
			want:   time.Time{},
		},
		{
			name: "single success",
			records: []storage.BackupRecord{
				{Name: "foo", Timestamp: base, Error: ""},
			},
			backup: "foo",
			want:   base,
		},
		{
			name: "latest of multiple",
			records: []storage.BackupRecord{
				{Name: "foo", Timestamp: base, Error: ""},
				{Name: "foo", Timestamp: base.Add(time.Hour), Error: ""},
				{Name: "foo", Timestamp: base.Add(2 * time.Hour), Error: "failed"},
			},
			backup: "foo",
			want:   base.Add(time.Hour),
		},
		{
			name: "different backup name",
			records: []storage.BackupRecord{
				{Name: "bar", Timestamp: base, Error: ""},
			},
			backup: "foo",
			want:   time.Time{},
		},
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

func TestFindMissed(t *testing.T) {
	// Monday 2026-03-16 12:00 UTC
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

	// Sunday schedule: fires at 2026-03-15 06:00
	sundayBackup := config.BackupConfig{
		Name:     "weekly",
		Type:     "ssh",
		Schedule: "0 6 * * 0",
	}

	// Daily schedule: fires at 2026-03-16 06:00
	dailyBackup := config.BackupConfig{
		Name:     "daily",
		Type:     "ssh",
		Schedule: "0 6 * * *",
	}

	tests := []struct {
		name    string
		backups []config.BackupConfig
		records []storage.BackupRecord
		want    []string // expected missed backup names
	}{
		{
			name:    "no backups configured",
			backups: nil,
			want:    nil,
		},
		{
			name:    "never run — skipped",
			backups: []config.BackupConfig{sundayBackup},
			records: nil,
			want:    nil,
		},
		{
			name:    "ran after last fire time — not missed",
			backups: []config.BackupConfig{sundayBackup},
			records: []storage.BackupRecord{
				{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)},
			},
			want: nil,
		},
		{
			name:    "last success before fire time — missed",
			backups: []config.BackupConfig{sundayBackup},
			records: []storage.BackupRecord{
				{Name: "weekly", Timestamp: time.Date(2026, 3, 8, 6, 5, 0, 0, time.UTC)},
			},
			want: []string{"weekly"},
		},
		{
			name:    "multiple backups mixed",
			backups: []config.BackupConfig{sundayBackup, dailyBackup},
			records: []storage.BackupRecord{
				{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)},
				{Name: "daily", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC)},
			},
			want: []string{"daily"}, // daily missed today's 06:00
		},
		{
			name:    "error-only records treated as never succeeded",
			backups: []config.BackupConfig{sundayBackup},
			records: []storage.BackupRecord{
				{Name: "weekly", Timestamp: time.Date(2026, 3, 15, 6, 5, 0, 0, time.UTC), Error: "failed"},
			},
			want: nil, // no successful run → skip
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &storage.StatusLog{Records: tt.records}
			got := FindMissed(tt.backups, log, now)

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
