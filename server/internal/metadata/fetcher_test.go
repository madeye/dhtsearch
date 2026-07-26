package metadata

import "testing"

// The zero infohash reaches AddMagnet as a panic, not an error, so rejecting
// it before the library sees it is what keeps a worker from taking the
// process down.
func TestValidInfohash(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"typical", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4", true},
		{"uppercase", "97A29535B9E2F0E46A1B09A4B0C0E4F0A1B2C3D4", true},
		{"all zeros", "0000000000000000000000000000000000000000", false},
		{"empty", "", false},
		{"too short", "97a29535", false},
		{"too long", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4f", false},
		{"non-hex", "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3zz", false},
		{"leading zeros are fine", "0000000000000000000000000000000000000001", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validInfohash(tc.in); got != tc.want {
				t.Fatalf("validInfohash(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
