package reminders

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/botlabs-gg/yagpdb/v2/bot"
	"github.com/botlabs-gg/yagpdb/v2/commands"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/common/scheduledevents2"
	seventsmodels "github.com/botlabs-gg/yagpdb/v2/common/scheduledevents2/models"
	"github.com/botlabs-gg/yagpdb/v2/lib/dcmd"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
	"github.com/botlabs-gg/yagpdb/v2/lib/dstate"
	"github.com/jinzhu/gorm"
	"github.com/lithammer/fuzzysearch/fuzzy"
)

var logger = common.GetPluginLogger(&Plugin{})

var _ bot.BotInitHandler = (*Plugin)(nil)
var _ commands.CommandProvider = (*Plugin)(nil)

func (p *Plugin) AddCommands() {
	commands.AddRootCommands(p, cmds...)
}

func (p *Plugin) BotInit() {
	// scheduledevents.RegisterEventHandler("reminders_check_user", checkUserEvtHandlerLegacy)
	scheduledevents2.RegisterHandler("reminders_check_user", int64(0), checkUserScheduledEvent)
	scheduledevents2.RegisterLegacyMigrater("reminders_check_user", migrateLegacyScheduledEvents)
}

// Reminder management commands
var cmds = []*commands.YAGCommand{
	{
		CmdCategory:  commands.CategoryTool,
		Name:         "Remindme",
		Description:  "Schedules a reminder, example: 'remindme 1h30min are you still alive?'",
		Aliases:      []string{"remind", "reminder"},
		RequiredArgs: 1,
		Arguments: []*dcmd.ArgDef{
			{Name: "Message", Type: dcmd.String, Help: "Message to display"},
		},
		ArgSwitches: []*dcmd.ArgDef{
			{Name: "time", Type: &commands.DurationArg{}, Help: "Relative reminder delay e.g. 90s for \"in 1 minute and 30s\". Exclusive with absolute time fields."},
			{Name: "year", Type: &dcmd.IntArg{}, Help: "Year of the reminder. Defaults to current year. Exclusive with time."},
			{Name: "month", Type: &dcmd.IntArg{}, Help: "Month of the reminder (1-12). Defaults to current month. Exclusive with time."},
			{Name: "day", Type: &dcmd.IntArg{}, Help: "Day of the reminder (1-31). Defaults to current day. Exclusive with time."},
			{Name: "hour", Type: &dcmd.IntArg{}, Help: "Hour of the reminder (0-23). Defaults to current hour. Exclusive with time."},
			{Name: "minute", Type: &dcmd.IntArg{}, Help: "Minute of the reminder (0-59). Defaults to 0. Exclusive with time."},
			{Name: "second", Type: &dcmd.IntArg{}, Help: "Second of the reminder (0-59). Defaults to 0. Exclusive with time."},
			{
				Name: "zone", Type: &dcmd.StringArg{},
				Help: "Timezone of the reminder date & time. Defaults to GMT. Exclusive with time.",
				// Default: "GMT",
				// Choices: GetTimeZoneChoices(),
				AutocompleteFunc: func(data *dcmd.Data, arg *dcmd.ParsedArg) (choices []*discordgo.ApplicationCommandOptionChoice, err error) {
					candidate := arg.Value.(string)
					matches := fuzzy.RankFindNormalizedFold(candidate, TimeZoneChoices)
					sort.Sort(matches)

					cap := len(matches)
					if cap > 25 {
						cap = 25
					}
					for _, match := range matches {
						choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
							Name:  match.Target,
							Value: match.Target,
						})
						if len(choices) == cap {
							break
						}
					}

					return choices, nil
				},
			},
			{Name: "channel", Type: dcmd.Channel},
		},
		SlashCommandEnabled: true,
		DefaultEnabled:      true,
		RunInDM:             true,
		RunFunc: func(parsed *dcmd.Data) (interface{}, error) {
			currentReminders, _ := GetUserReminders(parsed.Author.ID)
			logger.Info("checking reminders limit")
			if len(currentReminders) >= 100 {
				return "You can have a maximum of 100 active reminders, list your reminders with the `reminders` command", nil
			}

			logger.Info("checking reminder author")
			if parsed.Author.Bot {
				return nil, errors.New("cannot create reminder for Bots, you're most likely trying to use `execAdmin` to create a reminder, use `exec` instead")
			}

			logger.Info("relative or absolute?")
			rel, err := usesRelativeTime(parsed)
			if err != nil {
				return err.Error(), nil
			}
			var when time.Time
			var durString string
			if rel {
				logger.Info("parsing relative")
				when, durString, err = parseRelativeTime(parsed)
			} else {
				logger.Info("parsing absolute")
				when, durString, err = parseAbsoluteTime(parsed)
			}
			if err != nil {
				return err.Error(), nil
			}

			logger.Info("checking relative time < 10y")
			if when.After(time.Now().Add(time.Hour * 24 * 365 * 10)) {
				return "Can be max 10 years from now.", nil
			}

			id := parsed.ChannelID
			logger.Info("checking channel")
			if c := parsed.Switch("channel"); c.Value != nil {
				id = c.Value.(*dstate.ChannelState).ID

				hasPerms, err := bot.AdminOrPermMS(parsed.GuildData.GS.ID, id, parsed.GuildData.MS, discordgo.PermissionSendMessages|discordgo.PermissionReadMessages)
				if err != nil {
					return "Failed checking permissions, please try again or join the support server.", err
				}

				if !hasPerms {
					return "You do not have permissions to send messages there", nil
				}
			}

			var gid int64 = -1
			logger.Info("checking if in DM")
			if parsed.GuildData != nil {
				gid = parsed.GuildData.GS.ID
			}
			_, err = NewReminder(parsed.Author.ID, gid, id, parsed.Args[0].Str(), when)
			if err != nil {
				return nil, err
			}
			return "Set a reminder in " + durString + " from now (<t:" + fmt.Sprint(when.Unix()) + ":f>)\nView reminders with the `reminders` command", nil
		},
	},
	{
		CmdCategory:         commands.CategoryTool,
		Name:                "Reminders",
		Description:         "Lists your active reminders",
		SlashCommandEnabled: true,
		DefaultEnabled:      true,
		IsResponseEphemeral: true,
		RunInDM:             true,
		RunFunc: func(parsed *dcmd.Data) (interface{}, error) {
			currentReminders, err := GetUserReminders(parsed.Author.ID)
			if err != nil {
				return nil, err
			}

			if len(currentReminders) == 0 {
				return "You have no reminders. Create reminders with the `remindme` command.", nil
			}

			out := "Your reminders:\n"
			out += stringReminders(currentReminders, false)
			out += "\nRemove a reminder with `delreminder/rmreminder (id)` where id is the first number for each reminder above.\nTo clear all reminders, use `delreminder` with the `-a` switch."
			return out, nil
		},
	},
	{
		CmdCategory:         commands.CategoryTool,
		Name:                "CReminders",
		Aliases:             []string{"channelreminders"},
		Description:         "Lists reminders in channel, only users with 'manage channel' permissions can use this.",
		SlashCommandEnabled: true,
		DefaultEnabled:      true,
		IsResponseEphemeral: true,
		RunInDM:             true,
		RunFunc: func(parsed *dcmd.Data) (interface{}, error) {
			if parsed.GuildData != nil {
				ok, err := bot.AdminOrPermMS(parsed.GuildData.GS.ID, parsed.ChannelID, parsed.GuildData.MS, discordgo.PermissionManageChannels)
				if err != nil {
					return nil, err
				}
				if !ok {
					return "You do not have access to this command (requires manage channel permission)", nil
				}
			}

			currentReminders, err := GetChannelReminders(parsed.ChannelID)
			if err != nil {
				return nil, err
			}

			if len(currentReminders) == 0 {
				return "There are no reminders in this channel.", nil
			}

			out := "Reminders in this channel:\n"
			out += stringReminders(currentReminders, true)
			out += "\nRemove a reminder with `delreminder/rmreminder (id)` where id is the first number for each reminder above"
			return out, nil
		},
	},
	{
		CmdCategory:  commands.CategoryTool,
		Name:         "DelReminder",
		Aliases:      []string{"rmreminder"},
		Description:  "Deletes a reminder. You can delete reminders from other users provided you are running this command in the same guild the reminder was created in and have the Manage Channel permission in the channel the reminder was created in.",
		RequiredArgs: 0,
		Arguments: []*dcmd.ArgDef{
			{Name: "ID", Type: dcmd.Int},
		},
		ArgSwitches: []*dcmd.ArgDef{
			{Name: "a", Help: "All"},
		},
		SlashCommandEnabled: true,
		DefaultEnabled:      true,
		IsResponseEphemeral: true,
		RunInDM:             true,
		RunFunc: func(parsed *dcmd.Data) (interface{}, error) {
			var reminder Reminder

			clearAll := parsed.Switch("a").Value != nil && parsed.Switch("a").Value.(bool)
			if clearAll {
				db := common.GORM.Where("user_id = ?", parsed.Author.ID).Delete(&reminder)
				err := db.Error
				if err != nil {
					return "Error clearing reminders", err
				}

				count := db.RowsAffected
				if count == 0 {
					return "No reminders to clear", nil
				}
				return fmt.Sprintf("Cleared %d reminders", count), nil
			}

			if len(parsed.Args) == 0 || parsed.Args[0].Value == nil {
				return "No reminder ID provided", nil
			}

			err := common.GORM.Where(parsed.Args[0].Int()).First(&reminder).Error
			if err != nil {
				if err == gorm.ErrRecordNotFound {
					return "No reminder by that id found", nil
				}
				return "Error retrieving reminder", err
			}

			// Check perms
			if reminder.UserID != discordgo.StrID(parsed.Author.ID) {
				if reminder.GuildID != parsed.GuildData.GS.ID {
					return "You can only delete reminders that are not your own in the guild the reminder was originally created", nil
				}
				ok, err := bot.AdminOrPermMS(reminder.GuildID, reminder.ChannelIDInt(), parsed.GuildData.MS, discordgo.PermissionManageChannels)
				if err != nil {
					return nil, err
				}
				if !ok {
					return "You need manage channel permission in the channel the reminder is in to delete reminders that are not your own", nil
				}
			}

			// Do the actual deletion
			err = common.GORM.Delete(reminder).Error
			if err != nil {
				return nil, err
			}

			// Check if we should remove the scheduled event
			currentReminders, err := GetUserReminders(reminder.UserIDInt())
			if err != nil {
				return nil, err
			}

			delMsg := fmt.Sprintf("Deleted reminder **#%d**: '%s'", reminder.ID, limitString(reminder.Message))

			// If there is another reminder with the same timestamp, do not remove the scheduled event
			for _, v := range currentReminders {
				if v.When == reminder.When {
					return delMsg, nil
				}
			}

			return delMsg, nil
		},
	},
}

