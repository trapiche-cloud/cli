package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	deployTypeAuto   = "auto"
	deployTypeStatic = "static"
	deployTypeSSR    = "ssr"

	// containerPort is the port that Trapiche sends traffic to inside a container.
	containerPort = 3000
)

// serverDependencies are packages that mean the project is a server app.
// The API has the final say. This list only gives a first hint.
var serverDependencies = []string{
	"express",
	"fastify",
	"koa",
	"@nestjs/core",
	"@hono/node-server",
	"elysia",
	"@remix-run/react",
	"@sveltejs/kit",
	"nuxt",
	"@solidjs/start",
	"@tanstack/start",
	"@angular/ssr",
	"@qwik.dev/city",
}

// normalizeDeployType checks the --type flag value. An empty value means auto.
func normalizeDeployType(raw string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "", deployTypeAuto:
		return deployTypeAuto, nil
	case deployTypeStatic, deployTypeSSR:
		return v, nil
	case "container":
		return deployTypeSSR, nil
	default:
		return "", fmt.Errorf("invalid --type %q: use static, ssr, or auto", raw)
	}
}

// deployTypeLabel gives the name that users see.
func deployTypeLabel(t string) string {
	switch t {
	case deployTypeSSR:
		return "container"
	case deployTypeStatic:
		return "static site"
	default:
		return "auto"
	}
}

// detectDeployType looks at the project folder and returns a type and the reason.
// The type is "" when the folder does not give a clear answer. The caller then
// sends "auto" and the API decides.
func detectDeployType(dir string) (string, string) {
	if info, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil && !info.IsDir() {
		return deployTypeSSR, "Dockerfile found"
	}

	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, "index.html")); statErr == nil {
			return deployTypeStatic, "index.html found, no package.json"
		}
		return "", ""
	}

	var pkg struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", ""
	}
	hasDep := func(name string) bool {
		if _, ok := pkg.Dependencies[name]; ok {
			return true
		}
		_, ok := pkg.DevDependencies[name]
		return ok
	}
	for _, name := range serverDependencies {
		if hasDep(name) {
			return deployTypeSSR, name + " in package.json"
		}
	}
	// Next.js is static only with output: 'export'. This is the same rule as the API.
	if hasDep("next") {
		if nextHasStaticExport(dir) {
			return deployTypeStatic, "Next.js with static export"
		}
		return deployTypeSSR, "Next.js server"
	}
	return "", ""
}

// nextConfigNames are the file names that Next.js loads, in load order.
// Next.js does not load next.config.cjs or next.config.cts.
var nextConfigNames = []string{
	"next.config.js", "next.config.mjs", "next.config.ts", "next.config.mts",
}

// nextHasStaticExport reports whether the next.config file that Next.js loads
// sets output: 'export'. Next.js loads one file: the first that exists in
// nextConfigNames order. A second, unused file does not count. Comments and
// text inside strings do not count either. The type is saved in trapiche.json
// and cannot change, so a wrong guess is costly.
func nextHasStaticExport(dir string) bool {
	for _, name := range nextConfigNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false
		}
		return hasOutputExport(stripJSComments(string(data)))
	}
	return false
}

type jsTokenKind int

const (
	tokIdent jsTokenKind = iota
	tokString
	tokPunct
)

type jsToken struct {
	kind jsTokenKind
	text string
}

func isJSIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isJSIdentPart(c byte) bool {
	return isJSIdentStart(c) || (c >= '0' && c <= '9')
}

// tokenizeJS splits JavaScript or TypeScript source into identifiers, strings,
// and single punctuation characters. A string is one token, so text inside a
// string never looks like code. It is not a full parser.
func tokenizeJS(src string) []jsToken {
	var tokens []jsToken
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'' || c == '"' || c == '`':
			var sb strings.Builder
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				sb.WriteByte(src[j])
				j++
			}
			tokens = append(tokens, jsToken{tokString, sb.String()})
			i = j + 1
		case isJSIdentStart(c):
			j := i + 1
			for j < len(src) && isJSIdentPart(src[j]) {
				j++
			}
			tokens = append(tokens, jsToken{tokIdent, src[i:j]})
			i = j
		default:
			tokens = append(tokens, jsToken{tokPunct, string(c)})
			i++
		}
	}
	return tokens
}

// hasOutputExport looks for the token sequence: output : 'export'
// (the key may be a name or a string, the value must be the string export).
// It counts only an unconditional property. An object that follows "?", ":",
// "&" or "|" (a ternary branch, or "cond && {...}") may not be the config that
// Next.js runs, and static uploads are built with no environment variables. So
// the CLI does not call such a config static. A wrapper call, as in
// withPlugin({ output: 'export' }), still counts.
func hasOutputExport(src string) bool {
	tokens := tokenizeJS(src)
	// One entry for each open "{", "(" or "[". The value is true for a "{" that
	// starts a conditional object.
	var open []bool
	conditionalDepth := 0
	for i, tok := range tokens {
		if tok.kind == tokPunct {
			switch tok.text {
			case "{", "(", "[":
				conditional := false
				if tok.text == "{" && i > 0 && tokens[i-1].kind == tokPunct {
					switch tokens[i-1].text {
					case "?", ":", "&", "|":
						conditional = true
					}
				}
				open = append(open, conditional)
				if conditional {
					conditionalDepth++
				}
			case "}", ")", "]":
				if n := len(open); n > 0 {
					if open[n-1] {
						conditionalDepth--
					}
					open = open[:n-1]
				}
			}
			continue
		}
		if i+2 >= len(tokens) || tok.text != "output" || conditionalDepth > 0 {
			continue
		}
		if tokens[i+1].kind != tokPunct || tokens[i+1].text != ":" {
			continue
		}
		if val := tokens[i+2]; val.kind == tokString && val.text == "export" {
			return true
		}
	}
	return false
}

