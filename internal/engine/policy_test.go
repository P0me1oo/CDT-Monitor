package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/notify"
	"github.com/P0me1oo/CDT-Monitor/internal/store"
)

func TestDueWithinSupportsLateScheduler(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 7, 19, 8, 7, 0, 0, location)
	if !dueWithin(now, "08:00", 10*time.Minute) {
		t.Fatal("expected delayed scheduler to compensate")
	}
	if dueWithin(now, "07:50", 10*time.Minute) {
		t.Fatal("must not compensate outside the window")
	}
}

func TestInTimeRangeAcrossMidnight(t *testing.T) {
	if !inTimeRange("23:30", "22:00", "06:00") || !inTimeRange("05:59", "22:00", "06:00") {
		t.Fatal("cross-midnight window should include night times")
	}
	if inTimeRange("12:00", "22:00", "06:00") {
		t.Fatal("cross-midnight window must exclude midday")
	}
}

func TestScheduleCycleDateAcrossMidnight(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	start := time.Date(2026, 9, 24, 8, 0, 0, 0, location)
	stop := time.Date(2026, 9, 25, 0, 34, 0, 0, location)
	if got := scheduleCycleDate(start, "08:00", "00:34"); got != "2026-09-24" {
		t.Fatalf("start action belongs to %s, got %s", "2026-09-24", got)
	}
	if got := scheduleCycleDate(stop, "08:00", "00:34"); got != "2026-09-24" {
		t.Fatalf("overnight stop should use previous cycle date, got %s", got)
	}
	if got := scheduleCycleDate(time.Date(2026, 9, 25, 8, 0, 0, 0, location), "08:00", "00:34"); got != "2026-09-25" {
		t.Fatalf("next start should begin a new cycle, got %s", got)
	}
}

func TestDueWithinNormalizesFullWidthColon(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	if !dueWithin(time.Date(2026, 9, 25, 0, 38, 0, 0, location), "00：34", 10*time.Minute) {
		t.Fatal("expected normalized midnight schedule to be due")
	}
}

func TestScheduleActionKeyIncludesConfiguredTime(t *testing.T) {
	account := domain.Account{ID: 7, StartTime: "08:00", StopTime: "00:34"}
	oldKey := scheduleActionKey(domain.Account{ID: 7, StartTime: "08:00", StopTime: "00:00"}, "2026-09-24", "stop")
	newKey := scheduleActionKey(account, "2026-09-24", "stop")
	if oldKey == newKey {
		t.Fatalf("changing stop time must produce a new idempotency key: %q", newKey)
	}
	if newKey != "schedule:7:20260924:stop:00:34" {
		t.Fatalf("unexpected schedule key: %q", newKey)
	}
	if scheduleActionKey(account, "2026-09-24", "stop") != newKey {
		t.Fatal("same schedule configuration must remain idempotent")
	}
}

