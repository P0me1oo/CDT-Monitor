package domain

import (
	"testing"
	"time"
)

func TestRotationValidatesCoverageAndCrossMidnight(t *testing.T) {
	accounts := []Account{{ID: 1, InstanceID: "a", MaxTraffic: 100}, {ID: 2, InstanceID: "b", MaxTraffic: 100}, {ID: 3, InstanceID: "c", MaxTraffic: 100}}
	r := RotationConfig{Enabled: true, Token: "test", Hostname: "proxy.example.com", Slots: []RotationSlot{{1, "22:00", "06:00"}, {2, "06:00", "14:00"}, {3, "14:00", "22:00"}}}
	if err := r.Validate(accounts); err != nil {
		t.Fatal(err)
	}
	for _, clock := range []string{"23:59", "00:00", "05:59"} {
		at, _ := time.Parse("15:04", clock)
		if r.Slots[r.SlotAt(at)].AccountID != 1 {
			t.Fatalf("wrong overnight owner at %s", clock)
		}
	}
	for _, test := range []struct {
		name  string
		slots []RotationSlot
	}{
		{"gap", []RotationSlot{{1, "00:00", "08:00"}, {2, "09:00", "00:00"}}},
		{"overlap", []RotationSlot{{1, "00:00", "13:00"}, {2, "12:00", "00:00"}}},
		{"duplicate", []RotationSlot{{1, "00:00", "12:00"}, {1, "12:00", "00:00"}}},
		{"missing", []RotationSlot{{1, "00:00", "12:00"}, {9, "12:00", "00:00"}}},
		{"invalid", []RotationSlot{{1, "00:00", "24:00"}, {2, "12:00", "00:00"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := r
			copy.Slots = test.slots
			if copy.Validate(accounts) == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}
