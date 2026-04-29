package cron

import (
	"os"
	"testing"
	"time"

	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	_ "github.com/mattn/go-sqlite3"
)

func TestMain(m *testing.M) {
	// Fix RefTable on foreign keys (not set by ent codegen)
	migrate.UserRolesTable.ForeignKeys[0].RefTable = migrate.UsersTable
	migrate.UserRolesTable.ForeignKeys[1].RefTable = migrate.RolesTable
	migrate.AttachmentsTable.ForeignKeys[0].RefTable = migrate.AttachmentGroupsTable
	os.Exit(m.Run())
}

func TestNewCronManager(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	if manager == nil {
		t.Fatal("NewCronManager returned nil")
	}
	if manager.client != client {
		t.Fatal("client not set correctly")
	}
	if manager.logger == nil {
		t.Fatal("logger not initialized")
	}
	if len(manager.executors) != 3 {
		t.Fatalf("expected 3 executors, got %d", len(manager.executors))
	}
}

func TestAddAndRemoveJob(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	// Create a CronJob in DB
	job, err := client.CronJob.Create().
		SetName("test-add-remove").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo test").
		SetEnabled(true).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create cronjob failed: %v", err)
	}

	// Add job to scheduler
	err = manager.AddJob(job)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Verify job is in the jobs map
	if _, ok := manager.jobs.Load(job.ID); !ok {
		t.Fatal("job should be in jobs map after AddJob")
	}

	// Remove job
	manager.RemoveJob(job.ID)

	// Verify job is removed from map
	if _, ok := manager.jobs.Load(job.ID); ok {
		t.Fatal("job should not be in jobs map after RemoveJob")
	}
}

func TestRunNow(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	// Create a CronJob in DB
	job, err := client.CronJob.Create().
		SetName("test-run-now").
		SetType(cronjob.TypeShell).
		SetExpression("0 0 0 * * *").
		SetCommand("echo run-now-test").
		SetEnabled(true).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create cronjob failed: %v", err)
	}

	// Run the job immediately
	err = manager.RunNow(job.ID)
	if err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}

	// Wait a bit for the goroutine to execute
	time.Sleep(500 * time.Millisecond)

	// Verify log file was created
	logs, err := manager.logger.ListLogs(job.ID)
	if err != nil {
		t.Fatalf("ListLogs failed: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected at least one log file after RunNow")
	}
}

func TestRunNow_NotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	err := manager.RunNow(99999)
	if err == nil {
		t.Fatal("expected error for non-existent job")
	}
}

func TestStart_LoadsEnabledJobs(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	ctx := t.Context()

	// Create an enabled job
	enabledJob, err := client.CronJob.Create().
		SetName("enabled-job").
		SetType(cronjob.TypeShell).
		SetExpression("0 */10 * * * *").
		SetCommand("echo enabled").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create enabled job failed: %v", err)
	}

	// Create a disabled job
	_, err = client.CronJob.Create().
		SetName("disabled-job").
		SetType(cronjob.TypeShell).
		SetExpression("0 */10 * * * *").
		SetCommand("echo disabled").
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("create disabled job failed: %v", err)
	}

	// Start manager
	err = manager.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer manager.Stop()

	// Verify only enabled job is loaded
	if _, ok := manager.jobs.Load(enabledJob.ID); !ok {
		t.Fatal("enabled job should be in jobs map after Start")
	}

	// Count loaded jobs
	count := 0
	manager.jobs.Range(func(key, value any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected 1 loaded job, got %d", count)
	}
}

func TestGetJobLogger(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	manager := NewCronManager(client, tmpDir, 24*time.Hour)

	logger := manager.GetJobLogger()
	if logger == nil {
		t.Fatal("GetJobLogger should not return nil")
	}
}
