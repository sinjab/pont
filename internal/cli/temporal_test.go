package cli

// ParseTimeframe table tests (consult §7): structural invariants for
// every preset (parseable RFC3339, after < before) plus anchor checks
// that pin the calendar math (yesterday ends at today midnight,
// this_week starts Monday, last_week is exactly the previous Mon–Sun,
// this_month starts on the 1st).
import (
	"testing"
	"time"
)

func TestParseTimeframeInvalid(t *testing.T) {
	if _, _, err := ParseTimeframe("fortnight"); err == nil {
		t.Error("invalid preset must error")
	}
	a, b, err := ParseTimeframe("")
	if err != nil || a != "" || b != "" {
		t.Errorf("empty preset: want empty window, got %q %q err=%v", a, b, err)
	}
}

func TestParseTimeframePresetsParseAndOrder(t *testing.T) {
	for _, preset := range []string{
		"last_hour", "today", "yesterday", "last_3_days",
		"this_week", "last_week", "this_month",
	} {
		after, before, err := ParseTimeframe(preset)
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		at, err1 := time.Parse(time.RFC3339, after)
		bt, err2 := time.Parse(time.RFC3339, before)
		if err1 != nil || err2 != nil {
			t.Errorf("%s: non-RFC3339 output %q / %q", preset, after, before)
			continue
		}
		if !at.Before(bt) {
			t.Errorf("%s: after %s not before %s", preset, after, before)
		}
	}
}

func TestParseTimeframeCalendarAnchors(t *testing.T) {
	now := time.Now()

	_, yb, _ := ParseTimeframe("yesterday")
	ybt, _ := time.Parse(time.RFC3339, yb)
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if ybt.Sub(todayMidnight) != 0 {
		t.Errorf("yesterday.before = %s, want today midnight %s", yb, todayMidnight)
	}

	wa, _, _ := ParseTimeframe("this_week")
	wat, _ := time.Parse(time.RFC3339, wa)
	if wat.Weekday() != time.Monday || (wat.Hour() != 0 || wat.Minute() != 0) {
		t.Errorf("this_week.after = %s, want Monday midnight", wa)
	}
	if wat.After(now) {
		t.Errorf("this_week.after %s is in the future", wa)
	}

	lwa, lwb, _ := ParseTimeframe("last_week")
	lwat, _ := time.Parse(time.RFC3339, lwa)
	lwbt, _ := time.Parse(time.RFC3339, lwb)
	if lwat.Weekday() != time.Monday {
		t.Errorf("last_week.after weekday = %s, want Monday", lwat.Weekday())
	}
	span := lwbt.Sub(lwat)
	if span < 6*24*time.Hour || span >= 7*24*time.Hour {
		t.Errorf("last_week span = %s, want just under 7 days", span)
	}

	ma, _, _ := ParseTimeframe("this_month")
	mat, _ := time.Parse(time.RFC3339, ma)
	if mat.Day() != 1 {
		t.Errorf("this_month.after day = %d, want 1", mat.Day())
	}

	ha, _, _ := ParseTimeframe("last_hour")
	hat, _ := time.Parse(time.RFC3339, ha)
	if now.Sub(hat) > 2*time.Hour || now.Sub(hat) < 30*time.Minute {
		t.Errorf("last_hour.after %s not ~1h ago (now %s)", ha, now)
	}
}
