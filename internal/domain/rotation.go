package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type RotationSlot struct {
	AccountID int64  `json:"account_id"`
	Start     string `json:"start"`
	End       string `json:"end"`
}

type RotationConfig struct {
	Enabled         bool           `json:"enabled"`
	Hostname        string         `json:"hostname"`
	Token           string         `json:"token,omitempty"`
	TokenConfigured bool           `json:"token_configured"`
	Slots           []RotationSlot `json:"slots"`
}

type RotationState struct {
	ActiveID    int64     `json:"active_id"`
	IP          string    `json:"ip"`
	RetireAfter time.Time `json:"retire_after"`
	UpdatedAt   time.Time `json:"updated_at"`
	Message     string    `json:"message"`
	Error       string    `json:"error"`
}

func (r RotationConfig) Contains(id int64) bool {
	if r.Enabled {
		for _, slot := range r.Slots {
			if slot.AccountID == id {
				return true
			}
		}
	}
	return false
}

// Validate 要求每天完整覆盖且不重叠，跨午夜时段使用次日结束时间。
func (r *RotationConfig) Validate(accounts []Account) error {
	r.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Hostname), "."))
	if !r.Enabled {
		return nil
	}
	if r.Token == "" {
		return errors.New("请填写 Cloudflare API Token")
	}
	if len(r.Hostname) > 253 || !strings.Contains(r.Hostname, ".") {
		return errors.New("请填写完整域名，不包含协议、端口和路径")
	}
	for _, label := range strings.Split(r.Hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("域名格式无效")
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return errors.New("域名请使用 ASCII 或 Punycode 格式")
			}
		}
	}
	if len(r.Slots) < 2 {
		return errors.New("请至少选择两台机器")
	}
	known := map[int64]Account{}
	for _, a := range accounts {
		known[a.ID] = a
	}
	seen := map[int64]bool{}
	instances := map[string]bool{}
	var coverage [1440]bool
	for _, slot := range r.Slots {
		a, ok := known[slot.AccountID]
		if !ok || a.InstanceID == "" || a.MaxTraffic <= 0 {
			return errors.New("参与轮换的机器必须存在，并设置实例 ID 和正数流量上限")
		}
		key := a.RegionID + ":" + a.InstanceID
		if seen[a.ID] || instances[key] {
			return errors.New("同一机器只能参与一个时段，不能重复选择实例")
		}
		seen[a.ID], instances[key] = true, true
		start, err := time.Parse("15:04", slot.Start)
		if err != nil || start.Format("15:04") != slot.Start {
			return errors.New("开始时间必须为 HH:mm")
		}
		end, err := time.Parse("15:04", slot.End)
		if err != nil || end.Format("15:04") != slot.End {
			return errors.New("结束时间必须为 HH:mm，午夜请填写 00:00")
		}
		s, e := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
		if s == e {
			return errors.New("开始时间和结束时间不能相同")
		}
		for m := s; m != e; m = (m + 1) % 1440 {
			if coverage[m] {
				return fmt.Errorf("轮换时段在 %02d:%02d 重叠", m/60, m%60)
			}
			coverage[m] = true
		}
	}
	for m, covered := range coverage {
		if !covered {
			return fmt.Errorf("轮换时段未覆盖 %02d:%02d，请连续覆盖全天", m/60, m%60)
		}
	}
	sort.Slice(r.Slots, func(i, j int) bool { return r.Slots[i].Start < r.Slots[j].Start })
	return nil
}

func (r RotationConfig) SlotAt(now time.Time) int {
	clock := now.Format("15:04")
	for i, s := range r.Slots {
		if s.Start < s.End && clock >= s.Start && clock < s.End || s.Start > s.End && (clock >= s.Start || clock < s.End) {
			return i
		}
	}
	return -1
}
