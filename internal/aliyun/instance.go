package aliyun

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/P0me1oo/CDT-Monitor/internal/domain"
)

type InstanceInfo struct{ Status, PublicIP string }

// InstanceReader 单独定义，保持原有监控 Provider 的兼容性。
type InstanceReader interface {
	GetInstanceInfo(context.Context, domain.Account, string) (InstanceInfo, error)
}

func (c *Client) GetInstanceInfo(ctx context.Context, a domain.Account, secret string) (InstanceInfo, error) {
	if a.InstanceID == "" {
		return InstanceInfo{}, errors.New("缺少实例 ID")
	}
	ids, _ := json.Marshal([]string{a.InstanceID})
	result, err := c.call(ctx, a.AccessKeyID, secret, a.RegionID, "ecs."+a.RegionID+".aliyuncs.com", "2014-05-26", "DescribeInstances", map[string]string{"RegionId": a.RegionID, "InstanceIds": string(ids)})
	if err != nil {
		return InstanceInfo{}, err
	}
	for _, item := range nestedSlice(result, "Instances", "Instance") {
		row, ok := item.(map[string]any)
		if !ok || stringValue(row["InstanceId"]) != a.InstanceID {
			continue
		}
		info := InstanceInfo{Status: stringValue(row["Status"])}
		if eip, ok := row["EipAddress"].(map[string]any); ok {
			info.PublicIP = stringValue(eip["IpAddress"])
		}
		if info.PublicIP == "" {
			for _, value := range nestedSlice(row, "PublicIpAddress", "IpAddress") {
				if ip := net.ParseIP(stringValue(value)); ip != nil && ip.To4() != nil {
					info.PublicIP = ip.String()
					break
				}
			}
		}
		return info, nil
	}
	return InstanceInfo{}, errors.New("未找到指定实例，可能已被回收")
}
