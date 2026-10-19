package models

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/database"
	"server-monitor/internal/testdb"
)

func TestAITaskOptionsMigrationKeepsLegacyTasksInStandardMode(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	task := seedAITask(t, db, user, server)
	if _, err := db.Exec(`ALTER TABLE ai_tasks DROP COLUMN approval_mode, DROP COLUMN options`); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	got, err := GetAITask(context.Background(), db, task.ID, user)
	if err != nil || got.ApprovalMode != "standard" || len(got.Skills) != 0 || len(got.Plugins) != 0 || got.Prompt != task.Prompt || got.Status != task.Status {
		t.Fatalf("legacy task changed authority: mode=%s status=%s err=%v", got.ApprovalMode, got.Status, err)
	}
}

func TestAITaskSelectionRejectsUnknownAuthorityAndDuplicateExtensions(t *testing.T) {
	id := uuid.New()
	for _, selection := range []AITaskSelection{
		{ApprovalMode: "admin"},
		{ApprovalMode: "AUTOMATIC"},
		{SkillIDs: []uuid.UUID{id, id}},
		{PluginIDs: []uuid.UUID{uuid.Nil}},
		{PluginIDs: []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}},
	} {
		if selection.Normalize() == nil {
			t.Errorf("invalid selection accepted: %+v", selection)
		}
	}
	selection := AITaskSelection{}
	if err := selection.Normalize(); err != nil || selection.ApprovalMode != "standard" {
		t.Fatal(selection, err)
	}
}
