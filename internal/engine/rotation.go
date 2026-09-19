package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/P0me1oo/CDT-Monitor/internal/aliyun"
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/notify"
	"github.com/P0me1oo/CDT-Monitor/internal/security"
)

type rotationDNS interface {
	Check(context.Context, string, string) error
	Point(context.Context, string, string, string) (int, error)
}

func (e *Engine) SaveRotation(ctx context.Context, r domain.RotationConfig) error {
	e.automationMu.Lock()
	defer e.automationMu.Unlock()
	old, err := e.store.GetRotation(ctx)
	if err != nil {
		return err
	}
	if r.Token == "" {
		r.Token = old.Token
	}
	accounts, err := e.store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if err = r.Validate(accounts); err != nil {
		return err
	}
	if old.Enabled {
		// 运行时只允许停用，避免修改成员后遗留无人管理的运行实例。
		if r.Enabled {
			return errors.New("请先停用轮换，再修改并重新启用")
		}
		r = old
		r.Enabled = false
	}
	if r.Enabled {
		cfg, configErr := e.store.GetConfig(ctx)
		if configErr != nil {
			return configErr
		}
		if _, configErr = time.LoadLocation(cfg.Timezone); configErr != nil {
			return errors.New("请先在系统设置中配置有效时区")
		}
		if err = e.dns.Check(ctx, r.Token, r.Hostname); err != nil {
			return err
		}
	}
	return e.store.SaveRotation(ctx, r)
}

func (e *Engine) SaveConfig(ctx context.Context, cfg domain.Config) error {
	e.automationMu.Lock()
	defer e.automationMu.Unlock()
	r, err := e.store.GetRotation(ctx)
	if err != nil {
		return err
	}
	if r.Enabled {
		if _, err = time.LoadLocation(cfg.Timezone); err != nil {
			return errors.New("轮换计划需要有效的业务时区")
		}
		if err = r.Validate(cfg.Accounts); err != nil {
			return err
		}
		old, err := e.store.ListAccounts(ctx)
		if err != nil {
			return err
		}
		for _, a := range old {
			if !r.Contains(a.ID) {
				continue
			}
			for _, b := range cfg.Accounts {
				if a.ID == b.ID && (a.InstanceID != b.InstanceID || a.RegionID != b.RegionID || a.AccessKeyID != b.AccessKeyID) {
					return errors.New("请先停用轮换，再修改参与机器的账号、地域或实例 ID")
				}
			}
		}
	}
	return e.store.SaveConfig(ctx, cfg)
}

type rotationNode struct {
	account   domain.Account
	secret    string
	info      aliyun.InstanceInfo
	trafficOK bool
	err       error
}

