package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	_ "github.com/mattn/go-sqlite3"
)

func newTestCronManager(t *testing.T, client *ent.Client) *cron.CronManager {
	t.Helper()
	tmpDir := t.TempDir()
	return cron.NewCronManager(client, tmpDir, 24*time.Hour)
}

func newTestCronJobService(t *testing.T) (*CronJobService, *ent.Client, *cron.CronManager) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	manager := newTestCronManager(t, client)

	svc := &CronJobService{
		client:  client,
		manager: manager,
	}
	return svc, client, manager
}

func TestCronCreateCronJob(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "test-job-create",
		Type:       "shell",
		Expression: "0 */5 * * * *",
		Command:    "echo hello",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if resp.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
	if resp.Name != "test-job-create" {
		t.Fatalf("expected name 'test-job-create', got: %s", resp.Name)
	}
	if resp.Type != "shell" {
		t.Fatalf("expected type 'shell', got: %s", resp.Type)
	}
	if !resp.Enabled {
		t.Fatal("expected enabled=true by default")
	}
}

func TestCronCreateCronJob_DuplicateName(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "dup-name",
		Type:       "shell",
		Expression: "0 */5 * * * *",
		Command:    "echo 1",
	})
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	_, err = svc.Create(ctx, &CreateCronJobRequest{
		Name:       "dup-name",
		Type:       "shell",
		Expression: "0 */5 * * * *",
		Command:    "echo 2",
	})
	if err != ErrCronJobNameExists {
		t.Fatalf("expected ErrCronJobNameExists, got: %v", err)
	}
}

func TestCronCreateCronJob_InvalidExpression(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "bad-expr",
		Type:       "shell",
		Expression: "invalid-cron-expr",
		Command:    "echo test",
	})
	if err != ErrCronJobInvalidExpr {
		t.Fatalf("expected ErrCronJobInvalidExpr, got: %v", err)
	}
}

