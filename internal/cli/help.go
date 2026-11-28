package cli

import (
	"fmt"
	"io"
	"strings"
)

var flagDescriptions = map[string]string{
	"project":     "Project ID",
	"repo":        "Repository ID",
	"status":      "Task status",
	"priority":    "Task priority",
	"marking":     "Marking name",
	"description": "Markdown description",
	"title":       "Task title",
	"message":     "Markdown message",
	"host":        "HTTP host to bind (default: 127.0.0.1)",
	"port":        "HTTP port to bind (default: 44547)",
	"db":          "SQLite database path (default: the Gilir data directory)",
}

var flagPlaceholders = map[string]string{
	"project":     "id",
	"repo":        "id",
	"status":      "status",
	"priority":    "priority",
	"marking":     "marking",
	"description": "markdown",
	"title":       "title",
	"message":     "markdown",
	"host":        "host",
	"port":        "port",
	"db":          "path",
}

var groupSummaries = map[string]string{
	"project": "Manage projects.",
	"repo":    "Manage repositories and their project links.",
	"service": "Manage the managed background service and its queue.",
}

func helpFor(positionals []string) string {
	if len(positionals) == 0 {
		return generalHelp()
	}
	first := positionals[0]
	if commandGroupNames[first] {
		if len(positionals) > 1 {
			if spec, ok := commandByPath[first+" "+positionals[1]]; ok {
				return spec.helpText()
			}
		}
		return groupHelp(first)
	}
	if spec, ok := commandByPath[first]; ok {
		return spec.helpText()
	}
	return generalHelp()
}

func generalHelp() string {
	var builder strings.Builder
	builder.WriteString("gilir tracks projects, repositories, and tasks for coding agents.\n\n")
	builder.WriteString("Usage:\n  gilir [--actor <actor>] [--json] <command> [arguments]\n\n")
	builder.WriteString("Ordinary commands use GILIR_URL (default\n")
	builder.WriteString("http://127.0.0.1:44547). `gilir serve` runs the service in the foreground;\n")
	builder.WriteString("`gilir service` manages a detached one and its queue.\n\n")

	builder.WriteString("Project commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return strings.HasPrefix(spec.path, "project ")
	})
	builder.WriteString("\nRepository commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return strings.HasPrefix(spec.path, "repo ")
	})
	builder.WriteString("\nTask commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return !strings.HasPrefix(spec.path, "project ") &&
			!strings.HasPrefix(spec.path, "repo ") &&
			!strings.HasPrefix(spec.path, "service ") &&
			spec.path != "serve" &&
			spec.path != "skill"
	})
	builder.WriteString("\nServer commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return spec.path == "serve"
	})
	builder.WriteString("\nService commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return strings.HasPrefix(spec.path, "service ")
	})
	builder.WriteString("\nLocal commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return spec.path == "skill"
	})

	builder.WriteString("\nGlobal options (accepted anywhere before --):\n")
	builder.WriteString("  --actor <actor>  Audit identity (default: GILIR_ACTOR or \"user\")\n")
	builder.WriteString("  --json           Machine-readable JSON output\n")
	builder.WriteString("  --help           Show help\n")
	builder.WriteString("\nProject, repository, and comment IDs are positive integers. Task IDs\n")
	builder.WriteString("carry the GILIR- prefix, for example GILIR-42. Use -- to pass values that\n")
	builder.WriteString("begin with --.\n")
	return builder.String()
}

func groupHelp(group string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Usage:\n  gilir %s <command> [arguments]\n\n%s\n\nCommands:\n", group, groupSummaries[group])
	for _, spec := range commandSpecs {
		if !strings.HasPrefix(spec.path, group+" ") {
			continue
		}
		usage := strings.TrimPrefix(spec.usage, "gilir "+group+" ")
		fmt.Fprintf(&builder, "  %-32s  %s\n", usage, spec.summary)
	}
	builder.WriteString("\nGlobal options: --actor <actor>, --json, --help\n")
	return builder.String()
}

func (c command) helpText() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Usage:\n  %s\n\n%s\n", c.usage, c.summary)
	if len(c.flags) > 0 {
		builder.WriteString("\nOptions:\n")
		for _, flag := range c.flags {
			label := "--" + flag
			if placeholder := flagPlaceholders[flag]; placeholder != "" {
				label += " <" + placeholder + ">"
			}
			description := flagDescriptions[flag]
			if containsFlag(c.required, flag) {
				description += " (required)"
			}
			fmt.Fprintf(&builder, "  %-27s %s\n", label, description)
		}
	}
	builder.WriteString("\nGlobal options:\n")
	builder.WriteString("  --actor <actor>  Audit identity (default: GILIR_ACTOR or \"user\")\n")
	builder.WriteString("  --json           Machine-readable JSON output\n")
	builder.WriteString("  --help           Show help\n")
	return builder.String()
}

func writeCommandUsages(builder *strings.Builder, include func(command) bool) {
	for _, spec := range commandSpecs {
		if include(spec) {
			fmt.Fprintf(builder, "  %s\n", strings.TrimPrefix(spec.usage, "gilir "))
		}
	}
}

func writeSkill(w io.Writer, jsonMode bool) error {
	if jsonMode {
		return writeJSON(w, skillOutput{Skill: skillMarkdown})
	}
	writeText(w, skillMarkdown)
	return nil
}
