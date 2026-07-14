package templates

import "testing"

func TestListReturnsAllTemplates(t *testing.T) {
	ts, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"i2c_pullups",
		"mcu_minimal_atmega328",
		"opamp_noninverting",
		"voltage_divider",
		"voltage_regulator_linear",
	}
	if len(ts) != len(want) {
		t.Fatalf("List returned %d templates, want %d: %+v", len(ts), len(want), ts)
	}
	for i, w := range want {
		if ts[i].Name != w {
			t.Errorf("templates[%d] = %q, want %q", i, ts[i].Name, w)
		}
	}
}

func TestGetByName(t *testing.T) {
	got, err := Get("opamp_noninverting")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Components) < 3 {
		t.Errorf("opamp_noninverting components = %d, want ≥ 3", len(got.Components))
	}
	if len(got.Nets) == 0 {
		t.Errorf("opamp_noninverting nets is empty")
	}
}
