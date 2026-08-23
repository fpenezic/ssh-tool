package ssh

import (
	"testing"

	"ssh-tool/internal/store"
)

// Max cert age used to be stored only in whole hours, which made a short age
// impossible to express: the editor floored everything to 1h, so testing a
// refresh meant waiting an hour. The seconds key fixes that, and has to win
// over the hours key without breaking credentials written before it existed.
func TestMaxCertAgeSecondsResolution(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want uint32
	}{
		{
			name: "seconds wins over hours",
			cfg:  map[string]any{"max_cert_age_seconds": float64(300), "max_cert_age_hours": float64(24)},
			want: 300,
		},
		{
			name: "legacy credential with only hours",
			cfg:  map[string]any{"max_cert_age_hours": float64(24)},
			want: 24 * 3600,
		},
		{
			name: "neither key set falls back to a week",
			cfg:  map[string]any{},
			want: 168 * 3600,
		},
		{
			name: "a zero seconds value is unset, not an instant expiry",
			cfg:  map[string]any{"max_cert_age_seconds": float64(0), "max_cert_age_hours": float64(48)},
			want: 48 * 3600,
		},
		{
			name: "zero in both keys still yields the default",
			cfg:  map[string]any{"max_cert_age_seconds": float64(0), "max_cert_age_hours": float64(0)},
			want: 168 * 3600,
		},
		{
			name: "sub-minute ages are honoured by the parser",
			cfg:  map[string]any{"max_cert_age_seconds": float64(30)},
			want: 30,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseOpksshConfig(&store.CredentialRef{ID: "c1", Config: tc.cfg})
			if err != nil {
				t.Fatalf("ParseOpksshConfig: %v", err)
			}
			if cfg.MaxCertAgeSeconds != tc.want {
				t.Fatalf("MaxCertAgeSeconds = %d, want %d", cfg.MaxCertAgeSeconds, tc.want)
			}
		})
	}
}