var absoluteTimeFields = []string{
	"year",
	"month",
	"day",
	"hour",
	"minute",
	"second",
	"zone",
}

func parseRelativeTime(parsed *dcmd.Data) (time.Time, string, error) {
	fromNow := parsed.Switch("time").Value.(time.Duration)

	durString := common.HumanizeDuration(common.DurationPrecisionSeconds, fromNow)
	when := time.Now().Add(fromNow)

	return when, durString, nil
}

func parseAbsoluteTime(parsed *dcmd.Data) (time.Time, string, error) {
	var year, day, hour, minute, second int
	var month time.Month

	tz := "GMT"
	if raw := parsed.Switch("zone"); raw.Value != nil {
		tz = raw.Value.(string)
	}

	location, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("Invalid timezone: %s", tz)
	}

	now := time.Now().In(location)

	if raw := parsed.Switch("year"); raw.Value != nil {
		year = int(raw.Value.(int64))
		if year < now.Year() {
			return time.Time{}, "", fmt.Errorf("Year %d is in the past", year)
		}
	} else {
		year = now.Year()
	}
	if raw := parsed.Switch("month"); raw.Value != nil {
		month = time.Month(int(raw.Value.(int64)))
		if month < 1 || month > 12 {
			return time.Time{}, "", fmt.Errorf("Invalid month: %d", month)
		}
	} else {
		month = now.Month()
	}
	if raw := parsed.Switch("day"); raw.Value != nil {
		day = int(raw.Value.(int64))
		if day < 1 || day > 31 {
			return time.Time{}, "", fmt.Errorf("Invalid day: %d", day)
		}
	} else {
		day = now.Day()
	}
	if raw := parsed.Switch("hour"); raw.Value != nil {
		hour = int(raw.Value.(int64))
		if hour < 0 || hour > 23 {
			return time.Time{}, "", fmt.Errorf("Invalid hour: %d", hour)
		}
	} else {
		hour = now.Hour()
	}
	if raw := parsed.Switch("minute"); raw.Value != nil {
		minute = int(raw.Value.(int64))
		if minute < 0 || minute > 59 {
			return time.Time{}, "", fmt.Errorf("Invalid minute: %d", minute)
		}
	} else {
		minute = 0
	}
	if raw := parsed.Switch("second"); raw.Value != nil {
		second = int(raw.Value.(int64))
		if second < 0 || second > 59 {
			return time.Time{}, "", fmt.Errorf("Invalid second: %d", second)
		}
	} else {
		second = 0
	}

	when := time.Date(year, month, day, hour, minute, second, 0, location)

	if now.After(when) {
		return time.Time{}, "", fmt.Errorf("%s is in the past", when)
	}
	fromNow := when.Sub(now)

	durString := common.HumanizeDuration(common.DurationPrecisionSeconds, fromNow)

	return when, durString, nil
}

