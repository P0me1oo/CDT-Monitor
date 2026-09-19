package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/P0me1oo/CDT-Monitor/internal/aliyun"
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/notify"
	"github.com/P0me1oo/CDT-Monitor/internal/store"
)

type rotationProvider struct {
	billingTestProvider
	info        map[int64]aliyun.InstanceInfo
	traffic     map[int64]float64
	queryErrors map[int64]error
	actions     []string
}

func (p *rotationProvider) GetInstanceInfo(_ context.Context, a domain.Account, _ string) (aliyun.InstanceInfo, error) {
	return p.info[a.ID], nil
}
func (p *rotationProvider) GetTraffic(_ context.Context, a domain.Account, _ string) (float64, error) {
	return p.traffic[a.ID], p.queryErrors[a.ID]
}
func (p *rotationProvider) GetInstanceStatus(_ context.Context, a domain.Account, _ string) (string, error) {
	return p.info[a.ID].Status, nil
}
func (p *rotationProvider) ControlInstance(_ context.Context, a domain.Account, _, action, mode string) error {
	p.actions = append(p.actions, fmt.Sprintf("%d:%s:%s", a.ID, action, mode))
	info := p.info[a.ID]
	info.Status = domain.StatusStarting
	if action == "stop" {
		info.Status = domain.StatusStopping
	}
	p.info[a.ID] = info
	return nil
}

type rotationTestDNS struct {
	err   error
	calls []string
	ttl   int
}

func (d *rotationTestDNS) Check(context.Context, string, string) error { return d.err }
func (d *rotationTestDNS) Point(_ context.Context, _, _, ip string) (int, error) {
	d.calls = append(d.calls, ip)
	return d.ttl, d.err
}

