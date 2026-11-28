package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

func Run(ctx context.Context, argv []string, stdout, stderr io.Writer, defaultActor string, execute func(context.Context, string, string, map[string]any) (any, error)) int {
	inv, err := parseArguments(argv)
	if err != nil {
		writeFailure(stderr, inv.json, err.Error())
		return 2
	}

	if inv.help {
		text := helpFor(inv.positionals)
		if inv.json {
			if err := writeJSON(stdout, helpOutput{Help: text}); err != nil {
				writeFailure(stderr, true, "encode help: "+err.Error())
				return 1
			}
			return 0
		}
		writeText(stdout, text)
		return 0
	}

	spec, positionals, err := resolveCommand(inv.positionals)
	if err != nil {
		writeFailure(stderr, inv.json, err.Error())
		return 2
	}
	if err := spec.validate(positionals, inv); err != nil {
		writeFailure(stderr, inv.json, err.Error())
		return 2
	}

	if spec.kind == skillCommand {
		if err := writeSkill(stdout, inv.json); err != nil {
			writeFailure(stderr, inv.json, "write skill: "+err.Error())
			return 1
		}
		return 0
	}

	// An unusable default (unset or whitespace) falls back to "user". The
	// HTTP API rejects a blank actor on every command, including reads, so a
	// whitespace GILIR_ACTOR must not turn a working read into a failure.
	actor := strings.TrimSpace(defaultActor)
	if actor == "" {
		actor = "user"
	}
	if inv.actorSet {
		actor = inv.actor
	}

	args, err := spec.build(positionals, inv.flags)
	if err != nil {
		writeFailure(stderr, inv.json, fmt.Sprintf("%s: %s", spec.path, err))
		return 2
	}

	result, err := execute(ctx, spec.wire, actor, args)
	if err != nil {
		writeFailure(stderr, inv.json, err.Error())
		return 1
	}

	if inv.json {
		if err := writeJSON(stdout, result); err != nil {
			writeFailure(stderr, true, "encode result: "+err.Error())
			return 1
		}
		return 0
	}
	if spec.human != nil {
		if err := spec.human(stdout, result); err != nil {
			writeFailure(stderr, false, "format result: "+err.Error())
			return 1
		}
		return 0
	}
	writeHuman(stdout, result)
	return 0
}
