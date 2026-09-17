package engine

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/CDT-Monitor/internal/aliyun"
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/notify"
	"github.com/P0me1oo/CDT-Monitor/internal/store"
)

// recordingProvider 记录收到的开关机指令，用于验证保活是否被触发。
type recordingProvider struct {
	mu     sync.Mutex
	status string
	calls  []string
}

func (p *recordingProvider) GetTraffic(context.Context, domain.Account, string) (float64, error) {
	return 1.25, nil
}

func (p *recordingProvider) GetInstanceStatus(context.Context, domain.Account, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, nil
}

func (p *recordingProvider) ControlInstance(_ context.Context, _ domain.Account, _, action, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, action)
	return nil
}

func (p *recordingProvider) GetAccountBalance(context.Context, domain.Account, string) (aliyun.BillingBalance, error) {
	return aliyun.BillingBalance{}, nil
}

func (p *recordingProvider) GetInstanceBill(context.Context, domain.Account, string, string) (aliyun.BillingBill, error) {
	return aliyun.BillingBill{}, nil
}

func (p *recordingProvider) actions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func newKeepAliveEngine(t *testing.T, account domain.Account) (*store.Store, *Engine, *recordingProvider, int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	config := domain.Config{
		AdminPassword:    "Strong-Password-42!",
		TrafficThreshold: 95,
		ShutdownMode:     "StopCharging",
		ThresholdAction:  "stop_and_notify",
		KeepAlive:        true,
		APIInterval:      600,
		Timezone:         "Asia/Shanghai",
		Accounts:         []domain.Account{account},
	}
	if err = st.Setup(ctx, config); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
	provider := &recordingProvider{status: domain.StatusRunning}
	// 新建账号的状态是 Unknown，会被当成中间态拒绝控制指令，这里先落一个真实状态。
	if err = st.UpdateRuntime(ctx, accounts[0].ID, 0, domain.StatusRunning, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return st, New(st, provider, notify.New(), slog.Default(), 1), provider, accounts[0].ID
}

func TestKeepAlivePauseUntilNextStart(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	account := domain.Account{ScheduleEnabled: true, StartTime: "00:00", StopTime: "12:00"}

	now := time.Date(2026, 9, 17, 3, 30, 0, 0, location)
	want := time.Date(2026, 9, 18, 0, 0, 0, 0, location).Unix()
	if got := keepAlivePauseUntil(now, account); got != want {
		t.Fatalf("已过今天的开机点应顺延到明天：got=%d want=%d", got, want)
	}

	account.StartTime = "12:00"
	want = time.Date(2026, 9, 17, 12, 0, 0, 0, location).Unix()
	if got := keepAlivePauseUntil(now, account); got != want {
		t.Fatalf("未到今天的开机点应暂停到当天：got=%d want=%d", got, want)
	}

	account.ScheduleEnabled = false
	if got := keepAlivePauseUntil(now, account); got != domain.KeepAlivePauseUntilStart {
		t.Fatalf("没有定时计划应一直暂停到手动开机：got=%d", got)
	}
}

func TestKeepAliveRestartsStoppedInstance(t *testing.T) {
	ctx := context.Background()
	_, engine, provider, id := newKeepAliveEngine(t, domain.Account{
		AccessKeyID: "LTAItest", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-test", MaxTraffic: 200, SiteType: "china",
	})

	provider.status = domain.StatusStopped
	if _, err := engine.processAccount(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	actions := provider.actions()
	if len(actions) != 1 || actions[0] != "start" {
		t.Fatalf("保活应拉起停止的实例：actions=%v", actions)
	}
}

func TestManualStopPausesKeepAlive(t *testing.T) {
	ctx := context.Background()
	st, engine, provider, id := newKeepAliveEngine(t, domain.Account{
		AccessKeyID: "LTAItest", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-test", MaxTraffic: 200, SiteType: "china",
		ScheduleEnabled: true, StartTime: "00:00", StopTime: "12:00",
	})

	if _, err := engine.control(ctx, id, "stop", "手动"); err != nil {
		t.Fatalf("保活启用时手动关机不应被拒绝：%v", err)
	}
	account, err := st.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if account.KeepAlivePausedUntil <= 0 {
		t.Fatalf("手动关机应写入保活暂停时间：got=%d", account.KeepAlivePausedUntil)
	}

	provider.status = domain.StatusStopped
	if _, err = engine.processAccount(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if actions := provider.actions(); len(actions) != 1 || actions[0] != "stop" {
		t.Fatalf("暂停期间保活不应再开机：actions=%v", actions)
	}

	if _, err = engine.control(ctx, id, "start", "手动"); err != nil {
		t.Fatal(err)
	}
	account, err = st.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if account.KeepAlivePausedUntil != 0 {
		t.Fatalf("手动开机应恢复保活：got=%d", account.KeepAlivePausedUntil)
	}
}

func TestScheduledStartClearsKeepAlivePause(t *testing.T) {
	ctx := context.Background()
	st, engine, _, id := newKeepAliveEngine(t, domain.Account{
		AccessKeyID: "LTAItest", AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: "i-test", MaxTraffic: 200, SiteType: "china",
		ScheduleEnabled: true, StartTime: "00:00", StopTime: "12:00",
	})

	if err := st.SetKeepAlivePause(ctx, id, domain.KeepAlivePauseUntilStart); err != nil {
		t.Fatal(err)
	}
	config, err := st.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	account, err := st.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := engine.executeScheduledAction(ctx, config, account, "secret", "start", time.Now())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	account, err = st.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if account.KeepAlivePausedUntil != 0 {
		t.Fatalf("定时开机应恢复保活：got=%d", account.KeepAlivePausedUntil)
	}
}
