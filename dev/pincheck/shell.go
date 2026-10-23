package main

import (
	"path"
	"strings"
)

// shellScan is the best-effort scan for images that shell text pulls:
// `docker run`, `docker create`, `docker pull` (and the `docker container
// run|create`, `docker image pull` spellings) in workflow `run:` scripts and
// Makefiles. The first non-flag argument after the subcommand is the image.
// An image the same file builds (`docker build`/`docker buildx build` with
// `-t`/`--tag`) is local and exempt.
//
// It is a heuristic, not a shell: a flag it does not know to be boolean is
// assumed to take a value, so an unknown boolean flag makes it read the
// command as the image — a refusal, never a pass. What it cannot see is
// listed in KNOWN-LIMITATIONS.md (images in other scripts, images passed
// through a variable defined elsewhere are refused as computed, and commands
// assembled at run time).
type shellScan struct {
	file, origin string
	calls        []imageRef
	built        map[string]bool
	problems     []problem
}

func newShellScan(file, origin string) *shellScan {
	return &shellScan{file: file, origin: origin, built: map[string]bool{}}
}

// scanShellImages runs the shell scan over one whole file (a Makefile).
func (s *scanned) scanShellImages(file string, firstLine int, text, origin string) {
	sh := newShellScan(file, origin)
	sh.scan(firstLine, text)
	s.images = append(s.images, sh.images()...)
	s.problems = append(s.problems, sh.problems...)
}

// images returns the pulled images, without the ones this file builds.
func (sh *shellScan) images() []imageRef {
	var out []imageRef
	for _, c := range sh.calls {
		if !sh.built[c.ref] {
			out = append(out, c)
		}
	}
	return out
}

// token is one shell word and the line it is on.
type token struct {
	text string
	line int
}

// scan tokenizes text (whose first line is firstLine) into commands and
// records the docker invocations in each.
func (sh *shellScan) scan(firstLine int, text string) {
	for _, cmd := range shellCommands(firstLine, text) {
		sh.command(cmd)
	}
}

// shellCommands splits shell text into commands of words. A newline ends a
// command unless the line ends with a backslash; `;`, `|`, `||`, `&&` and
// `&` end one too. Quotes group words and are dropped. A `#` at the start of
// a word starts a comment to the end of the line.
func shellCommands(firstLine int, text string) [][]token {
	var cmds [][]token
	var cur []token
	var word strings.Builder
	inWord := false
	var quote byte
	wordLine := 0
	flushWord := func() {
		if inWord {
			cur = append(cur, token{text: word.String(), line: wordLine})
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		flushWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
			cur = nil
		}
	}
	for idx, raw := range strings.Split(text, "\n") {
		line := firstLine + idx
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimRight(raw, " \t")
		continued := quote == 0 && strings.HasSuffix(trimmed, `\`)
		if continued {
			raw = strings.TrimSuffix(trimmed, `\`)
		}
		for i := 0; i < len(raw); i++ {
			c := raw[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				} else {
					word.WriteByte(c)
				}
				continue
			}
			switch {
			case c == '\'' || c == '"':
				if !inWord {
					inWord, wordLine = true, line
				}
				quote = c
			case c == ' ' || c == '\t':
				flushWord()
			case c == '#' && !inWord:
				i = len(raw) // comment to end of line
			case c == ';' || c == '|' || c == '&':
				if i+1 < len(raw) && (raw[i+1] == '|' || raw[i+1] == '&') && c != ';' {
					i++
				}
				endCmd()
			default:
				if !inWord {
					inWord, wordLine = true, line
				}
				word.WriteByte(c)
			}
		}
		if quote == 0 && !continued {
			endCmd()
		} else {
			flushWord()
		}
	}
	endCmd()
	return cmds
}

// Boolean flags: every other flag spelled without `=` takes the next word.
var (
	runBoolFlags = map[string]bool{
		"--rm": true, "-d": true, "--detach": true, "-i": true, "--interactive": true,
		"-t": true, "--tty": true, "--init": true, "--privileged": true, "--read-only": true,
		"-P": true, "--publish-all": true, "--no-healthcheck": true, "--oom-kill-disable": true,
		"-q": true, "--quiet": true, "--disable-content-trust": true, "--use-api-socket": true,
		"--help": true, "--sig-proxy": true,
	}
	runBoolShort  = "dtiPq" // letters that may be combined, as in -it or -dit
	pullBoolFlags = map[string]bool{
		"-a": true, "--all-tags": true, "-q": true, "--quiet": true,
		"--disable-content-trust": true, "--help": true,
	}
	pullBoolShort = "aq"
	// globalValueFlags are docker's own flags (before the subcommand) that
	// take a value.
	globalValueFlags = map[string]bool{
		"--config": true, "-c": true, "--context": true, "-H": true, "--host": true,
		"-l": true, "--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true,
	}
)

// command records the docker invocations in one command's words.
func (sh *shellScan) command(toks []token) {
	for i := 0; i < len(toks); i++ {
		if path.Base(toks[i].text) != "docker" {
			continue
		}
		rest := toks[i+1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0].text, "-") {
			if !strings.Contains(rest[0].text, "=") && globalValueFlags[rest[0].text] && len(rest) > 1 {
				rest = rest[1:]
			}
			rest = rest[1:]
		}
		if len(rest) == 0 {
			continue
		}
		verb, args := rest[0].text, rest[1:]
		if (verb == "container" || verb == "image" || verb == "buildx") && len(args) > 0 {
			verb, args = verb+" "+args[0].text, args[1:]
		}
		switch verb {
		case "build", "image build", "buildx build":
			sh.buildTags(args)
		case "run", "create", "container run", "container create":
			sh.imageArg(args, runBoolFlags, runBoolShort)
		case "pull", "image pull":
			sh.imageArg(args, pullBoolFlags, pullBoolShort)
		}
	}
}

func (sh *shellScan) buildTags(args []token) {
	for i := 0; i < len(args); i++ {
		a := args[i].text
		switch {
		case a == "-t" || a == "--tag":
			if i+1 < len(args) {
				sh.built[args[i+1].text] = true
				i++
			}
		case strings.HasPrefix(a, "--tag="):
			sh.built[strings.TrimPrefix(a, "--tag=")] = true
		case strings.HasPrefix(a, "-t") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			sh.built[strings.TrimPrefix(a, "-t")] = true
		}
	}
}

// imageArg records the first non-flag word: the image.
func (sh *shellScan) imageArg(args []token, bools map[string]bool, short string) {
	for i := 0; i < len(args); i++ {
		a := args[i].text
		if !strings.HasPrefix(a, "-") || a == "-" {
			sh.calls = append(sh.calls, imageRef{file: sh.file, line: args[i].line, ref: a, origin: sh.origin})
			return
		}
		if strings.Contains(a, "=") || bools[a] || isShortBools(a, short) {
			continue
		}
		i++ // the flag's value
	}
}

// isShortBools reports whether a is a run of combinable boolean short
// flags, as in -it.
func isShortBools(a, short string) bool {
	if len(a) < 3 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for _, c := range a[1:] {
		if !strings.ContainsRune(short, c) {
			return false
		}
	}
	return true
}
