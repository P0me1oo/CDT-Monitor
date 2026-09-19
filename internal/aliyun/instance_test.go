package aliyun

import (
	"fmt"
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetInstanceInfoUsesExactInstanceAndEIP(t *testing.T) {
	for _, tc := range []struct{ eip, want string }{{"", "8.8.8.8"}, {"8.8.4.4", "8.8.4.4"}} {
		c := NewClient()
		c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = r.ParseForm()
			if r.Form.Get("Action") != "DescribeInstances" || r.Form.Get("InstanceIds") != `["i-test"]` {
				t.Errorf("wrong request: %v", r.Form)
			}
			body := fmt.Sprintf(`{"Instances":{"Instance":[{"InstanceId":"i-other","Status":"Running"},{"InstanceId":"i-test","Status":"Running","PublicIpAddress":{"IpAddress":["8.8.8.8"]},"EipAddress":{"IpAddress":%q}}]}}`, tc.eip)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		info, err := c.GetInstanceInfo(t.Context(), domain.Account{InstanceID: "i-test", RegionID: "cn-hongkong"}, "secret")
		if err != nil || info.Status != "Running" || info.PublicIP != tc.want {
			t.Fatalf("info=%+v err=%v", info, err)
		}
	}
}
