package localmeta

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var prefaceHeading = regexp.MustCompile(`^(?:前言|自序|作者序|作者前言)(?:[ \t]*[（(].*[）)])?$`)
var prefaceSignature = regexp.MustCompile(`(?m)^[ \t]*([\p{Han}]{2,5})[ \t]*\n[ \t]*(?:19|20)[0-9]{2}年[^\n]{0,20}\n[ \t]*于[\p{Han}]{2,15}[ \t]*(?:\n|$)`)

// A signed self-preface can supply a missing author, but an endorsement or a
// person's name in ordinary prose cannot. Signature dates are not publication dates.
func prefaceAuthor(text, title string) string {
	title = strings.TrimSpace(SearchTitle(title))
	if utf8.RuneCountInString(title) < 2 {
		return ""
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	var author string
	for i, line := range lines {
		if !prefaceHeading.MatchString(strings.TrimSpace(line)) {
			continue
		}
		end := min(i+240, len(lines))
		section := strings.Join(lines[i+1:end], "\n")
		if len(section) > 32<<10 {
			section = section[:32<<10]
		}
		if !strings.Contains(section, "《"+title+"》") || !(strings.Contains(section, "我写这本书") || strings.Contains(section, "写这本书，是我") || strings.Contains(section, "我撰写本书")) {
			continue
		}
		for _, signature := range prefaceSignature.FindAllStringSubmatch(section, -1) {
			if author != "" && author != signature[1] {
				return ""
			}
			author = signature[1]
		}
	}
	return author
}
