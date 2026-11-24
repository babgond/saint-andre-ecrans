package main

import (
	"os"
	"testing"
	"time"
)

func TestBackupFrequency(t *testing.T) {
	last := time.Date(2026, time.January, 31, 9, 45, 0, 0, paris)
	for _, tt := range []struct {
		frequency string
		due       time.Time
	}{
		{"daily", last.Add(24 * time.Hour)},
		{"weekly", last.AddDate(0, 0, 7)},
		{"fortnightly", last.AddDate(0, 0, 15)},
		{"monthly", time.Date(2026, time.February, 28, 9, 45, 0, 0, paris)},
	} {
		plan := backupPlan{Frequency: tt.frequency}
		if plan.due(tt.due.Add(-time.Second), last) || !plan.due(tt.due, last) {
			t.Errorf("%s : échéance attendue le %v", tt.frequency, tt.due)
		}
	}
	if (backupPlan{Frequency: "off"}).due(last.AddDate(0, 4, 0), last) {
		t.Fatal("sauvegarde automatique due en mode désactivé")
	}
}

func TestPurgeBackups(t *testing.T) {
	a := testApp(t)
	if plan, err := a.backupPlan(); err != nil || plan.Frequency != "daily" {
		t.Fatalf("fréquence initiale : %+v, %v", plan, err)
	}
	callJSON(t, a, "PUT", "backup-frequency", `{"frequency":"invalid"}`, 400)
	callJSON(t, a, "PUT", "backup-frequency", `{"frequency":"off"}`, 204)
	assertCount := func(want int) {
		t.Helper()
		list, err := a.backupList()
		if err != nil || len(list) != want {
			t.Fatalf("archives : %v, erreur %v, nombre attendu %d", list, err, want)
		}
	}
	if err := a.runScheduledBackup(time.Now()); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
	postImage(t, a, "images", "Affiche.png", pngImage(t, 1), 201)
	call(t, a, "POST", "/api/admin/backups", nil, true, nil, 201)
	call(t, a, "POST", "/api/admin/backups", nil, true, nil, 201)
	assertCount(2)
	path := storagePath(a.data, backups, "notes.txt")
	if err := os.WriteFile(path, []byte("texte"), 0600); err != nil {
		t.Fatal(err)
	}
	call(t, a, "POST", "/api/admin/backups/purge", nil, false, nil, 401)
	callJSON(t, a, "POST", "backups/purge", `{"confirmed":false}`, 400)
	a.backupMu.Lock()
	callJSON(t, a, "POST", "backups/purge", `{"confirmed":true}`, 409)
	a.backupMu.Unlock()
	assertCount(2)
	callJSON(t, a, "PUT", "backup-frequency", `{"frequency":"weekly"}`, 204)
	callJSON(t, a, "POST", "backups/purge", `{"confirmed":true}`, 204)
	assertCount(0)
	playlistIDs(t, a, 1)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	reopened, err := openApp(a.data)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	plan, err := reopened.backupPlan()
	if err != nil || plan.Frequency != "weekly" || plan.lastPurge == 0 {
		t.Fatalf("plan après réouverture : %+v, %v", plan, err)
	}
	if err := reopened.runScheduledBackup(time.Unix(plan.lastPurge, 0).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
	if err := reopened.runScheduledBackup(time.Unix(plan.lastPurge, 0).In(paris).AddDate(0, 0, 7)); err != nil {
		t.Fatal(err)
	}
	assertCount(1)
}
