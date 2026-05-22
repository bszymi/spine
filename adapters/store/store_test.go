package store

import "testing"

// TestArtifactQuery_ClampedLimit guards the store-boundary pagination
// contract: non-positive limits become the default, anything over the
// max is capped, and values inside the band pass through. The
// QueryArtifacts SQL interpolates this value directly into the LIMIT
// clause, so a regression here would either silently scan unbounded
// rows or return surprising row counts to internal callers.
func TestArtifactQuery_ClampedLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero defaults", 0, ArtifactQueryDefaultLimit},
		{"negative defaults", -42, ArtifactQueryDefaultLimit},
		{"low passes through", 1, 1},
		{"normal passes through", 50, 50},
		{"at-cap passes through", ArtifactQueryMaxLimit, ArtifactQueryMaxLimit},
		{"over-cap clamps", ArtifactQueryMaxLimit + 1, ArtifactQueryMaxLimit},
		{"huge clamps", 1_000_000, ArtifactQueryMaxLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ArtifactQuery{Limit: c.in}.ClampedLimit()
			if got != c.want {
				t.Errorf("ArtifactQuery{Limit: %d}.ClampedLimit() = %d, want %d",
					c.in, got, c.want)
			}
		})
	}
}