func usesRelativeTime(parsed *dcmd.Data) (bool, error) {
	relative := parsed.Switch("time").Value != nil
	absolute := false
	for _, field := range absoluteTimeFields {
		fieldUsed := parsed.Switch(field).Value != nil
		if relative && fieldUsed {
			return false, fmt.Errorf("Exclusive fields \"time\" and \"%s\" cannot be used together.", field)
		}
		absolute = absolute || fieldUsed
	}
	if !relative && !absolute {
		return false, fmt.Errorf("No relative or absolute time given.")
	}
	return relative, nil
}

func stringReminders(reminders []*Reminder, displayUsernames bool) string {
	out := ""
	for _, v := range reminders {
		parsedCID, _ := strconv.ParseInt(v.ChannelID, 10, 64)

		t := time.Unix(v.When, 0)
		tUnix := t.Unix()
		timeFromNow := common.HumanizeTime(common.DurationPrecisionMinutes, t)
		if !displayUsernames {
			channel := "<#" + discordgo.StrID(parsedCID) + ">"
			out += fmt.Sprintf("**%d**: %s: '%s' - %s from now (<t:%d:f>)\n", v.ID, channel, limitString(v.Message), timeFromNow, tUnix)
		} else {
			member, _ := bot.GetMember(v.GuildID, v.UserIDInt())
			username := "Unknown user"
			if member != nil {
				username = member.User.Username
			}
			out += fmt.Sprintf("**%d**: %s: '%s' - %s from now (<t:%d:f>)\n", v.ID, username, limitString(v.Message), timeFromNow, tUnix)
		}
	}
	return out
}

func checkUserScheduledEvent(evt *seventsmodels.ScheduledEvent, data interface{}) (retry bool, err error) {
	// !important! the evt.GuildID can be 1 in cases where it was migrated from the legacy scheduled event system

	userID := *data.(*int64)

	reminders, err := GetUserReminders(userID)
	if err != nil {
		return true, err
	}

	now := time.Now()
	nowUnix := now.Unix()
	for _, v := range reminders {
		if v.When <= nowUnix {
			err := v.Trigger()
			if err != nil {
				// possibly try again
				return scheduledevents2.CheckDiscordErrRetry(err), err
			}
		}
	}

	return false, nil
}

func migrateLegacyScheduledEvents(t time.Time, data string) error {
	split := strings.Split(data, ":")
	if len(split) < 2 {
		logger.Error("invalid check user scheduled event: ", data)
		return nil
	}

	parsed, _ := strconv.ParseInt(split[1], 10, 64)

	return scheduledevents2.ScheduleEvent("reminders_check_user", 1, t, parsed)
}

func limitString(s string) string {
	if utf8.RuneCountInString(s) < 50 {
		return s
	}

	runes := []rune(s)
	return string(runes[:47]) + "..."
}