func TestDailyReportKeyIncludesScheduleStopTime(t *testing.T) {
	account := domain.Account{ID: 7, ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:34"}
	oldKey := dailyReportKey(domain.Account{ID: 7, ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:00"}, "2026-09-24")
	newKey := dailyReportKey(account, "2026-09-24")
	if oldKey == newKey {
		t.Fatalf("changing stop time must produce a new report key: %q", newKey)
	}
	if newKey != "daily_report:7:20260924:00:34" {
		t.Fatalf("unexpected daily report key: %q", newKey)
	}
}

func TestScheduledConsumptionUsesOvernightCycleSnapshot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	trueVal := true
	if err = st.Setup(ctx, domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai",
		Accounts: []domain.Account{{AccessKeyID: "LTAI_OVERNIGHT", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-overnight", MaxTraffic: 200, ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:34", DailyReport: &trueVal}},
	}); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 9, 25, 0, 34, 0, 0, location)
	if err = st.UpdateRuntime(ctx, accounts[0].ID, 15, domain.StatusStopped, now); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordTrafficSnapshot(ctx, accounts[0].ID, "2026-09-24", "start", 10, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordTrafficSnapshot(ctx, accounts[0].ID, "2026-09-24", "stop", 15, "00:34"); err != nil {
		t.Fatal(err)
	}
	eng := New(st, nil, notify.New(), nil, 1)
	consumed, period := eng.calculateInstanceConsumption(ctx, accounts[0], now, "2026-09-25", false)
	if consumed != 5 || period != "定时时段 (08:00 ~ 00:34)" {
		t.Fatalf("got consumed=%v period=%q", consumed, period)
	}
}

func TestScheduledConsumptionFallsBackWhenSnapshotIsMissing(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 9, 25, 0, 10, 0, 0, loc)
	start := time.Date(2026, 9, 24, 8, 0, 0, 0, loc)
	acc := domain.Account{ID: 101, AccessKeyID: "LTAI_SNAPSHOT", ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:10", TrafficUsed: 54.44}
	if err = st.AddTrafficStats(ctx, acc.ID, 51.98, start); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTrafficStats(ctx, acc.ID, 54.44, now); err != nil {
		t.Fatal(err)
	}
	eng := New(st, nil, notify.New(), nil, 1)
	assertConsumed := func(stage string) {
		t.Helper()
		consumed, period := eng.calculateInstanceConsumption(ctx, acc, now, "2026-09-25", false)
		if consumed != 2.46 || period != "定时时段 (08:00 ~ 00:10)" {
			t.Fatalf("%s: consumed=%v period=%q, want 2.46 GB", stage, consumed, period)
		}
	}
	assertConsumed("no snapshot")
	stopOnly := acc
	stopOnly.ID++
	if err = st.AddTrafficStats(ctx, stopOnly.ID, 51.98, start); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordTrafficSnapshot(ctx, stopOnly.ID, "2026-09-24", "stop", 54.44, "00:10"); err != nil {
		t.Fatal(err)
	}
	if consumed, _ := eng.calculateInstanceConsumption(ctx, stopOnly, now, "2026-09-25", false); consumed != 2.46 {
		t.Fatalf("stop snapshot only: consumed=%v, want 2.46 GB", consumed)
	}
	if err = st.RecordTrafficSnapshot(ctx, acc.ID, "2026-09-24", "start", 51.98, "08:00"); err != nil {
		t.Fatal(err)
	}
	assertConsumed("start snapshot only")
	if err = st.RecordTrafficSnapshot(ctx, acc.ID, "2026-09-24", "stop", 54.44, "00:10"); err != nil {
		t.Fatal(err)
	}
	assertConsumed("complete snapshot")
}

func TestScheduledReportWaitsForStopMonitor(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	cfg := domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai", EnableDailyReport: true,
		Accounts: []domain.Account{
			{AccessKeyID: "LTAI_SCHEDULED", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-scheduled", ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:10"},
			{AccessKeyID: "LTAI_UNSCHEDULED", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-unscheduled", DailyReportTime: "00:10"},
		},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	eng := New(st, nil, notify.New(), nil, 1)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	eng.checkDailyReport(ctx, time.Date(2026, 9, 25, 0, 10, 0, 0, loc))
	job, err := st.ClaimJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.Type != JobDailyReport || job.AccountID != accounts[1].ID {
		t.Fatalf("unexpected early report job: %+v", job)
	}
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scheduled account should wait for its stop monitor, got %v", err)
	}
}

func TestScheduledReportRecoversAfterStopSnapshot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	cfg := domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai", EnableDailyReport: true,
		Accounts: []domain.Account{{
			AccessKeyID: "LTAI_RECOVERY", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-recovery",
			ScheduleEnabled: true, StartTime: "08:00", StopTime: "00:10",
		}},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 9, 25, 0, 10, 0, 0, loc)
	eng := New(st, nil, notify.New(), nil, 1)
	if err = st.RecordTrafficSnapshot(ctx, accounts[0].ID, "2026-09-24", "stop", 54.44, "23:00"); err != nil {
		t.Fatal(err)
	}
	eng.checkDailyReport(ctx, now)
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale stop snapshot should not queue a report: %v", err)
	}
	if err = st.RecordTrafficSnapshot(ctx, accounts[0].ID, "2026-09-24", "stop", 54.44, "00:10"); err != nil {
		t.Fatal(err)
	}
	eng.checkDailyReport(ctx, now)
	eng.checkDailyReport(ctx, now)
	job, err := st.ClaimJob(ctx)
	if err != nil || job.Type != JobDailyReport || job.AccountID != accounts[0].ID {
		t.Fatalf("expected one recovered report job, got %+v err=%v", job, err)
	}
	if job.MaxAttempts != 12 {
		t.Fatalf("scheduled report should retry transient API failures, got %d attempts", job.MaxAttempts)
	}
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate report job: %v", err)
	}
}

