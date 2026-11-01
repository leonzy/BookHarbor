package localmeta

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"talebook/booksvr/internal/importmeta"
)

var datedLectureName = regexp.MustCompile(`^([0-9]{6})([^\r\n《》]+?)[-－—]([\p{Han}]{2,4})$`)
var speakerProfile = regexp.MustCompile(`(?m)^\s*主讲人(?:简介|介绍)\s*[:：]\s*`)

// DatedLectureFilename is only a structural hint; extraction also needs body evidence.
func DatedLectureFilename(name string) (title, author string) {
	m := datedLectureName.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return "", ""
	}
	if _, err := time.Parse("060102", m[1]); err != nil {
		return "", ""
	}
	title = strings.TrimSpace(m[2])
	if utf8.RuneCountInString(title) < 2 || utf8.RuneCountInString(title) > 80 {
		return "", ""
	}
	return title, m[3]
}

func lectureText(name, text string) importmeta.Metadata {
	title, author := DatedLectureFilename(name)
	if title == "" || !strings.Contains(text, "《"+title+"》") {
		return importmeta.Metadata{}
	}
	if len(text) > 4096 {
		text = text[:4096]
	}
	profile := speakerProfile.FindStringIndex(text)
	if profile == nil {
		return importmeta.Metadata{}
	}
	intro := strings.TrimSpace(text[profile[1]:])
	if !strings.HasPrefix(intro, author) {
		return importmeta.Metadata{}
	}
	suffix := strings.TrimPrefix(intro, author)
	if suffix != "" && !strings.ContainsRune("，,。 \t\r\n", []rune(suffix)[0]) {
		return importmeta.Metadata{}
	}
	return importmeta.Metadata{Title: title, Authors: []string{author}}
}
