package reminders_test

import (
	"testing"

	"github.com/botlabs-gg/yagpdb/v2/reminders"
)

func TestTimezones(t *testing.T) {
	t.Run("", func(t *testing.T) {
		res := reminders.Timezones()
		if len(res) == 0 {
			t.Error()
		}
	})
}
