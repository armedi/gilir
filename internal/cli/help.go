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
}

var flagPlaceholders = map[string]string{
	"project":     "id",
	"repo":        "id",
	"status":      "status",
	"priority":    "priority",
	"marking":     "marking",
	"description": "markdown",
	"title":       "title",
}

var groupSummaries = map[string]string{
	"project": "Manage projects.",
	"repo":    "Manage repositories in a project.",
	"daemon":  "Run and control the background daemon.",
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
			!strings.HasPrefix(spec.path, "daemon ") &&
			spec.path != "skill"
	})
	builder.WriteString("\nDaemon commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return strings.HasPrefix(spec.path, "daemon ")
	})
	builder.WriteString("\nLocal commands:\n")
	writeCommandUsages(&builder, func(spec command) bool {
		return spec.path == "skill"
	})

	builder.WriteString("\nGlobal options (accepted anywhere before --):\n")
	builder.WriteString("  --actor <actor>  Audit identity (default: GILIR_ACTOR or \"user\")\n")
	builder.WriteString("  --json           Machine-readable JSON output\n")
	builder.WriteString("  --help           Show help\n")
	builder.WriteString("\nIDs are positive integers. Use -- to pass values that begin with --.\n")
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
		fmt.Fprintf(&builder, "  %-26s  %s\n", usage, spec.summary)
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
