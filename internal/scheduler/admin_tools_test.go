package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newAdminDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("new test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestScheduleCreateAndList(t *testing.T) {
	db := newAdminDB(t)
	ctx := context.Background()

	res, err := scheduler.ScheduleCreateHandler(ctx, db, nil, nil, scheduler.ScheduleCreateArgs{
		Name:    "prune",
		Kind:    scheduler.KindInterval,
		Spec:    "5m",
		Command: "/bin/echo",
		Args:    []string{"hi"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Job.ID == "" || res.Job.Surface != "schedule" || !res.Job.Enabled {
		t.Errorf("unexpected job: %+v", res.Job)
	}
	if res.Job.NextRunAt == nil {
		t.Error("next_run_at not set on interval kind")
	}

	listed, err := scheduler.ScheduleListHandler(ctx, db, scheduler.ScheduleListArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Jobs) != 1 {
		t.Fatalf("list len = %d, want 1", len(listed.Jobs))
	}
	if listed.Jobs[0].Name != "prune" {
		t.Errorf("name = %q", listed.Jobs[0].Name)
	}

	// Filter: enabled_only excludes disabled rows.
	disabled := false
	_, err = scheduler.ScheduleCreateHandler(ctx, db, nil, nil, scheduler.ScheduleCreateArgs{
		Name:    "off",
		Kind:    scheduler.KindInterval,
		Spec:    "1h",
		Command: "/bin/echo",
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	all, _ := scheduler.ScheduleListHandler(ctx, db, scheduler.ScheduleListArgs{})
	if len(all.Jobs) != 2 {
		t.Fatalf("all len = %d, want 2", len(all.Jobs))
	}
	on, _ := scheduler.ScheduleListHandler(ctx, db, scheduler.ScheduleListArgs{EnabledOnly: true})
	if len(on.Jobs) != 1 {
		t.Fatalf("enabled-only len = %d, want 1", len(on.Jobs))
	}
}

func TestScheduleCreateInvalidSpec(t *testing.T) {
	db := newAdminDB(t)
	_, err := scheduler.ScheduleCreateHandler(context.Background(), db, nil, nil,
		scheduler.ScheduleCreateArgs{
			Name:    "bogus",
			Kind:    scheduler.KindCron,
			Spec:    "definitely not a cron expression",
			Command: "/bin/echo",
		})
	if err == nil {
		t.Fatal("expected error for invalid cron spec")
	}
}

func TestScheduleCreateMissingFields(t *testing.T) {
	db := newAdminDB(t)
	for _, args := range []scheduler.ScheduleCreateArgs{
		{Kind: scheduler.KindInterval, Spec: "5m", Command: "x"}, // no name
		{Name: "x", Spec: "5m", Command: "x"},                    // no kind
		{Name: "x", Kind: scheduler.KindInterval, Spec: "5m"},    // no command
	} {
		if _, err := scheduler.ScheduleCreateHandler(context.Background(), db, nil, nil, args); err == nil {
			t.Errorf("expected validation error for %+v", args)
		}
	}
}

func TestScheduleDelete(t *testing.T) {
	db := newAdminDB(t)
	ctx := context.Background()
	res, _ := scheduler.ScheduleCreateHandler(ctx, db, nil, nil, scheduler.ScheduleCreateArgs{
		Name: "tmp", Kind: scheduler.KindInterval, Spec: "5m", Command: "/bin/echo",
	})
	del, err := scheduler.ScheduleDeleteHandler(ctx, db, nil, nil, scheduler.ScheduleDeleteArgs{ID: res.Job.ID})
	if err != nil || !del.Deleted {
		t.Fatalf("delete: %v deleted=%v", err, del.Deleted)
	}
	if _, err := db.GetScheduledJob(ctx, res.Job.ID); err != store.ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestScheduleDeleteEmptyID(t *testing.T) {
	db := newAdminDB(t)
	if _, err := scheduler.ScheduleDeleteHandler(context.Background(), db, nil, nil,
		scheduler.ScheduleDeleteArgs{}); err == nil {
		t.Error("expected error for empty id")
	}
}

func TestScheduleCreateEventDrivenKind(t *testing.T) {
	db := newAdminDB(t)
	// file_watch is OK at create time even though NextRun returns
	// ErrEventDrivenKind — the handler must NOT treat it as fatal.
	res, err := scheduler.ScheduleCreateHandler(context.Background(), db, nil, nil,
		scheduler.ScheduleCreateArgs{
			Name: "watch", Kind: scheduler.KindFileWatch, Spec: "/tmp",
			Command: "/bin/echo",
		})
	if err != nil {
		t.Fatalf("create file_watch: %v", err)
	}
	if res.Job.NextRunAt != nil {
		t.Errorf("file_watch should not seed next_run_at: %v", res.Job.NextRunAt)
	}
}

// fakeDriver lets the create-with-native test assert ordering.
type fakeDriver struct {
	available   bool
	installed   string
	uninstalled string
	installFail bool
}

func (f *fakeDriver) Name() string    { return "fake_driver" }
func (f *fakeDriver) Available() bool { return f.available }
func (f *fakeDriver) Install(_ context.Context, j store.ScheduledJob) (string, error) {
	if f.installFail {
		return "", errFakeInstall
	}
	f.installed = "native-" + j.ID
	return f.installed, nil
}
func (f *fakeDriver) Uninstall(_ context.Context, id string) error {
	f.uninstalled = id
	return nil
}

var errFakeInstall = stringErr("install failed")

type stringErr string

func (e stringErr) Error() string { return string(e) }

func TestScheduleCreateWithSurviveDaemonDown(t *testing.T) {
	db := newAdminDB(t)
	d := &fakeDriver{available: true}
	res, err := scheduler.ScheduleCreateHandler(context.Background(), db, nil, d,
		scheduler.ScheduleCreateArgs{
			Name: "promoted", Kind: scheduler.KindInterval, Spec: "5m",
			Command: "/bin/echo", SurviveDaemonDown: true,
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Job.NativeDriver != "fake_driver" || res.Job.NativeID == "" {
		t.Errorf("native promotion not recorded: %+v", res.Job)
	}
	if d.installed == "" {
		t.Error("driver Install never called")
	}
	// Delete should call Uninstall.
	_, _ = scheduler.ScheduleDeleteHandler(context.Background(), db, nil, d,
		scheduler.ScheduleDeleteArgs{ID: res.Job.ID})
	if d.uninstalled != d.installed {
		t.Errorf("Uninstall got %q, want %q", d.uninstalled, d.installed)
	}
}

func TestScheduleCreateRollsBackOnDriverFail(t *testing.T) {
	db := newAdminDB(t)
	// Driver succeeds but DB create will fail because we re-use a name twice
	// after pre-inserting a row. Simpler: driver itself fails install.
	d := &fakeDriver{available: true, installFail: true}
	_, err := scheduler.ScheduleCreateHandler(context.Background(), db, nil, d,
		scheduler.ScheduleCreateArgs{
			Name: "bad", Kind: scheduler.KindInterval, Spec: "5m",
			Command: "/bin/echo", SurviveDaemonDown: true,
		})
	if err == nil {
		t.Fatal("expected install error")
	}
	listed, _ := scheduler.ScheduleListHandler(context.Background(), db, scheduler.ScheduleListArgs{})
	if len(listed.Jobs) != 0 {
		t.Errorf("DB should be empty after rollback, has %d rows", len(listed.Jobs))
	}
}

// Sanity check: clock-injected create gives deterministic NextRunAt.
func TestScheduleCreateUsesSchedulerClock(t *testing.T) {
	db := newAdminDB(t)
	clk := newFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := scheduler.New(db, nil, nil, clk)
	res, err := scheduler.ScheduleCreateHandler(context.Background(), db, s, nil,
		scheduler.ScheduleCreateArgs{
			Name: "fixed", Kind: scheduler.KindInterval, Spec: "10m", Command: "/bin/echo",
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wantNext := clk.Now().Add(10 * time.Minute)
	if res.Job.NextRunAt == nil || !res.Job.NextRunAt.Equal(wantNext) {
		t.Errorf("next_run_at = %v, want %v", res.Job.NextRunAt, wantNext)
	}
}

// fixedClock is a tiny Clock that never advances. Local to this test
// file so it doesn't collide with the in-package fakeClock.
type fixedClock struct{ t time.Time }

func newFixedClock(t time.Time) *fixedClock { return &fixedClock{t: t} }
func (c *fixedClock) Now() time.Time        { return c.t }
func (c *fixedClock) NewTimer(d time.Duration) scheduler.Timer {
	return &nopTimer{c: make(chan time.Time)}
}

type nopTimer struct{ c chan time.Time }

func (n *nopTimer) C() <-chan time.Time { return n.c }
func (n *nopTimer) Stop() bool          { return true }
