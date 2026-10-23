package main

import (
	"regexp"
	"strings"
)

// parserDirective is a `# key=value` line at the very top of a Dockerfile.
// BuildKit stops looking for directives at the first line that is not one
// (an instruction, a comment or a blank line), and so does this scanner.
var parserDirective = regexp.MustCompile(`^#\s*([A-Za-z][A-Za-z0-9_-]*)\s*=\s*(\S+)\s*$`)

// scanDockerfile records every image a build of this Dockerfile fetches:
//
//   - the `# syntax=` parser directive (the BuildKit frontend is an image that
//     runs with the build's inputs);
//   - FROM, unless it names an earlier stage or `scratch`;
//   - COPY/ADD --from=, unless it names an earlier stage or a stage index;
//   - RUN --mount=...,from=<ref>, with the same exceptions;
//
// and the same under ONBUILD. Instructions are read as the builder reads
// them: continuation lines (the escape character at the end of a line,
// `\` or the one `# escape=` sets) are joined, and comment lines inside a
// continuation are dropped.
func (s *scanned) scanDockerfile(file string, content []byte) {
	lines := strings.Split(string(content), "\n")
	escape := `\`
	i := 0
	for ; i < len(lines); i++ {
		m := parserDirective.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if m == nil {
			break
		}
		switch strings.ToLower(m[1]) {
		case "syntax":
			s.addImage(file, i+1, m[2], originDockerfile)
		case "escape":
			if m[2] == "`" {
				escape = "`"
			}
		}
	}

	stages := map[string]bool{}
	var buf strings.Builder
	start := 0
	for j := i; j < len(lines); j++ {
		trimmed := strings.TrimSpace(strings.TrimRight(lines[j], "\r"))
		if buf.Len() == 0 {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			start = j + 1
		} else if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, escape) {
			buf.WriteString(strings.TrimSuffix(trimmed, escape))
			buf.WriteByte(' ')
			continue
		}
		buf.WriteString(trimmed)
		s.dockerInstruction(file, start, buf.String(), stages)
		buf.Reset()
	}
	if buf.Len() > 0 {
		s.dockerInstruction(file, start, buf.String(), stages)
	}
}

// isStageRef reports whether a --from value names an earlier build stage
// (case-insensitively, as the builder matches them) or a stage index.
func isStageRef(ref string, stages map[string]bool) bool {
	return stages[strings.ToLower(ref)] || isAllDigits(ref)
}

func (s *scanned) dockerInstruction(file string, line int, text string, stages map[string]bool) {
	fields := strings.Fields(text)
	if len(fields) > 1 && strings.EqualFold(fields[0], "ONBUILD") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return
	}
	switch strings.ToUpper(fields[0]) {
	case "FROM":
		rest := fields[1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			s.refuse(file, line, "FROM without an image")
			return
		}
		image := rest[0]
		// The stage this line declares is usable from the next line on, so
		// it is checked against the earlier stages first.
		if !strings.EqualFold(image, "scratch") && !stages[strings.ToLower(image)] {
			s.addImage(file, line, image, originDockerfile)
		}
		if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
			stages[strings.ToLower(rest[2])] = true
		}
	case "COPY", "ADD":
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "--") {
				break
			}
			if from, ok := strings.CutPrefix(f, "--from="); ok && !isStageRef(from, stages) {
				s.addImage(file, line, from, originDockerfile)
			}
		}
	case "RUN":
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "--") {
				break
			}
			spec, ok := strings.CutPrefix(f, "--mount=")
			if !ok {
				continue
			}
			for _, kv := range strings.Split(spec, ",") {
				from, ok := strings.CutPrefix(strings.Trim(kv, `"'`), "from=")
				from = strings.Trim(from, `"'`)
				if ok && !isStageRef(from, stages) {
					s.addImage(file, line, from, originDockerfile)
				}
			}
		}
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
