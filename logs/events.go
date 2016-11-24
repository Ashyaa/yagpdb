package logs

import (
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/fzzy/radix/redis"
	"github.com/jinzhu/gorm"
	"github.com/jonas747/discordgo"
	"github.com/jonas747/dutil/commandsystem"
	"github.com/jonas747/yagpdb/commands"
	"github.com/jonas747/yagpdb/common"
	"strconv"
	"time"
)

func (p *Plugin) InitBot() {
	common.BotSession.AddHandler(HandleGuildmemberUpdate)
	common.BotSession.AddHandler(HandlePresenceUpdate)
	common.BotSession.AddHandler(HandleGuildCreate)

	commands.CommandSystem.RegisterCommands(cmds...)
}

var cmds = []commandsystem.CommandHandler{
	&commands.CustomCommand{
		Cooldown: 30,
		Category: commands.CategoryTool,
		SimpleCommand: &commandsystem.SimpleCommand{
			Name:        "Logs",
			Aliases:     []string{"ps", "paste", "pastebin", "log"},
			Description: "Creates a log of the channels last 100 messages",
		},
		RunFunc: func(cmd *commandsystem.ParsedCommand, client *redis.Client, m *discordgo.MessageCreate) (interface{}, error) {
			l, err := CreateChannelLog(m.ChannelID, m.Author.Username, m.Author.ID, 100)
			if err != nil {
				return "An error occured", err
			}

			return l.Link(), err
		},
	},
	&commands.CustomCommand{
		Cooldown: 10,
		Category: commands.CategoryTool,
		SimpleCommand: &commandsystem.SimpleCommand{
			Name:        "whois",
			Description: "shows users pervious username and nicknames",
			Aliases:     []string{"whoami"},
			RunInDm:     false,
			Arguments: []*commandsystem.ArgumentDef{
				{Name: "User", Type: commandsystem.ArgumentTypeUser},
			},
		},
		RunFunc: func(parsed *commandsystem.ParsedCommand, client *redis.Client, m *discordgo.MessageCreate) (interface{}, error) {
			target := m.Author
			if parsed.Args[0] != nil {
				target = parsed.Args[0].DiscordUser()
			}

			member, err := common.GetGuildMember(common.BotSession, parsed.Guild.ID, target.ID)
			if err != nil {
				return "An error occured fetching guild member, contact bot owner", err
			}

			usernames, err := GetUsernames(target.ID)
			if err != nil {
				return err, err
			}

			nicknames, err := GetNicknames(target.ID, parsed.Guild.ID)
			if err != nil {
				return err, err
			}

			nick := ""
			if member.Nick != "" {
				nick = " (" + member.Nick + ")"
			}
			header := fmt.Sprintf("**%s%s**:\n", target.Username, nick)
			header += fmt.Sprintf("Joined server at: %s", member.JoinedAt)

			body := "**Usernames:**```\n"
			for _, v := range usernames {
				body += fmt.Sprintf("%20s: %s\n", v.CreatedAt.Format(time.RFC822), v.Username)
			}
			body += "```\n\n"

			body += "**Nicknames:**```\n"
			if len(nicknames) < 1 {
				body += "No nicknames tracked"
			} else {
				for _, v := range nicknames {
					body += fmt.Sprintf("%20s: %s\n", v.CreatedAt.Format(time.RFC822), v.Nickname)
				}
			}
			body += "```"

			return header + "\n" + body, nil
		},
	},
}

// Guildmemberupdate is sent when user changes nick
func HandleGuildmemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	CheckNickname(m.User.ID, m.GuildID, m.Nick)
}

// While presence update is sent when user changes username.... MAKES NO SENSE IMO BUT WHATEVER
// Also check nickname incase the user came online
func HandlePresenceUpdate(s *discordgo.Session, m *discordgo.PresenceUpdate) {
	CheckNickname(m.User.ID, m.GuildID, m.Presence.Nick)
	if m.User.Username != "" {
		CheckUsername(m.User)
	}
}

func HandleGuildCreate(s *discordgo.Session, c *discordgo.GuildCreate) {
	started := time.Now()
	for _, v := range c.Members {
		CheckNickname(v.User.ID, c.Guild.ID, v.Nick)
		CheckUsername(v.User)
	}
	logrus.Infof("Checked %d members in %s", len(c.Members), time.Since(started).String())
}

type UsernameListing struct {
	gorm.Model
	UserID   int64 `gorm:"index"`
	Username string
}

type NicknameListing struct {
	gorm.Model
	UserID   int64 `gorm:"index"`
	GuildID  string
	Nickname string
}

func CheckUsername(user *discordgo.User) {
	var result UsernameListing
	err := common.SQL.Model(&result).Where(UsernameListing{UserID: MustParseID(user.ID)}).Last(&result).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		logrus.WithError(err).Error("Failed checking username for changes")
		return
	}

	if err == nil && result.Username == user.Username {
		return
	}

	logrus.Info("User changed username, old:", result.Username, "new:", user.Username)

	listing := UsernameListing{
		UserID:   MustParseID(user.ID),
		Username: user.Username,
	}

	err = common.SQL.Create(&listing).Error
	if err != nil {
		logrus.WithError(err).Error("Failed setting username")
	}
}

func CheckNickname(userID, guildID, nickname string) {
	var result NicknameListing
	err := common.SQL.Model(&result).Where(NicknameListing{UserID: MustParseID(userID), GuildID: guildID}).Last(&result).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		logrus.WithError(err).Error("Failed checking nickname for changes")
		return
	}

	if err == gorm.ErrRecordNotFound && nickname == "" {
		// don't need to be putting this in the database as the first record for the user
		return
	}

	if err == nil && result.Nickname == nickname {
		return
	}

	logrus.Info("User changed nickname, old:", result.Nickname, "new:", nickname)

	listing := NicknameListing{
		UserID:   MustParseID(userID),
		GuildID:  guildID,
		Nickname: nickname,
	}

	err = common.SQL.Create(&listing).Error
	if err != nil {
		logrus.WithError(err).Error("Failed setting nickname")
	}
}

func GetUsernames(userID string) ([]UsernameListing, error) {
	var listings []UsernameListing
	err := common.SQL.Where(&UsernameListing{UserID: MustParseID(userID)}).Order("id desc").Find(&listings).Error
	return listings, err
}

func GetNicknames(userID, GuildID string) ([]NicknameListing, error) {
	var listings []NicknameListing
	err := common.SQL.Where(&NicknameListing{UserID: MustParseID(userID), GuildID: GuildID}).Order("id desc").Find(&listings).Error
	return listings, err
}

func MustParseID(id string) int64 {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		panic("Failed parsing id: " + err.Error())
	}

	return v
}