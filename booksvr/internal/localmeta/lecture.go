package localmeta

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"talebook/booksvr/internal/importmeta"
)

var datedLectureName = regexp.MustCompile(`^([0-9]{6})([^\r\n《》]+?)[-－—]([\p{Han}]{2,4})$`)
var speakerProfile = regexp.MustCompile(`(?m)^[\t \p{Zs}]*(?:主讲人(?:简介|介绍)|主讲简介|人物简介)(?:[\t \p{Zs}]*[:：][\t \p{Zs}]*|[\t \p{Zs}]*\r?\n)`)
var lecturePart = regexp.MustCompile(`[（(](?:上|中|下|上集|中集|下集|[一二三四五六七八九十0-9]+)[）)]$`)
var profilePerson = regexp.MustCompile(`(?m)^[\t \p{Zs}]*([\p{Han}](?:[\t \p{Zs}]*[\p{Han}]){1,3})[\t \p{Zs}]*[:：]([^\r\n]+)`)
var lectureHeadline = regexp.MustCompile(`《([^《》\r\n]+)》[（(]全文[）)]`)
var profileEnd = regexp.MustCompile(`(?m)^[\t \p{Zs}]*(?:内容简介|讲座简介|全文|正文)[\t \p{Zs}]*[:：]?[\t \p{Zs}]*(?:\r?\n|$)`)

func lectureNormal(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

// LectureAuthorsMatch keeps multiple speakers tied to the labelled profile list.
func LectureAuthorsMatch(authors []string, filenameAuthor string) bool {
	if len(authors) == 1 && authors[0] == filenameAuthor {
		return true
	}
	return len(authors) > 1 && strings.HasSuffix(filenameAuthor, "等") && authors[0] == strings.TrimSuffix(filenameAuthor, "等")
}

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
	if title == "" {
		return importmeta.Metadata{}
	}
	evidence := text
	if len(evidence) > 64<<10 {
		evidence = evidence[:64<<10]
	}
	titleConfirmed := strings.Contains(evidence, "《"+title+"》")
	base := lectureNormal(lecturePart.ReplaceAllString(title, ""))
	titleConfirmed = titleConfirmed || (utf8.RuneCountInString(base) >= 4 && strings.Contains(lectureNormal(evidence), base))
	if !titleConfirmed {
		// Structured lecture exports often omit a repeated title in the body.
		// Their labelled speaker profile corroborates the author in the filename.
		if !strings.Contains(evidence, "内容简介") {
			return importmeta.Metadata{}
		}
		headline := lectureHeadline.FindStringSubmatch(evidence)
		if headline != nil && lectureNormal(headline[1]) != lectureNormal(title) {
			return importmeta.Metadata{}
		}
	}
	if len(text) > 4096 {
		text = text[:4096]
	}
	profile := speakerProfile.FindStringIndex(text)
	if profile == nil {
		return importmeta.Metadata{}
	}
	intro := strings.TrimSpace(text[profile[1]:])
	if strings.HasSuffix(author, "等") && !strings.HasPrefix(intro, author) {
		if end := profileEnd.FindStringIndex(intro); end != nil {
			intro = intro[:end[0]]
		}
		people := profilePerson.FindAllStringSubmatch(intro, -1)
		var authors []string
		seen := map[string]bool{}
		for _, person := range people {
			name := lectureNormal(person[1])
			if strings.Contains("|职务|单位|学历|职称|出生|简历|主要著作|研究领域|研究方向|著作目录|主要成果|", "|"+name+"|") {
				continue
			}
			if len(authors) == 0 && name != strings.TrimSuffix(author, "等") {
				return importmeta.Metadata{}
			}
			if len(authors) > 0 {
				professional := false
				for _, word := range []string{"教授", "主任", "研究员", "总裁", "博士", "大学", "学院", "专家", "中国", "协会", "所长", "校长"} {
					if strings.Contains(person[2], word) {
						professional = true
						break
					}
				}
				if !professional {
					continue
				}
			}
			if !seen[name] {
				authors = append(authors, name)
				seen[name] = true
			}
			if len(authors) == 8 {
				break
			}
		}
		if LectureAuthorsMatch(authors, author) {
			return importmeta.Metadata{Title: title, Authors: authors}
		}
		return importmeta.Metadata{}
	}
	// Only a verified speaker at the beginning of the profile is a filename hint.
	first := strings.TrimSpace(strings.SplitN(intro, "\n", 2)[0])
	hadPrefix := false
	for _, prefix := range []string{"中国科学院院士，", "中国工程院院士，"} {
		if strings.HasPrefix(first, prefix) {
			first = strings.TrimSpace(strings.TrimPrefix(first, prefix))
			hadPrefix = true
		}
	}
	for i, r := range author {
		if i > 0 {
			first = strings.TrimLeftFunc(first, unicode.IsSpace)
		}
		got, n := utf8.DecodeRuneInString(first)
		if got != r {
			return importmeta.Metadata{}
		}
		first = first[n:]
	}
	suffix := first
	professional := strings.HasPrefix(suffix, "教授") || strings.HasPrefix(suffix, "院士") || (hadPrefix && strings.HasPrefix(suffix, "先生"))
	if !professional && suffix != "" && !strings.ContainsRune("，,。:：;； \t\r\n", []rune(suffix)[0]) {
		return importmeta.Metadata{}
	}
	return importmeta.Metadata{Title: title, Authors: []string{author}}
}
