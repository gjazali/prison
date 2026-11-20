package ecosystem

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxInspectBytes = 1 << 20

type Detection struct {
	Matched          []*Ecosystem
	Shadow           []string
	SetupCommands    []string
	CheckpointIgnore []string
	EgressHosts      []string
	EgressDocs       []string
}

func (d *Detection) Empty() bool {
	return len(d.Shadow) == 0 && len(d.SetupCommands) == 0 &&
		len(d.CheckpointIgnore) == 0 && len(d.EgressHosts) == 0 &&
		len(d.EgressDocs) == 0
}

func Detect(projectDir string, catalog *Catalog) (*Detection, error) {
	probe := &projectProbe{dir: projectDir}
	var candidates []*Ecosystem
	for _, eco := range catalog.Ecosystems {
		if probe.matches(eco.Detect) {
			candidates = append(candidates, eco)
		}
	}

	bestByGroup := map[string]*Ecosystem{}
	var ungrouped []*Ecosystem
	for _, eco := range candidates {
		if eco.Group == "" {
			ungrouped = append(ungrouped, eco)
			continue
		}
		current := bestByGroup[eco.Group]
		if current == nil || eco.Priority > current.Priority ||
			(eco.Priority == current.Priority && eco.Name < current.Name) {
			bestByGroup[eco.Group] = eco
		}
	}

	matched := make([]*Ecosystem, 0, len(bestByGroup)+len(ungrouped))
	for _, eco := range bestByGroup {
		matched = append(matched, eco)
	}
	matched = append(matched, ungrouped...)
	sort.Slice(matched, func(i, j int) bool {
		if (matched[i].Group != "") != (matched[j].Group != "") {
			return matched[i].Group != ""
		}
		return matched[i].Name < matched[j].Name
	})

	result := &Detection{Matched: matched}
	seenShadow := map[string]bool{}
	seenSetup := map[string]bool{}
	seenIgnore := map[string]bool{}
	seenHosts := map[string]bool{}
	seenDocs := map[string]bool{}

	for _, eco := range matched {
		for _, shadow := range eco.Box.Shadow {
			if !seenShadow[shadow] {
				seenShadow[shadow] = true
				result.Shadow = append(result.Shadow, shadow)
			}
		}
		for _, command := range eco.Setup.Commands {
			if !seenSetup[command] {
				seenSetup[command] = true
				result.SetupCommands = append(result.SetupCommands, command)
			}
		}
		for _, host := range eco.Egress.Hosts {
			if !seenHosts[host] {
				seenHosts[host] = true
				result.EgressHosts = append(result.EgressHosts, host)
			}
		}
	}
	for _, eco := range matched {
		for _, host := range eco.Egress.Docs {
			if seenHosts[host] || seenDocs[host] {
				continue
			}
			seenDocs[host] = true
			result.EgressDocs = append(result.EgressDocs, host)
		}
	}
	for _, eco := range matched {
		for _, pattern := range eco.Checkpoint.Ignore {
			trimmed := strings.TrimSuffix(pattern, "/")
			// Shadowed directories are automatically ignored by checkpoints.
			if seenShadow[trimmed] {
				continue
			}
			if !seenIgnore[pattern] {
				seenIgnore[pattern] = true
				result.CheckpointIgnore = append(result.CheckpointIgnore, pattern)
			}
		}
	}
	return result, nil
}

type projectProbe struct {
	dir        string
	fileExists map[string]bool
	fileText   map[string]cachedFile
}

type cachedFile struct {
	present bool
	text    string
}

func (p *projectProbe) matches(rule DetectTable) bool {
	if len(rule.AllFiles) > 0 {
		for _, pattern := range rule.AllFiles {
			if !p.hasFile(pattern) {
				return false
			}
		}
		if len(rule.Files) == 0 && len(rule.Match) == 0 {
			return true
		}
	}
	for _, pattern := range rule.Files {
		if p.hasFile(pattern) {
			return true
		}
	}
	for _, matcher := range rule.Match {
		text, ok := p.readFile(matcher.File)
		if !ok {
			continue
		}
		expression, err := regexp.Compile(matcher.Pattern)
		if err != nil {
			continue
		}
		if expression.MatchString(text) {
			return true
		}
	}
	return false
}

func (p *projectProbe) hasFile(pattern string) bool {
	if p.fileExists == nil {
		p.fileExists = map[string]bool{}
	}
	if cached, ok := p.fileExists[pattern]; ok {
		return cached
	}
	found := false
	if strings.ContainsAny(pattern, "*?[") {
		matches, err := filepath.Glob(filepath.Join(p.dir, filepath.FromSlash(pattern)))
		found = err == nil && len(matches) > 0
	} else {
		_, err := os.Stat(filepath.Join(p.dir, filepath.FromSlash(pattern)))
		found = err == nil
	}
	p.fileExists[pattern] = found
	return found
}

func (p *projectProbe) readFile(relative string) (string, bool) {
	if p.fileText == nil {
		p.fileText = map[string]cachedFile{}
	}
	if cached, ok := p.fileText[relative]; ok {
		return cached.text, cached.present
	}
	file, err := os.Open(filepath.Join(p.dir, filepath.FromSlash(relative)))
	if err != nil {
		p.fileText[relative] = cachedFile{}
		return "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInspectBytes))
	if err != nil {
		p.fileText[relative] = cachedFile{}
		return "", false
	}
	cached := cachedFile{present: true, text: string(data)}
	p.fileText[relative] = cached
	return cached.text, true
}