func TestCronUpdateCronJob(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	// Create a job first
	job, err := client.CronJob.Create().
		SetName("update-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo before").
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	newName := "update-test-renamed"
	newCmd := "echo after"
	err = svc.Update(ctx, job.ID, &UpdateCronJobRequest{
		Name:    &newName,
		Command: &newCmd,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify update
	resp, err := svc.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.Name != newName {
		t.Fatalf("expected name %q, got %q", newName, resp.Name)
	}
	if resp.Command != newCmd {
		t.Fatalf("expected command %q, got %q", newCmd, resp.Command)
	}
}

func TestCronUpdateCronJob_NotFound(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	name := "whatever"
	err := svc.Update(ctx, 99999, &UpdateCronJobRequest{Name: &name})
	if err != ErrCronJobNotFound {
		t.Fatalf("expected ErrCronJobNotFound, got: %v", err)
	}
}

func TestCronDeleteCronJob(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("delete-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo delete").
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	err = svc.Delete(ctx, job.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	_, err = svc.Get(ctx, job.ID)
	if err != ErrCronJobNotFound {
		t.Fatalf("expected ErrCronJobNotFound after delete, got: %v", err)
	}
}

func TestCronDeleteCronJob_NotFound(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	err := svc.Delete(ctx, 99999)
	if err != ErrCronJobNotFound {
		t.Fatalf("expected ErrCronJobNotFound, got: %v", err)
	}
}

func TestCronListCronJobs(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	// Create several jobs
	for i := 0; i < 5; i++ {
		_, err := client.CronJob.Create().
			SetName(fmt.Sprintf("list-test-%d", i)).
			SetType(cronjob.TypeShell).
			SetExpression("0 */5 * * * *").
			SetCommand(fmt.Sprintf("echo %d", i)).
			SetEnabled(true).
			Save(ctx)
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	resp, err := svc.List(ctx, &ListCronJobsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if resp.Total != 5 {
		t.Fatalf("expected total 5, got %d", resp.Total)
	}
	if len(resp.Data) != 5 {
		t.Fatalf("expected 5 items, got %d", len(resp.Data))
	}
}

func TestCronListCronJobs_WithKeyword(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	client.CronJob.Create().SetName("alpha-job").SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").SetCommand("echo a").SetEnabled(true).SaveX(ctx)
	client.CronJob.Create().SetName("beta-job").SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").SetCommand("echo b").SetEnabled(true).SaveX(ctx)

	resp, err := svc.List(ctx, &ListCronJobsRequest{Page: 1, PageSize: 10, Keyword: "alpha"})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected 1 result for keyword 'alpha', got %d", resp.Total)
	}
}

func TestCronEnableCronJob(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("enable-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo enable").
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	err = svc.Enable(ctx, job.ID)
	if err != nil {
		t.Fatalf("Enable failed: %v", err)
	}

	// Verify enabled
	resp, err := svc.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !resp.Enabled {
		t.Fatal("expected enabled=true after Enable")
	}
}

func TestCronEnableCronJob_AlreadyEnabled(t *testing.T) {
	svc, client, _ := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("already-enabled").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo test").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Should be a no-op
	err = svc.Enable(ctx, job.ID)
	if err != nil {
		t.Fatalf("Enable on already-enabled job should not fail: %v", err)
	}
}

func TestCronDisableCronJob(t *testing.T) {
	svc, client, manager := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("disable-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo disable").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Add to scheduler first
	_ = manager.AddJob(job)

	err = svc.Disable(ctx, job.ID)
	if err != nil {
		t.Fatalf("Disable failed: %v", err)
	}

	// Verify disabled
	resp, err := svc.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.Enabled {
		t.Fatal("expected enabled=false after Disable")
	}
}

func TestCronGetLogs(t *testing.T) {
	svc, client, manager := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("logs-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo logs").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Manually create a log file
	logger := manager.GetJobLogger()
	f, _, err := logger.CreateLogFile(job.ID)
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	f.WriteString("test log content")
	f.Close()

	logs, err := svc.GetLogs(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetLogs failed: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
}

func TestCronGetLogs_NotFound(t *testing.T) {
	svc, _, _ := newTestCronJobService(t)
	ctx := context.Background()

	_, err := svc.GetLogs(ctx, 99999)
	if err != ErrCronJobNotFound {
		t.Fatalf("expected ErrCronJobNotFound, got: %v", err)
	}
}

func TestCronTailLog(t *testing.T) {
	svc, client, manager := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("tail-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo tail").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Create log file with content
	logger := manager.GetJobLogger()
	f, _, err := logger.CreateLogFile(job.ID)
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
	f.Close()

	result, err := svc.TailLog(ctx, job.ID, 3)
	if err != nil {
		t.Fatalf("TailLog failed: %v", err)
	}

	if result == "" {
		t.Fatal("expected non-empty tail result")
	}
}

func TestCronGetLogContent(t *testing.T) {
	svc, client, manager := newTestCronJobService(t)
	ctx := context.Background()

	job, err := client.CronJob.Create().
		SetName("content-test").
		SetType(cronjob.TypeShell).
		SetExpression("0 */5 * * * *").
		SetCommand("echo content").
		SetEnabled(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Create log file with known content
	logger := manager.GetJobLogger()
	logFile, filename, err := logger.CreateLogFile(job.ID)
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	logFile.WriteString("specific log content")
	logFile.Close()

	// Also create the file directly for ReadLog to read
	// The CreateLogFile opens with O_WRONLY|O_APPEND|O_SYNC, need to verify it wrote
	jobDir := filepath.Join(os.TempDir()) // not needed, use GetLogContent
	_ = jobDir

	content, err := svc.GetLogContent(ctx, job.ID, filename)
	if err != nil {
		t.Fatalf("GetLogContent failed: %v", err)
	}
	if content != "specific log content" {
		t.Fatalf("expected 'specific log content', got: %q", content)
	}
}