func TestDailyReportGroupsOnlyUnscheduledAccountsAtSameTime(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	falseValue := false
	if err = st.Setup(ctx, domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai", EnableDailyReport: true,
		Accounts: []domain.Account{
			{AccessKeyID: "LTAI_GROUP_A", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "group-a", DailyReportTime: "22:00"},
			{AccessKeyID: "LTAI_GROUP_B", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "group-b", DailyReportTime: "22：00"},
			{AccessKeyID: "LTAI_LATER", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "later", DailyReportTime: "22:05"},
			{AccessKeyID: "LTAI_DISABLED", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "disabled", DailyReportTime: "22:00", DailyReport: &falseValue},
			{AccessKeyID: "LTAI_STOP_A", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "stop-a", ScheduleEnabled: true, StartTime: "08:00", StopTime: "22:00"},
			{AccessKeyID: "LTAI_STOP_B", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "stop-b", ScheduleEnabled: true, StartTime: "08:00", StopTime: "22:00"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byInstance := make(map[string]int64, len(accounts))
	for _, acc := range accounts {
		byInstance[acc.InstanceID] = acc.ID
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 9, 25, 22, 0, 0, 0, location)
	for _, name := range []string{"stop-a", "stop-b"} {
		if err = st.RecordTrafficSnapshot(ctx, byInstance[name], "2026-09-25", "stop", 15, "22:00"); err != nil {
			t.Fatal(err)
		}
	}
	eng := New(st, nil, notify.New(), nil, 1)
	eng.checkDailyReport(ctx, now)
	eng.checkDailyReport(ctx, now)

	groupCount := 0
	scheduled := make(map[int64]bool)
	for {
		job, claimErr := st.ClaimJob(ctx)
		if errors.Is(claimErr, sql.ErrNoRows) {
			break
		}
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if job.Type != JobDailyReport {
			t.Fatalf("unexpected job: %+v", job)
		}
		var payload struct {
			AccountID  int64   `json:"account_id"`
			AccountIDs []int64 `json:"account_ids"`
		}
		if err = json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if job.AccountID == 0 {
			groupCount++
			if len(payload.AccountIDs) != 2 || payload.AccountIDs[0] != byInstance["group-a"] || payload.AccountIDs[1] != byInstance["group-b"] {
				t.Fatalf("unexpected grouped accounts: %v", payload.AccountIDs)
			}
		} else {
			scheduled[job.AccountID] = true
			if payload.AccountID != job.AccountID {
				t.Fatalf("scheduled report must remain individual: %+v", job)
			}
		}
	}
	if groupCount != 1 || len(scheduled) != 2 || !scheduled[byInstance["stop-a"]] || !scheduled[byInstance["stop-b"]] {
		t.Fatalf("wanted one ordinary group and two scheduled reports, got groups=%d scheduled=%v", groupCount, scheduled)
	}
	for _, acc := range accounts {
		if acc.InstanceID == "group-b" {
			if err = st.UpdateAccountSettings(ctx, acc.ID, acc.KeepAlive, acc.ShutdownMode, acc.ScheduleEnabled, acc.StartTime, acc.StopTime, &falseValue, acc.DailyReportTime); err != nil {
				t.Fatal(err)
			}
		}
	}
	eng.checkDailyReport(ctx, now.Add(time.Minute))
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("remaining group member must not receive a duplicate singleton report: %v", err)
	}

	eng.checkDailyReport(ctx, now.Add(5*time.Minute))
	job, err := st.ClaimJob(ctx)
	if err != nil || job.AccountID != byInstance["later"] {
		t.Fatalf("different report time must remain individual: job=%+v err=%v", job, err)
	}
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate or early report job: %v", err)
	}
	trueValue := true
	for _, acc := range accounts {
		if acc.InstanceID == "disabled" {
			if err = st.UpdateAccountSettings(ctx, acc.ID, acc.KeepAlive, acc.ShutdownMode, acc.ScheduleEnabled, acc.StartTime, acc.StopTime, &trueValue, "22:05"); err != nil {
				t.Fatal(err)
			}
		}
	}
	eng.checkDailyReport(ctx, now.Add(6*time.Minute))
	job, err = st.ClaimJob(ctx)
	if err != nil || job.AccountID != byInstance["disabled"] {
		t.Fatalf("new group member must not repeat an already sent member: job=%+v err=%v", job, err)
	}
	if _, err = st.ClaimJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected repeated report after group membership changed: %v", err)
	}
}

