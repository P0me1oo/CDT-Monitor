package httpapi

import (
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"github.com/P0me1oo/CDT-Monitor/internal/store"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestRotationConfigNeverReturnsToken(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SaveRotation(t.Context(), domain.RotationConfig{Token: "rotation-private-test-token"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	w := httptest.NewRecorder()
	s.getRotation(w, httptest.NewRequest("GET", "/api/v1/rotation", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "rotation-private-test-token") || !strings.Contains(w.Body.String(), `"token_configured":true`) {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestRotationEndpointsRequireAuthentication(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, nil, fstest.MapFS{}, slog.Default())
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/rotation", strings.NewReader(`{}`))
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d", method, w.Code)
		}
	}
}

func TestRotationWriteRequiresCSRF(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, err := st.CreateSession(t.Context(), "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, fstest.MapFS{}, slog.Default())
	r := httptest.NewRequest(http.MethodPut, "/api/v1/rotation", strings.NewReader(`{}`))
	r.AddCookie(&http.Cookie{Name: "cdt_session", Value: token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
