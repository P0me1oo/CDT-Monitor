package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
	base string
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: "https://api.cloudflare.com/client/v4"}
}

type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

func (c *Client) request(ctx context.Context, token, method, path string, body any, result any) error {
	var input bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&input).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &input)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("Cloudflare 请求失败，请检查网络连接")
	}
	defer resp.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("Cloudflare 响应无效（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		code := 0
		if len(envelope.Errors) > 0 {
			code = envelope.Errors[0].Code
		}
		// 不将远端响应正文写入日志，避免凭据被原样返回。
		return fmt.Errorf("Cloudflare 请求失败（HTTP %d，错误码 %d），请检查 Token 的 Zone Read / DNS Edit 权限", resp.StatusCode, code)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

func (c *Client) findRecord(ctx context.Context, token, hostname string) (string, *record, error) {
	labels := strings.Split(hostname, ".")
	zone := ""
	for i := 0; i < len(labels)-1; i++ {
		var zones []struct {
			ID string `json:"id"`
		}
		if err := c.request(ctx, token, "GET", "/zones?name="+url.QueryEscape(strings.Join(labels[i:], ".")), nil, &zones); err != nil {
			return "", nil, err
		}
		if len(zones) == 1 {
			zone = zones[0].ID
			break
		}
	}
	if zone == "" {
		return "", nil, errors.New("Token 无权访问该域名所属的 Cloudflare Zone")
	}
	var records []record
	if err := c.request(ctx, token, "GET", "/zones/"+zone+"/dns_records?name="+url.QueryEscape(hostname)+"&per_page=5000", nil, &records); err != nil {
		return "", nil, err
	}
	var found *record
	for _, rec := range records {
		switch rec.Type {
		case "AAAA", "CNAME", "NS":
			return "", nil, errors.New("该域名存在 AAAA、CNAME 或 NS 记录；轮换需使用独立的灰云 IPv4 A 记录")
		case "A":
			if found != nil {
				return "", nil, errors.New("该域名存在多条 A 记录，请保留一条再启用轮换")
			}
			if rec.Proxied {
				return "", nil, errors.New("代理服务的轮换域名必须关闭橙云代理")
			}
			copy := rec
			found = &copy
		}
	}
	return zone, found, nil
}

// Check 只校验权限和记录结构，不修改 DNS。
func (c *Client) Check(ctx context.Context, token, hostname string) error {
	_, _, err := c.findRecord(ctx, token, hostname)
	return err
}

// Point 更新唯一的灰云 A 记录，返回旧 TTL，供旧机器延迟停机使用。
func (c *Client) Point(ctx context.Context, token, hostname, address string) (int, error) {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return 0, errors.New("实例尚未取得有效公网 IPv4")
	}
	zone, rec, err := c.findRecord(ctx, token, hostname)
	if err != nil {
		return 0, err
	}
	path := "/zones/" + zone + "/dns_records"
	body := map[string]any{"content": ip.String(), "ttl": 60}
	oldTTL := 300
	method := "POST"
	if rec != nil {
		oldTTL = rec.TTL
		if oldTTL == 1 {
			oldTTL = 300
		}
		if rec.Content == ip.String() && rec.TTL == 60 {
			return oldTTL, nil
		}
		method = "PATCH"
		path += "/" + rec.ID
	} else {
		body["type"] = "A"
		body["name"] = hostname
		body["proxied"] = false
	}
	var updated record
	if err = c.request(ctx, token, method, path, body, &updated); err != nil {
		return 0, err
	}
	if updated.Content != ip.String() || updated.Proxied || updated.Type != "A" {
		return 0, errors.New("Cloudflare 返回的 DNS 记录与目标不一致")
	}
	return oldTTL, nil
}