func TestGroupedDailyReportIncludesOnlySelectedAccounts(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	if err = st.Setup(ctx, domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai", EnableDailyReport: true,
		Notifications: domain.NotificationConfig{Webhook: domain.WebhookConfig{Enabled: true, URL: "https://webhook.example.com/test"}},
		Accounts: []domain.Account{
			{AccessKeyID: "LTAI_A", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-a", Remark: "分组甲", MaxTraffic: 100, DailyReportTime: "22:00"},
			{AccessKeyID: "LTAI_B", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-b", Remark: "分组乙", MaxTraffic: 100, DailyReportTime: "22:00"},
			{AccessKeyID: "LTAI_C", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-c", Remark: "其他时间", MaxTraffic: 100, DailyReportTime: "23:00"},
			{AccessKeyID: "LTAI_D", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-d", Remark: "定时关机", MaxTraffic: 100, ScheduleEnabled: true, StartTime: "08:00", StopTime: "22:00"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, acc := range accounts {
		if err = st.UpdateRuntime(ctx, acc.ID, 10, domain.StatusRunning, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	eng := New(st, nil, notify.New(), nil, 1)
	payload, _ := json.Marshal(map[string]any{"force": false, "account_ids": []int64{accounts[0].ID, accounts[1].ID}, "report_date": "2026-09-25"})
	groupJob := domain.Job{ID: "selected-report-retry", Type: JobDailyReport, Payload: string(payload)}
	if _, err = eng.runJob(ctx, groupJob); err != nil {
		t.Fatal(err)
	}
	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatal(err)
	}
	if event.Fields["纳入实例"] != "2 台" || event.Fields["统计日期"] != "2026-09-25" {
		t.Fatalf("unexpected grouped report fields: %+v", event.Fields)
	}
	if !strings.Contains(event.Summary, "分组甲") || !strings.Contains(event.Summary, "分组乙") || strings.Contains(event.Summary, "其他时间") || strings.Contains(event.Summary, "定时关机") {
		t.Fatalf("report contains wrong accounts: %s", event.Summary)
	}
	if _, err = eng.runJob(ctx, groupJob); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimOutbox(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry must not duplicate the grouped notification: %v", err)
	}
}

func TestGroupedDailyReportKeepsQueuedMembershipAfterSettingsChange(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	if err = st.Setup(ctx, domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging",
		ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai", EnableDailyReport: true,
		Notifications: domain.NotificationConfig{Webhook: domain.WebhookConfig{Enabled: true, URL: "https://webhook.example.com/test"}},
		Accounts: []domain.Account{
			{AccessKeyID: "LTAI_CHANGE_A", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-change-a", Remark: "保留实例", MaxTraffic: 100, DailyReportTime: "22:00"},
			{AccessKeyID: "LTAI_CHANGE_B", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-change-b", Remark: "关闭日报", MaxTraffic: 100, DailyReportTime: "22:00"},
			{AccessKeyID: "LTAI_CHANGE_C", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-change-c", Remark: "后来改时", MaxTraffic: 100, DailyReportTime: "23:00"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, acc := range accounts {
		if err = st.UpdateRuntime(ctx, acc.ID, 10, domain.StatusRunning, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	falseValue := false
	if err = st.UpdateAccountSettings(ctx, accounts[1].ID, accounts[1].KeepAlive, accounts[1].ShutdownMode, false, "", "", &falseValue, "22:00"); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateAccountSettings(ctx, accounts[2].ID, accounts[2].KeepAlive, accounts[2].ShutdownMode, false, "", "", nil, "22:00"); err != nil {
		t.Fatal(err)
	}
	eng := New(st, nil, notify.New(), nil, 1)
	payload, _ := json.Marshal(map[string]any{"force": false, "account_ids": []int64{accounts[0].ID, accounts[1].ID}, "report_date": "2026-09-25"})
	if _, err = eng.runJob(ctx, domain.Job{ID: "queued-membership", Type: JobDailyReport, Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatal(err)
	}
	if event.AccountID != accounts[0].ID || event.Fields["实例名称"] != "保留实例" || strings.Contains(event.Summary, "关闭日报") || strings.Contains(event.Summary, "后来改时") {
		t.Fatalf("queued group should fall back to one remaining member: %+v", event)
	}
}

func TestUsagePercent(t *testing.T) {
	if value := usagePercent(95, 200); value != 47.5 {
		t.Fatalf("got %v", value)
	}
	if value := usagePercent(1, 0); value != 0 {
		t.Fatalf("zero quota got %v", value)
	}
}

func TestRegionNameIncludesSeoul(t *testing.T) {
	if name := RegionName("ap-northeast-2"); name != "韩国（首尔）" {
		t.Fatalf("got %q", name)
	}
}

func TestResolveKeepAliveAndShutdownMode(t *testing.T) {
	trueVal := true
	falseVal := false

	cfg := domain.Config{KeepAlive: true, ShutdownMode: "KeepCharging"}

	// Inherit
	accDefault := domain.Account{}
	if !resolveKeepAlive(accDefault, cfg) {
		t.Fatal("expected default account to inherit keep_alive=true")
	}
	if resolveShutdownMode(accDefault, cfg) != "KeepCharging" {
		t.Fatal("expected default account to inherit shutdown_mode=KeepCharging")
	}

	// Override keep alive
	accOverrideKA := domain.Account{KeepAlive: &falseVal}
	if resolveKeepAlive(accOverrideKA, cfg) {
		t.Fatal("expected account to override keep_alive=false")
	}

	// Override shutdown mode
	accOverrideSM := domain.Account{ShutdownMode: "StopCharging"}
	if resolveShutdownMode(accOverrideSM, cfg) != "StopCharging" {
		t.Fatal("expected account to override shutdown_mode=StopCharging")
	}

	// Global false, instance true
	cfgFalse := domain.Config{KeepAlive: false, ShutdownMode: "StopCharging"}
	accEnableKA := domain.Account{KeepAlive: &trueVal}
	if !resolveKeepAlive(accEnableKA, cfgFalse) {
		t.Fatal("expected account to override keep_alive=true")
	}
}

func TestDailyReportGenerationAndExclusion(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()

	trueVal := true
	falseVal := false

	cfg := domain.Config{
		AdminPassword:     "Strong-Password-42!",
		TrafficThreshold:  95,
		ShutdownMode:      "KeepCharging",
		ThresholdAction:   "stop_and_notify",
		APIInterval:       600,
		Timezone:          "Asia/Shanghai",
		EnableDailyReport: true,
		DailyReportTime:   "22:00",
		Notifications: domain.NotificationConfig{
			Webhook: domain.WebhookConfig{
				Enabled: true,
				URL:     "https://webhook.example.com/test",
			},
		},
		Accounts: []domain.Account{
			{
				AccessKeyID:     "LTAI1",
				AccessKeySecret: "sec1",
				RegionID:        "cn-hongkong",
				InstanceID:      "i-hk",
				MaxTraffic:      200,
				Remark:          "香港节点",
				ScheduleEnabled: true,
				StartTime:       "08:00",
				StopTime:        "22:00",
				DailyReport:     &trueVal, // Included
			},
			{
				AccessKeyID:     "LTAI2",
				AccessKeySecret: "sec2",
				RegionID:        "ap-northeast-1",
				InstanceID:      "i-jp",
				MaxTraffic:      100,
				Remark:          "东京节点",
				DailyReport:     nil, // Default -> Included
			},
			{
				AccessKeyID:     "LTAI3",
				AccessKeySecret: "sec3",
				RegionID:        "us-west-1",
				InstanceID:      "i-us",
				MaxTraffic:      300,
				Remark:          "硅谷节点",
				DailyReport:     &falseVal, // Excluded!
			},
		},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	eng := New(st, nil, notify.New(), nil, 1)

	// Set traffic for accounts
	accs, _ := st.ListAccounts(ctx)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	// Record snapshots
	_ = st.UpdateRuntime(ctx, accs[0].ID, 15.0, domain.StatusStopped, now)
	_ = st.RecordTrafficSnapshot(ctx, accs[0].ID, now.Format("2006-01-02"), "start", 10.0, "08:00")
	_ = st.RecordTrafficSnapshot(ctx, accs[0].ID, now.Format("2006-01-02"), "stop", 14.5, "22:00")

	_ = st.UpdateRuntime(ctx, accs[1].ID, 8.0, domain.StatusRunning, now)
	// For accs[1] (non-scheduled), baseline traffic 24 hours ago = 5.0
	t24Ago := now.Add(-24 * time.Hour)
	_ = st.AddTrafficStats(ctx, accs[1].ID, 5.0, t24Ago)
	_ = st.AddTrafficStats(ctx, accs[1].ID, 8.0, now)

	_ = st.UpdateRuntime(ctx, accs[2].ID, 20.0, domain.StatusRunning, now)

	result, err := eng.generateAndSendDailyReport(ctx, false)
	if err != nil {
		t.Fatalf("generateAndSendDailyReport failed: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result message")
	}

	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatalf("expected daily report in outbox: %v", err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if event.Type != "daily_report" {
		t.Fatalf("event.Type = %s, want daily_report", event.Type)
	}
	if event.Fields["纳入实例"] != "2 台" {
		t.Fatalf("expected 2 included accounts, got %s", event.Fields["纳入实例"])
	}
	if event.Fields["排除实例"] != "1 台" {
		t.Fatalf("expected 1 excluded account, got %s", event.Fields["排除实例"])
	}

	// Normal reports use the configured mode: the scheduled instance uses its
	// start/stop snapshots (14.5 - 10 = 4.5 GB), while the other uses 8 - 5 = 3 GB.
	if event.Fields["流量消耗总和"] != "7.50 GB" {
		t.Fatalf("expected 7.50 GB total consumed, got %s", event.Fields["流量消耗总和"])
	}
}

func TestDailyReportTestPushUsesRolling24HourWindow(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	trueVal := true
	cfg := domain.Config{
		AdminPassword:     "Strong-Password-42!",
		TrafficThreshold:  95,
		ShutdownMode:      "KeepCharging",
		ThresholdAction:   "stop_and_notify",
		APIInterval:       600,
		Timezone:          "Asia/Shanghai",
		EnableDailyReport: true,
		Notifications:     domain.NotificationConfig{Webhook: domain.WebhookConfig{Enabled: true, URL: "https://webhook.example.com/test"}},
		Accounts: []domain.Account{{
			AccessKeyID: "LTAI_TEST_WINDOW", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-test-window",
			MaxTraffic: 200, ScheduleEnabled: true, StartTime: "08:00", StopTime: "22:00", DailyReport: &trueVal,
		}},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	accs, err := st.ListAccounts(ctx)
	if err != nil || len(accs) != 1 {
		t.Fatalf("accounts=%v err=%v", accs, err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	if err = st.UpdateRuntime(ctx, accs[0].ID, 15, domain.StatusRunning, now); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordTrafficSnapshot(ctx, accs[0].ID, now.Format("2006-01-02"), "start", 10, "08:00"); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordTrafficSnapshot(ctx, accs[0].ID, now.Format("2006-01-02"), "stop", 14.5, "22:00"); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTrafficStats(ctx, accs[0].ID, 5, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	eng := New(st, nil, notify.New(), nil, 1)
	if _, err = eng.generateAndSendDailyReport(ctx, true, accs[0].ID); err != nil {
		t.Fatal(err)
	}
	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatal(err)
	}
	if event.Fields["消耗流量"] != "10.00 GB" {
		t.Fatalf("expected 10.00 GB for test push, got %q", event.Fields["消耗流量"])
	}
	if event.Fields["运行模式"] != "测试推送（前24小时）" {
		t.Fatalf("unexpected test push period: %q", event.Fields["运行模式"])
	}
}

func TestDailyReportSmallAmountPrecisionAndSingleInstance(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()

	cfg := domain.Config{
		AdminPassword:     "Strong-Password-42!",
		TrafficThreshold:  95,
		ShutdownMode:      "KeepCharging",
		ThresholdAction:   "stop_and_notify",
		APIInterval:       600,
		Timezone:          "Asia/Shanghai",
		EnableDailyReport: true,
		Notifications: domain.NotificationConfig{
			Webhook: domain.WebhookConfig{
				Enabled: true,
				URL:     "https://webhook.example.com/test",
			},
		},
		Accounts: []domain.Account{
			{
				AccessKeyID:     "LTAI_PRECISION",
				AccessKeySecret: "sec",
				RegionID:        "cn-hongkong",
				InstanceID:      "i-precision",
				MaxTraffic:      200,
				Remark:          "高精度测试节点",
				ScheduleEnabled: false,
				DailyReportTime: "00:00",
			},
		},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	eng := New(st, nil, notify.New(), nil, 1)
	accs, _ := st.ListAccounts(ctx)
	acc := accs[0]

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	t24Ago := now.Add(-24 * time.Hour)

	// Baseline 24 hours ago was 1.000 GB, current is 1.292 GB -> consumed = 0.292 GB
	_ = st.AddTrafficStats(ctx, acc.ID, 1.000, t24Ago)
	_ = st.UpdateRuntime(ctx, acc.ID, 1.292, domain.StatusRunning, now)

	result, err := eng.generateAndSendDailyReport(ctx, true, acc.ID)
	if err != nil {
		t.Fatalf("generateAndSendDailyReport failed: %v", err)
	}
	if !strings.Contains(result, "0.292 GB") {
		t.Fatalf("expected result message to contain 0.292 GB, got: %s", result)
	}

	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatalf("expected outbox item: %v", err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if event.Fields["消耗流量"] != "0.292 GB" {
		t.Fatalf("expected 0.292 GB in event fields, got: %s", event.Fields["消耗流量"])
	}
	if !strings.Contains(event.Title, "高精度测试节点") {
		t.Fatalf("expected title to contain remark, got: %s", event.Title)
	}
}

func TestDailyReportFallbackWhenNo24HourData(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()

	cfg := domain.Config{
		AdminPassword:     "Strong-Password-42!",
		TrafficThreshold:  95,
		ShutdownMode:      "KeepCharging",
		ThresholdAction:   "stop_and_notify",
		APIInterval:       600,
		Timezone:          "Asia/Shanghai",
		EnableDailyReport: true,
		Notifications: domain.NotificationConfig{
			Webhook: domain.WebhookConfig{
				Enabled: true,
				URL:     "https://webhook.example.com/test",
			},
		},
		Accounts: []domain.Account{
			{
				AccessKeyID:     "LTAI_FALLBACK",
				AccessKeySecret: "sec",
				RegionID:        "cn-hongkong",
				InstanceID:      "i-fallback",
				MaxTraffic:      200,
				Remark:          "回退测试节点",
				ScheduleEnabled: false,
				DailyReportTime: "00:00",
			},
		},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	eng := New(st, nil, notify.New(), nil, 1)
	accs, _ := st.ListAccounts(ctx)
	acc := accs[0]

	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)

	_ = st.AddTrafficStats(ctx, acc.ID, 2.0, now.Add(-2*time.Hour))
	_ = st.AddTrafficStats(ctx, acc.ID, 2.5, now)
	_ = st.UpdateRuntime(ctx, acc.ID, 2.5, domain.StatusRunning, now)

	result, err := eng.generateAndSendDailyReport(ctx, true, acc.ID)
	if err != nil {
		t.Fatalf("generateAndSendDailyReport failed: %v", err)
	}
	if strings.Contains(result, "0.00 GB") {
		t.Fatalf("consumed should not be 0 when fallback data is available, got: %s", result)
	}
	if !strings.Contains(result, "0.50 GB") && !strings.Contains(result, "0.500 GB") {
		t.Fatalf("expected consumed ~0.5 GB from fallback, got: %s", result)
	}
}

func TestDailyReportRefreshesTrafficBeforeCalculation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()

	cfg := domain.Config{
		AdminPassword:     "Strong-Password-42!",
		TrafficThreshold:  95,
		ShutdownMode:      "KeepCharging",
		ThresholdAction:   "stop_and_notify",
		APIInterval:       600,
		Timezone:          "Asia/Shanghai",
		EnableDailyReport: true,
		Notifications: domain.NotificationConfig{Webhook: domain.WebhookConfig{
			Enabled: true,
			URL:     "https://webhook.example.com/test",
		}},
		Accounts: []domain.Account{{
			AccessKeyID:     "LTAI_REPORT_REFRESH",
			AccessKeySecret: "secret",
			RegionID:        "cn-hongkong",
			InstanceID:      "i-report-refresh",
			MaxTraffic:      200,
			Remark:          "日报刷新测试节点",
		}},
	}
	if err = st.Setup(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	// The stored runtime starts at zero, while the last known baseline is 1 GB.
	// The provider returns 1.25 GB when the report refreshes the account.
	if err = st.AddTrafficStats(ctx, accounts[0].ID, 1.0, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	eng := New(st, billingTestProvider{}, notify.New(), nil, 1)
	result, err := eng.generateAndSendDailyReport(ctx, true, accounts[0].ID)
	if err != nil {
		t.Fatalf("generateAndSendDailyReport failed: %v", err)
	}
	if !strings.Contains(result, "0.25 GB") && !strings.Contains(result, "0.250 GB") {
		t.Fatalf("expected refreshed traffic consumption, got: %s", result)
	}

	outboxItem, err := st.ClaimOutbox(ctx)
	if err != nil {
		t.Fatalf("expected outbox item: %v", err)
	}
	var event domain.NotificationEvent
	if err = json.Unmarshal([]byte(outboxItem.Payload), &event); err != nil {
		t.Fatal(err)
	}
	if event.Fields["消耗流量"] != "0.25 GB" {
		t.Fatalf("expected 0.25 GB in event fields, got %q", event.Fields["消耗流量"])
	}
}
