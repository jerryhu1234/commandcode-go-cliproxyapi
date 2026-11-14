package predictor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFirstDepletionIdentity(t *testing.T) {
	now := time.Now().UTC()
	for _, mode := range []string{"earliest", "tie", "reset", "stale", "empty"} {
		t.Run(mode, func(t *testing.T) {
			s := NewStore(24 * time.Hour)
			want := "b"
			if mode != "empty" {
				seedLinear(s, "a", now, 4, 55, 50, 100)
				seedLinear(s, "b", now, 4, 34, 30, 100)
			}
			if mode == "tie" {
				s = NewStore(24 * time.Hour)
				seedLinear(s, "a", now, 4, 34, 30, 100)
				seedLinear(s, "b", now, 4, 34, 30, 100)
				want = "a"
			}
			if mode == "reset" {
				reset := now.Add(time.Hour)
				// Without this reset a would deplete in 2h, before b's 30h.
				s = NewStore(24 * time.Hour)
				seedLinear(s, "b", now, 4, 34, 30, 100)
				for i, rem := range []float64{30, 10} {
					s.Apply(AccountSnapshot{KeyID: "a", At: now.Add(time.Duration(i-1) * 4 * time.Hour), Included: &USDWindow{Remaining: rem, Cap: 100, ResetsAt: reset}})
				}
			}
			if mode == "stale" {
				s = NewStore(24 * time.Hour)
				seedLinear(s, "a", now, 4, 55, 50, 100)
				seedLinear(s, "b", now.Add(-10*time.Hour), 4, 34, 30, 100)
				want = "a"
			}
			if mode == "empty" {
				want = ""
			}
			view := BuildFleetView(s, []string{"a", "b"}, FleetOptions{Now: now, Horizon: 48 * time.Hour, StaleAfter: 3 * time.Hour, Src: SamplingRateSource{}}, nil)
			b, _ := json.Marshal(view)
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			if want == "" {
				if fields["first_depletion_key_id"] != nil {
					t.Fatalf("unexpected identity: %s", b)
				}
			} else if fields["first_depletion_key_id"] != want || fields["first_depletion_window"] != WindowIncluded {
				t.Fatalf("expected winner %s/%s: %s", want, WindowIncluded, b)
			}
		})
	}
}
