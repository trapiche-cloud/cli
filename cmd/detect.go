package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// nextConfigNames are the file names that Next.js reads for its configuration.
var nextConfigNames = []string{
	"next.config.js", "next.config.mjs", "next.config.cjs",
	"next.config.ts", "next.config.mts", "next.config.cts",
}

// staticExportPattern matches output: 'export' (any quote style, any spacing).
var staticExportPattern = regexp.MustCompile("output\\s*:\\s*['\"`]export['\"`]")

// nextHasStaticExport reports whether a next.config file sets output: 'export'.
// Comments are removed first, so a line like "// output: 'export'" does not count.
// The type is saved in trapiche.json and cannot change, so a wrong guess is costly.
func nextHasStaticExport(dir string) bool {
	for _, name := range nextConfigNames {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if staticExportPattern.MatchString(stripJSComments(string(data))) {
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
	if info, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil && !info.IsDir() {
		return ""
	}
	return fmt.Sprintf("a container app must listen on port %d (Trapiche sends traffic to that port).", containerPort)
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

// dockerfilePortWarning returns a warning when the Dockerfile exposes ports
// and none of them is the container port. It returns "" in all other cases.
func dockerfilePortWarning(dir string) string {
	file, err := os.Open(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		return ""
	}
	defer file.Close()

	var exposed []int
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "EXPOSE") {
			continue
		}
		for _, f := range fields[1:] {
			port, _, _ := strings.Cut(f, "/")
			if n, err := strconv.Atoi(port); err == nil {
				exposed = append(exposed, n)
			}
		}
	}
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
