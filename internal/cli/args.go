package cli

import (
	"fmt"
	"strings"
)

type arguments struct {
	positionals []string
	json        bool
	help        bool
	actor       string
	actorSet    bool
	flags       map[string]string
	flagOrder   []string
}

func parseArguments(argv []string) (*arguments, error) {
	inv := &arguments{flags: make(map[string]string)}
	seen := make(map[string]bool)
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			inv.positionals = append(inv.positionals, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			inv.positionals = append(inv.positionals, arg)
			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--json":
			inv.json = true
			if hasValue {
				fail(fmt.Errorf("flag --json does not take a value"))
				continue
			}
			if seen[name] {
				fail(fmt.Errorf("duplicate flag --json"))
				continue
			}
			seen[name] = true
		case "--help":
			inv.help = true
			if hasValue {
				fail(fmt.Errorf("flag --help does not take a value"))
				continue
			}
			if seen[name] {
				fail(fmt.Errorf("duplicate flag --help"))
				continue
			}
			seen[name] = true
		case "--actor":
			if seen[name] {
				fail(fmt.Errorf("duplicate flag --actor"))
				continue
			}
			seen[name] = true
			actor, ok := takeValue(argv, &i, value, hasValue)
			if !ok {
				fail(fmt.Errorf("flag --actor requires a value"))
				continue
			}
			if strings.TrimSpace(actor) == "" {
				fail(fmt.Errorf("flag --actor requires a non-empty value"))
				continue
			}
			inv.actor = actor
			inv.actorSet = true
		default:
			flag := strings.TrimPrefix(name, "--")
			if !knownCommandFlags[flag] {
				fail(fmt.Errorf("unknown flag %s", name))
				continue
			}
			if seen[name] {
				fail(fmt.Errorf("duplicate flag %s", name))
				continue
			}
			seen[name] = true
			flagValue, ok := takeValue(argv, &i, value, hasValue)
			if !ok {
				fail(fmt.Errorf("flag %s requires a value", name))
				continue
			}
			inv.flags[flag] = flagValue
			inv.flagOrder = append(inv.flagOrder, flag)
		}
	}

	if firstErr != nil {
		return inv, firstErr
	}
	return inv, nil
}

func takeValue(argv []string, index *int, inline string, hasInline bool) (string, bool) {
	if hasInline {
		return inline, true
	}
	if *index+1 >= len(argv) {
		return "", false
	}
	next := argv[*index+1]
	if next == "--" || strings.HasPrefix(next, "--") {
		return "", false
	}
	*index = *index + 1
	return next, true
}
