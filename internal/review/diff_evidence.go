package review

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

// Decode diff tool output separately so additional field types cannot change
// the existing content/matches JSON-versus-raw fallback contract.
type diffCorpusFile struct {
	Path   string `json:"path"`
	Patch  string `json:"patch"`
	Found  *bool  `json:"found"`
	Status string `json:"status"`
}
type diffCorpusOutput struct {
	diffCorpusFile
	Files []diffCorpusFile `json:"files"`
}

func (d *diffCorpusOutput) matches(e store.Evidence) bool {
	if d.diffCorpusFile.matches(e) {
		return true
	}
	for _, file := range d.Files {
		if file.matches(e) {
			return true
		}
	}
	return false
}
func (d diffCorpusFile) matches(e store.Evidence) bool {
	if d.Path != e.File || d.Status == "removed" || (d.Found != nil && !*d.Found) || e.Line <= 0 || strings.TrimSpace(e.Text) == "" {
		return false
	}
	return patchEvidence(d.Patch, e)
}

var evidenceHunk = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

// Match only supplied new-side lines within a valid hunk. An incomplete tail
// is allowed (tools truncate by lines); missing lines are never reconstructed.
func patchEvidence(patch string, e store.Evidence) bool {
	target := strings.TrimSpace(e.Text)
	next, oldRemaining, newRemaining := 0, 0, 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			next, oldRemaining, newRemaining = 0, 0, 0
			parts := evidenceHunk.FindStringSubmatch(line)
			if parts == nil {
				continue
			}
			values := [4]int{}
			valid := true
			for i := 0; i < 4; i++ {
				if (i == 1 || i == 3) && parts[i+1] == "" {
					values[i] = 1
					continue
				}
				value, err := strconv.Atoi(parts[i+1])
				if err != nil {
					valid = false
					break
				}
				values[i] = value
			}
			if !valid || (values[0] == 0 && values[1] != 0) || (values[2] == 0 && values[3] != 0) {
				continue
			}
			next, oldRemaining, newRemaining = values[2], values[1], values[3]
			continue
		}
		if line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			next = 0
			continue
		}
		if next <= 0 {
			continue
		}
		switch line[0] {
		case '-':
			if oldRemaining <= 0 {
				next = 0
				continue
			}
			oldRemaining--
		case '+', ' ':
			if newRemaining <= 0 || (line[0] == ' ' && oldRemaining <= 0) {
				next = 0
				continue
			}
			if line[0] == ' ' {
				oldRemaining--
			}
			newRemaining--
			if next == e.Line && strings.TrimSpace(line[1:]) == target {
				return true
			}
			next++
		default:
			next = 0
		}
	}
	return false
}