func rotationFixture(t *testing.T) (*Engine, *rotationProvider, *rotationTestDNS, []domain.Account, time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := domain.Config{AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging", ThresholdAction: "notify_only", KeepAlive: true, APIInterval: 600, Timezone: "Asia/Shanghai"}
	for i := 0; i < 3; i++ {
		cfg.Accounts = append(cfg.Accounts, domain.Account{AccessKeyID: fmt.Sprintf("key%d", i), AccessKeySecret: "secret", RegionID: "cn-hongkong", InstanceID: fmt.Sprintf("i-%d", i), MaxTraffic: 100, ScheduleEnabled: true, StartTime: time.Now().Format("15:04"), StopTime: "22:00"})
	}
	if err = st.Setup(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := domain.RotationConfig{Enabled: true, Token: "test-token", Hostname: "proxy.example.com", Slots: []domain.RotationSlot{{AccountID: accounts[0].ID, Start: "00:00", End: "08:00"}, {AccountID: accounts[1].ID, Start: "08:00", End: "16:00"}, {AccountID: accounts[2].ID, Start: "16:00", End: "00:00"}}}
	if err = st.SaveRotation(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	p := &rotationProvider{info: map[int64]aliyun.InstanceInfo{}, traffic: map[int64]float64{}, queryErrors: map[int64]error{}}
	for i, a := range accounts {
		p.info[a.ID] = aliyun.InstanceInfo{Status: domain.StatusStopped, PublicIP: fmt.Sprintf("8.8.4.%d", i+1)}
	}
	d := &rotationTestDNS{ttl: 60}
	e := New(st, p, notify.New(), slog.Default(), 1)
	e.dns = d
	loc, _ := time.LoadLocation(cfg.Timezone)
	return e, p, d, accounts, time.Date(2026, 9, 19, 8, 0, 0, 0, loc)
}

func TestRotationStartsBeforeDNSAndDrainsAcrossRestart(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	p.info[a[0].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.1"}
	if _, err := e.runRotation(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 || len(p.actions) != 1 || p.actions[0] != fmt.Sprintf("%d:start:StopCharging", a[1].ID) {
		t.Fatalf("premature switch: %v %v", d.calls, p.actions)
	}
	p.info[a[1].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.2"}
	d.ttl = 600
	if _, err := e.runRotation(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 1 || len(p.actions) != 1 {
		t.Fatalf("old instance stopped before drain: %v", p.actions)
	}
	restarted := New(e.store, p, notify.New(), slog.Default(), 1)
	restarted.dns = d
	if _, err := restarted.runRotation(t.Context(), now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(p.actions) != 1 {
		t.Fatal("restart lost old TTL delay")
	}
	if _, err := restarted.runRotation(t.Context(), now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(p.actions) != 2 || p.actions[1] != fmt.Sprintf("%d:stop:StopCharging", a[0].ID) {
		t.Fatalf("old machine not retired: %v", p.actions)
	}
}

func TestRotationThresholdStopsEvenWhenDNSFails(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	p.traffic[a[0].ID] = 95
	p.info[a[0].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.1"}
	p.info[a[1].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.2"}
	d.err = errors.New("DNS unavailable")
	if _, err := e.runRotation(t.Context(), now.Add(-time.Hour)); err == nil {
		t.Fatal("expected DNS failure")
	}
	if len(p.actions) != 1 || p.actions[0] != fmt.Sprintf("%d:stop:StopCharging", a[0].ID) {
		t.Fatalf("threshold did not override notify-only/global shutdown mode: %v", p.actions)
	}
}

func TestRotationDNSFailureKeepsOldMachine(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	for _, account := range a[:2] {
		info := p.info[account.ID]
		info.Status = domain.StatusRunning
		p.info[account.ID] = info
	}
	d.err = errors.New("DNS unavailable")
	if _, err := e.runRotation(t.Context(), now); err == nil {
		t.Fatal("expected DNS failure")
	}
	if len(p.actions) != 0 {
		t.Fatalf("stopped old node on failed switch: %v", p.actions)
	}
}

func TestRotationSkipsExceededNodesAndKeepsSchedule(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	p.traffic[a[0].ID] = 95
	p.traffic[a[1].ID] = 99
	p.info[a[2].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.3"}
	for _, at := range []time.Time{now.Add(-time.Hour), now, now.Add(9 * time.Hour)} {
		if _, err := e.runRotation(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	for _, ip := range d.calls {
		if ip != "8.8.4.3" {
			t.Fatal("did not skip exceeded machines")
		}
	}
	if len(p.actions) != 0 {
		t.Fatalf("unexpected action: %v", p.actions)
	}
}

func TestRotationAllExceededStopsAllAndResumesAfterReset(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	for _, account := range a {
		p.traffic[account.ID] = 100
		info := p.info[account.ID]
		info.Status = domain.StatusRunning
		p.info[account.ID] = info
	}
	if _, err := e.runRotation(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 || len(p.actions) != 3 {
		t.Fatalf("actions=%v DNS=%v", p.actions, d.calls)
	}
	for _, account := range a {
		p.traffic[account.ID] = 0
		info := p.info[account.ID]
		info.Status = domain.StatusStopped
		p.info[account.ID] = info
	}
	if _, err := e.runRotation(t.Context(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.actions[3] != fmt.Sprintf("%d:start:StopCharging", a[1].ID) {
		t.Fatalf("did not resume scheduled node: %v", p.actions)
	}
}

func TestRotationUnknownTrafficNeverStartsNode(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	for _, account := range a {
		p.queryErrors[account.ID] = errors.New("traffic unavailable")
	}
	if _, err := e.runRotation(t.Context(), now); err == nil {
		t.Fatal("expected query error")
	}
	if len(p.actions) != 0 || len(d.calls) != 0 {
		t.Fatal("acted on unverified traffic")
	}
}

func TestRotationPrewarmAndMidnightBoundary(t *testing.T) {
	e, p, d, a, now := rotationFixture(t)
	p.info[a[0].ID] = aliyun.InstanceInfo{Status: domain.StatusRunning, PublicIP: "8.8.4.1"}
	if _, err := e.runRotation(t.Context(), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(p.actions) != 1 || p.actions[0] != fmt.Sprintf("%d:start:StopCharging", a[1].ID) || d.calls[0] != "8.8.4.1" {
		t.Fatalf("prewarm changed DNS too early: %v %v", p.actions, d.calls)
	}
	p.actions = nil
	p.info[a[0].ID] = aliyun.InstanceInfo{Status: domain.StatusStopped, PublicIP: "8.8.4.1"}
	if _, err := e.runRotation(t.Context(), now.Add(16*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p.actions[0] != fmt.Sprintf("%d:start:StopCharging", a[0].ID) {
		t.Fatal("midnight did not select first slot")
	}
}

func TestRotationOwnsLegacyScheduleKeepaliveAndManualActions(t *testing.T) {
	e, p, _, a, _ := rotationFixture(t)
	if _, err := e.processAccount(t.Context(), a[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if len(p.actions) != 0 {
		t.Fatalf("legacy automation controlled member: %v", p.actions)
	}
	if _, err := e.control(t.Context(), a[0].ID, "start", "manual"); err == nil {
		t.Fatal("manual control bypassed rotation")
	}
	config, err := e.store.GetConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	config.Accounts = config.Accounts[1:]
	if err = e.SaveConfig(t.Context(), config); err == nil {
		t.Fatal("deleted active rotation member")
	}
}
