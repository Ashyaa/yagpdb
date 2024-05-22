package reminders

import (
	"os"
	"strings"
)

var zoneDirs = []string{
	"/usr/share/zoneinfo/",
	"/usr/share/lib/zoneinfo/",
	"/usr/lib/locale/TZ/",
}

func Timezones() (res []string) {
	for _, zoneDir := range zoneDirs {
		res = append(res, ReadFile(zoneDir, "")...)
	}
	return res
}

func ReadFile(zoneDir, path string) (res []string) {
	files, err := os.ReadDir(zoneDir + path)
	if err != nil {
		return
	}
	for _, f := range files {
		if f.Name() != strings.ToUpper(f.Name()[:1])+f.Name()[1:] {
			continue
		}
		if f.IsDir() {
			res = append(res, ReadFile(zoneDir, path+"/"+f.Name())...)
		} else {
			res = append(res, (path + "/" + f.Name())[1:])
		}
	}
	return
}

func GetTimeZoneChoices() (res []string) {
	for _, timezone := range Timezones() {
		res = append(res, timezone)
	}
	return res
}

var (
	TimeZoneChoices = GetTimeZoneChoices()
)