// stripJSComments removes // line comments and /* block comments */ from
// JavaScript or TypeScript source. It keeps text inside string and template
// literals, so "https://example.com" stays whole. It is not a full parser.
func stripJSComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	var quote byte // the quote character of the string that we are in, or 0
	for i := 0; i < len(src); i++ {
		c := src[i]

		if quote != 0 {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == quote {
				quote = 0
			}
			continue
		}

		switch {
		case c == '\'' || c == '"' || c == '`':
			quote = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out.WriteByte('\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip the "/" of the closing "*/"
			out.WriteByte(' ')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// containerPortHint reminds the user about the container port when the project
// has no Dockerfile. The Dockerfile case has its own, more exact warning.
func containerPortHint(dir string) string {
	ports, found := dockerfileExposedPorts(dir)
	switch {
	case !found:
		return fmt.Sprintf("a container app must listen on port %d (Trapiche sends traffic to that port).", containerPort)
	case len(ports) == 0:
		// No EXPOSE, or only names such as EXPOSE $PORT. The CLI cannot tell the port.
		return fmt.Sprintf("the Dockerfile has no numeric EXPOSE. Make your app listen on port %d (Trapiche sends traffic to that port).", containerPort)
	default:
		// Numeric ports: dockerfilePortWarning gives the exact answer.
		return ""
	}
}

// resolveDeployType decides which type to send to the API.
//
//	requested: the --type value (auto, static, or ssr)
//	detected:  the result of detectDeployType ("" if not clear)
//	saved:     the type in trapiche.json ("" if not known)
//	isUpdate:  true when the deploy updates an existing deployment
//
// The type of an existing deployment cannot change. A different --type is an error.
// When trapiche.json has no saved type (an older file), the CLI sends no type
// unless --type is set. The server keeps the type that the deployment has.
func resolveDeployType(requested, detected, saved string, isUpdate bool) (string, error) {
	if isUpdate && saved != "" {
		if requested != deployTypeAuto && requested != saved {
			return "", fmt.Errorf("this deployment is a %s and cannot change to a %s — use --new to create a new deployment",
				deployTypeLabel(saved), deployTypeLabel(requested))
		}
		return saved, nil
	}
	if isUpdate {
		if requested != deployTypeAuto {
			return requested, nil
		}
		return "", nil
	}
	if requested != deployTypeAuto {
		return requested, nil
	}
	if detected != "" {
		return detected, nil
	}
	return deployTypeAuto, nil
}

// describeDeployType makes the "Type:" line that deploy prints.
func describeDeployType(chosen, requested, detected, reason string, saved bool) string {
	label := deployTypeLabel(chosen)
	switch {
	case chosen == "":
		return "same as the linked deployment"
	case saved:
		return fmt.Sprintf("%s (same as the linked deployment)", label)
	case requested != deployTypeAuto:
		return fmt.Sprintf("%s (--type %s)", label, requested)
	case detected != "":
		return fmt.Sprintf("%s (%s)", label, reason)
	default:
		return "auto (the server decides)"
	}
}

// dockerfileExposedPorts returns the numeric ports of the EXPOSE lines of the
// final stage of the Dockerfile in dir, and whether a Dockerfile exists. Names
// such as $PORT are skipped. The final image is what runs, so an earlier build
// stage does not count. A stage that starts FROM an earlier stage keeps the
// ports of that stage, as Docker does.
func dockerfileExposedPorts(dir string) (ports []int, found bool) {
	file, err := os.Open(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		return nil, false
	}
	defer file.Close()

	stages := map[string][]int{} // stage name (lower case) -> ports at its end
	var stageName string
	finishStage := func() {
		if stageName != "" {
			stages[stageName] = ports
		}
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			finishStage()
			// FROM [--platform=x] image [AS name]
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) == 0 {
				continue
			}
			ports = append([]int(nil), stages[strings.ToLower(args[0])]...)
			stageName = ""
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				stageName = strings.ToLower(args[2])
			}
		case "EXPOSE":
			for _, f := range fields[1:] {
				port, _, _ := strings.Cut(f, "/")
				if n, err := strconv.Atoi(port); err == nil {
					ports = append(ports, n)
				}
			}
		}
	}
	return ports, true
}

// dockerfilePortWarning returns a warning when the Dockerfile exposes ports
// and none of them is the container port. It returns "" in all other cases.
func dockerfilePortWarning(dir string) string {
	exposed, _ := dockerfileExposedPorts(dir)
	if len(exposed) == 0 {
		return ""
	}
	for _, p := range exposed {
		if p == containerPort {
			return ""
		}
	}
	return fmt.Sprintf("Dockerfile exposes port %d, but Trapiche sends traffic to port %d. Make your app listen on port %d.",
		exposed[0], containerPort, containerPort)
}
