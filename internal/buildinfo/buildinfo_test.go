package buildinfo

import "testing"

func TestSelfUpdateRequiresExplicitEnabledValue(t *testing.T) {
	original := SelfUpdate
	t.Cleanup(func() { SelfUpdate = original })
	for _, value := range []string{"enabled", "disabled", "", "Enabled", "true", "typo"} {
		t.Run(value, func(t *testing.T) {
			SelfUpdate = value
			if got, want := SelfUpdateEnabled(), value == "enabled"; got != want {
				t.Fatalf("SelfUpdateEnabled(%q) = %t, want %t", value, got, want)
			}
		})
	}
}
