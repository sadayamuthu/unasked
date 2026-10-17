package model

import (
	"regexp"
	"strings"
)

var ciPathRes = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)\.github/workflows/`),
	regexp.MustCompile(`(^|/)\.circleci/`),
	regexp.MustCompile(`(^|/)(\.gitlab-ci\.yml|Jenkinsfile|azure-pipelines\.yml)$`),
}

// IsCIPath reports whether p (either slash style) is a CI configuration file:
// a GitHub workflow, CircleCI config, .gitlab-ci.yml, Jenkinsfile or
// azure-pipelines.yml. Such files are labelled test (label package), and a
// prompt asks for them with CI words rather than test words (invite).
func IsCIPath(p string) bool {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, re := range ciPathRes {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}