func (e *Engine) runRotation(ctx context.Context, now time.Time) (message string, runErr error) {
	e.automationMu.Lock()
	defer e.automationMu.Unlock()
	liveClock := now.IsZero()
	if liveClock {
		now = time.Now()
	}
	// 不同 Worker 或进程不能同时向同一个域名提交切换。
	owner, err := security.NewToken(12)
	if err != nil {
		return "", err
	}
	ok, err := e.store.AcquireLease(ctx, "rotation", owner, 70*time.Second)
	if err != nil {
		return "", err
	}
	if !ok {
		return "已有轮换任务执行中", nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = e.store.ReleaseLease(cleanup, "rotation", owner)
	}()
	r, err := e.store.GetRotation(ctx)
	if err != nil {
		return "", err
	}
	if !r.Enabled {
		return "轮换已停用", nil
	}
	cfg, err := e.store.GetConfig(ctx)
	if err != nil {
		return "", err
	}
	if err = r.Validate(cfg.Accounts); err != nil {
		return "", err
	}
	state, err := e.store.GetRotationState(ctx)
	if err != nil {
		return "", err
	}
	previous := state
	defer func() {
		state.UpdatedAt = time.Now().UTC()
		state.Message = message
		state.Error = ""
		if runErr != nil {
			state.Error = runErr.Error()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := e.store.SaveRotationState(cleanup, state); err != nil && runErr == nil {
			runErr = err
		}
		if state.Error != "" && state.Error != previous.Error {
			e.rotationEvent(cleanup, cfg, "轮换执行失败", state.Error)
		}
	}()
	reader, ok := e.provider.(aliyun.InstanceReader)
	if !ok {
		return "", errors.New("当前云服务不支持实例公网 IP 查询")
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return "", err
	}
	now = now.In(location)
	index := r.SlotAt(now)
	if index < 0 {
		return "", errors.New("当前时间没有对应的轮换时段")
	}
	byID := map[int64]domain.Account{}
	for _, a := range cfg.Accounts {
		byID[a.ID] = a
	}
	nodes := make([]rotationNode, len(r.Slots))
	for i, slot := range r.Slots {
		nodes[i].account = byID[slot.AccountID]
		nodes[i].secret, err = e.store.AccountSecret(ctx, slot.AccountID)
		if err != nil {
			return "", err
		}
	}
	// 并发查询不同机器，最多四个请求组，避免一个实例拖延全部阈值检查。
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	queryCtx, cancelQueries := context.WithTimeout(ctx, 20*time.Second)
	defer cancelQueries()
	for i := range nodes {
		wg.Add(1)
		go func(n *rotationNode) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-queryCtx.Done():
				n.err = queryCtx.Err()
				return
			}
			defer func() { <-limit }()
			n.info, n.err = reader.GetInstanceInfo(queryCtx, n.account, n.secret)
			traffic, trafficErr := e.provider.GetTraffic(queryCtx, n.account, n.secret)
			if trafficErr == nil {
				n.account.TrafficUsed = traffic
				n.trafficOK = true
			} else if n.err == nil {
				n.err = fmt.Errorf("实例 %s 流量查询失败", n.account.InstanceID)
			}
		}(&nodes[i])
	}
	wg.Wait()
	cancelQueries()
	// 排队和云端查询可能跨越时段边界，生产任务使用查询完成后的当前时间。
	if liveClock {
		now = time.Now().In(location)
		index = r.SlotAt(now)
		if index < 0 {
			return "", errors.New("当前时间没有对应的轮换时段")
		}
	}
	var queryErr error
	var stopErr error
	for i := range nodes {
		n := &nodes[i]
		if n.err != nil {
			queryErr = errors.Join(queryErr, fmt.Errorf("%s: %w", n.account.InstanceID, n.err))
		}
		if n.info.Status != "" {
			n.account.InstanceStatus = n.info.Status
			updated := n.account.UpdatedAt
			if n.trafficOK {
				updated = now.UTC()
			}
			if err = e.store.UpdateRuntime(ctx, n.account.ID, n.account.TrafficUsed, n.info.Status, updated); err != nil {
				return "", err
			}
			if n.trafficOK {
				_ = e.store.AddTrafficStats(ctx, n.account.ID, n.account.TrafficUsed, now)
			}
		}
		if usagePercent(n.account.TrafficUsed, n.account.MaxTraffic) >= float64(cfg.TrafficThreshold) {
			// 超限优先，DNS 故障或下一台尚未开机都不能延迟节省停机。
			if err = e.rotationStop(ctx, n); err != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("实例 %s 超限停机失败: %w", n.account.InstanceID, err))
				continue
			}
			key := fmt.Sprintf("rotation-threshold:%d", n.account.ID)
			fresh, keyErr := e.store.RecordActionEvent(ctx, key, n.account.ID, "rotation_threshold", "stopped", "")
			if keyErr != nil {
				return "", keyErr
			}
			if fresh {
				e.rotationEvent(ctx, cfg, "轮换机器流量超限", fmt.Sprintf("实例 %s 已达到全局 %d%% 告警阈值，执行节省停机并跳过。", n.account.InstanceID, cfg.TrafficThreshold))
			}
		} else if n.trafficOK {
			_ = e.store.DeleteActionEvent(ctx, fmt.Sprintf("rotation-threshold:%d", n.account.ID))
		}
	}
	if stopErr != nil {
		return "部分超限机器停机失败，将继续重试", stopErr
	}
	eligible := func(i int) bool {
		n := nodes[i]
		return n.err == nil && n.trafficOK && usagePercent(n.account.TrafficUsed, n.account.MaxTraffic) < float64(cfg.TrafficThreshold)
	}
	choose := func(start int) int {
		for offset := 0; offset < len(nodes); offset++ {
			i := (start + offset) % len(nodes)
			if eligible(i) {
				return i
			}
		}
		return -1
	}
	target := choose(index)
	if target < 0 {
		if queryErr != nil {
			return "无法确认可接班机器，保留未超限机器的当前状态", queryErr
		}
		state.ActiveID = 0
		message = "全部机器达到流量阈值，已停止轮换，等待流量恢复后重新检查"
		if previous.Message != message {
			e.rotationEvent(ctx, cfg, "轮换无可用机器", message)
		}
		return message, nil
	}
	n := &nodes[target]
	if n.info.Status == domain.StatusStopped {
		if err = e.rotationStart(ctx, n); err != nil {
			return "", err
		}
		return "已发送接班机器开机指令，等待 Running", nil
	}
	if n.info.Status != domain.StatusRunning {
		return "等待接班机器进入 Running 状态", nil
	}
	if n.info.PublicIP == "" {
		return "等待接班机器分配公网 IPv4", nil
	}
	// 每轮重新核对 DNS，进程在更新成功后崩溃也能安全恢复交接。
	ttl, err := e.dns.Point(ctx, r.Token, r.Hostname, n.info.PublicIP)
	if err != nil {
		return "DNS 更新失败，保留未超限机器运行", err
	}
	if state.ActiveID != n.account.ID || state.IP != n.info.PublicIP {
		state.ActiveID = n.account.ID
		state.IP = n.info.PublicIP
		if ttl < 300 {
			ttl = 300
		}
		deadline := now.Add(time.Duration(ttl) * time.Second).UTC()
		if deadline.After(state.RetireAfter) {
			state.RetireAfter = deadline
		}
		if err = e.store.SaveRotationState(ctx, state); err != nil {
			return "", err
		}
		e.rotationEvent(ctx, cfg, "轮换域名已切换", fmt.Sprintf("%s 已指向实例 %s（%s）", r.Hostname, n.account.InstanceID, n.info.PublicIP))
	}
	// 临近下一时段时提前两分钟开机，DNS 仍在原定边界切换。
	prewarm := -1
	future := r.SlotAt(now.Add(2 * time.Minute))
	if future >= 0 && future != index {
		prewarm = choose(future)
		if prewarm >= 0 && prewarm != target && nodes[prewarm].info.Status == domain.StatusStopped {
			if err = e.rotationStart(ctx, &nodes[prewarm]); err != nil {
				return "", err
			}
		}
	}
	if !now.Before(state.RetireAfter) {
		for i := range nodes {
			if i == target || i == prewarm || nodes[i].err != nil {
				continue
			}
			if err = e.rotationStop(ctx, &nodes[i]); err != nil {
				return "", err
			}
		}
	}
	if queryErr != nil {
		return "当前机器运行中，部分成员查询失败", queryErr
	}
	if now.Before(state.RetireAfter) {
		return "DNS 已更新，等待缓存交接后节省停机", nil
	}
	return "按固定时段运行", nil
}

func (e *Engine) rotationStart(ctx context.Context, n *rotationNode) error {
	if err := e.provider.ControlInstance(ctx, n.account, n.secret, "start", "StopCharging"); err != nil {
		return err
	}
	return e.store.UpdateRuntime(ctx, n.account.ID, n.account.TrafficUsed, domain.StatusStarting, time.Now().UTC())
}

func (e *Engine) rotationStop(ctx context.Context, n *rotationNode) error {
	status := n.info.Status
	if status == "" {
		status = n.account.InstanceStatus
	}
	if status == domain.StatusStopped || status == domain.StatusStopping {
		return nil
	}
	if err := e.provider.ControlInstance(ctx, n.account, n.secret, "stop", "StopCharging"); err != nil {
		return err
	}
	n.info.Status = domain.StatusStopping
	return e.store.UpdateRuntime(ctx, n.account.ID, n.account.TrafficUsed, domain.StatusStopping, time.Now().UTC())
}

func (e *Engine) rotationEvent(ctx context.Context, cfg domain.Config, title, message string) {
	_ = e.store.AddLog(ctx, "info", title+"："+message)
	event := newEvent("rotation", title, message, 0, nil)
	_ = e.store.AddOutbox(ctx, event, notify.EnabledChannels(cfg))
}
