package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/engine"
	"github.com/P0me1oo/CDT-Monitor/internal/store"
)

func TestSecurityHeadersAllowFaviconEndpoint(t *testing.T) {
	server := &Server{}
	handler := server.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://monitor.example.com/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://a.favicon.im") {
		t.Fatalf("favicon endpoint missing from CSP: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' https://api.github.com") {
		t.Fatalf("GitHub API endpoint missing from CSP: %s", csp)
	}
}

func TestNormalizeClockTimeUses24HourValues(t *testing.T) {
	for input, want := range map[string]string{"00:34": "00:34", "00：34": "00:34", "24:00": "00:00"} {
		got, err := normalizeClockTime(input, true)
		if err != nil || got != want {
			t.Errorf("normalizeClockTime(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeClockTime("12:34 AM", true); err == nil {
		t.Fatal("expected non-HH:MM time input to be rejected")
	}
}

func TestBeginPasskeyLoginIncludesRegisteredCredentialIDs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	credentialID := []byte("desktop-bitwarden-credential")
	if err = st.SavePasskey(t.Context(), "PC Bitwarden", webauthn.Credential{ID: credentialID, PublicKey: []byte("test-key")}); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: st, limits: make(map[string]*rateWindow), passkeys: make(map[string]passkeySession)}
	request := httptest.NewRequest(http.MethodPost, "http://monitor.example.com/api/v1/auth/passkeys/begin", strings.NewReader(`{}`))
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	server.beginPasskeyLogin(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		SessionID string `json:"session_id"`
		PublicKey struct {
			PublicKey struct {
				AllowCredentials []struct {
					ID string `json:"id"`
				} `json:"allowCredentials"`
			} `json:"publicKey"`
		} `json:"public_key"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PublicKey.PublicKey.AllowCredentials) != 1 || payload.PublicKey.PublicKey.AllowCredentials[0].ID != base64.RawURLEncoding.EncodeToString(credentialID) {
		t.Fatalf("allowCredentials = %#v", payload.PublicKey.PublicKey.AllowCredentials)
	}
	ceremony, ok := server.passkeys[payload.SessionID]
	if !ok || string(ceremony.session.UserID) != adminWebAuthnID {
		t.Fatalf("login session does not identify the admin user: %#v", ceremony.session.UserID)
	}
}

func TestBeginPasskeyLoginRejectsEmptyCredentialList(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := &Server{store: st, limits: make(map[string]*rateWindow), passkeys: make(map[string]passkeySession)}
	request := httptest.NewRequest(http.MethodPost, "http://monitor.example.com/api/v1/auth/passkeys/begin", strings.NewReader(`{}`))
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	server.beginPasskeyLogin(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRefreshAllEnqueuesEveryConfiguredAccount(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	config := domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging", ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai",
		Accounts: []domain.Account{
			{AccessKeyID: "LTAItest-one", AccessKeySecret: "secret-one", RegionID: "cn-hongkong", InstanceID: "i-one", MaxTraffic: 200, SiteType: "china"},
			{AccessKeyID: "LTAItest-two", AccessKeySecret: "secret-two", RegionID: "ap-southeast-1", InstanceID: "i-two", MaxTraffic: 300, SiteType: "international"},
		},
	}
	if err = st.Setup(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, slog.Default(), 1)
	server := &Server{store: st, engine: eng}
	request := httptest.NewRequest(http.MethodPost, "http://monitor.example.com/api/v1/accounts/refresh", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.refreshAll(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Jobs []domain.Job `json:"jobs"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Jobs) != 2 {
		t.Fatalf("jobs = %#v", payload.Jobs)
	}
	if payload.Jobs[0].Type != engine.JobRefreshAccount || payload.Jobs[1].Type != engine.JobRefreshAccount || payload.Jobs[0].AccountID == payload.Jobs[1].AccountID {
		t.Fatalf("unexpected refresh jobs: %#v", payload.Jobs)
	}
}

func TestUpdateAccountSettingsEndpoint(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	config := domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging", ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai",
		Accounts: []domain.Account{
			{AccessKeyID: "LTAI-settings-test", AccessKeySecret: "secret-one", RegionID: "cn-hongkong", InstanceID: "i-one", MaxTraffic: 200, SiteType: "china"},
		},
	}
	if err = st.Setup(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, slog.Default(), 1)
	server := &Server{store: st, engine: eng}

	accounts, _ := st.ListAccounts(t.Context())
	id := accounts[0].ID

	body := `{"keep_alive":true,"shutdown_mode":"StopCharging","schedule_enabled":true,"start_time":"09:00","stop_time":"00:34","daily_report":false,"daily_report_time":"02:45"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/1/settings", strings.NewReader(body))
	req.SetPathValue("id", "1")
	resp := httptest.NewRecorder()

	server.updateAccountSettings(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}

	updated, err := st.GetAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.KeepAlive == nil || !*updated.KeepAlive {
		t.Fatal("expected keep_alive=true")
	}
	if updated.ShutdownMode != "StopCharging" {
		t.Fatalf("expected shutdown_mode=StopCharging, got %s", updated.ShutdownMode)
	}
	if !updated.ScheduleEnabled || updated.StartTime != "09:00" || updated.StopTime != "00:34" {
		t.Fatalf("unexpected schedule: %+v", updated)
	}
	if updated.DailyReport == nil || *updated.DailyReport != false {
		t.Fatal("expected daily_report=false")
	}

	// Test resetting back to default (null / default)
	bodyReset := `{"keep_alive":null,"shutdown_mode":"default","schedule_enabled":false,"start_time":"08:00","stop_time":"22:00","daily_report":null}`
	reqReset := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/1/settings", strings.NewReader(bodyReset))
	reqReset.SetPathValue("id", "1")
	respReset := httptest.NewRecorder()

	server.updateAccountSettings(respReset, reqReset)
	if respReset.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", respReset.Code, respReset.Body.String())
	}

	resetAccount, err := st.GetAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if resetAccount.KeepAlive != nil {
		t.Fatalf("expected keep_alive=nil, got %v", *resetAccount.KeepAlive)
	}
	if resetAccount.ShutdownMode != "" {
		t.Fatalf("expected shutdown_mode='', got %s", resetAccount.ShutdownMode)
	}
	if resetAccount.DailyReport != nil {
		t.Fatalf("expected daily_report=nil, got %v", *resetAccount.DailyReport)
	}
}

func TestTriggerDailyReportEndpoint(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	config := domain.Config{
		AdminPassword: "Strong-Password-42!", TrafficThreshold: 95, ShutdownMode: "KeepCharging", ThresholdAction: "stop_and_notify", APIInterval: 600, Timezone: "Asia/Shanghai",
		EnableDailyReport: true,
	}
	if err = st.Setup(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, slog.Default(), 1)
	server := &Server{store: st, engine: eng}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/daily-report", strings.NewReader(`{}`))
	resp := httptest.NewRecorder()

	server.triggerDailyReport(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var job domain.Job
	if err = json.Unmarshal(resp.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Type != engine.JobDailyReport {
		t.Fatalf("expected job type %s, got %s", engine.JobDailyReport, job.Type)
	}
}
