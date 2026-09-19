package store

import (
	"github.com/P0me1oo/CDT-Monitor/internal/domain"
	"strings"
	"testing"
)

func TestRotationTokenEncryptedAndBlankPreserves(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := domain.RotationConfig{Token: "rotation-private-test-value", Hostname: "proxy.example.com", Slots: []domain.RotationSlot{}}
	if err = st.SaveRotation(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = st.db.QueryRow(`SELECT value FROM settings WHERE key='rotation_config'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, r.Token) {
		t.Fatal("token stored in plaintext")
	}
	r.Token = ""
	if err = st.SaveRotation(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetRotation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != "rotation-private-test-value" || !loaded.TokenConfigured {
		t.Fatal("blank token did not preserve secret")
	}
}
