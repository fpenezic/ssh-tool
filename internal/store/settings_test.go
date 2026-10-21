package store

import "testing"

func TestSettingsWithPrefix(t *testing.T) {
	d := openTestDB(t)
	for k, v := range map[string]string{
		"fleet_facts:f1":      "a",
		"fleet_facts:f2":      "b",
		"fleet_facts_hist:f1": "hist",
		"fleetXfacts:f3":      "wildcard",
		"theme":               "latte",
	} {
		if err := d.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.SettingsWithPrefix("fleet_facts:")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["f1"] != "a" || got["f2"] != "b" {
		t.Fatalf("got %v, want f1=a f2=b only", got)
	}
}
