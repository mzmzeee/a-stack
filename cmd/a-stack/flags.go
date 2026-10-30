package main

import (
	"flag"
	"strconv"
	"strings"
)

// splitFlags lets flags appear before or after the command; stdlib flag stops
// at the first positional argument. repeated is --after more than once.
func splitFlags(fs *flag.FlagSet, args []string) (command []string, repeated bool, err error) {
	given := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			command = append(command, a)
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, false, unknownFlag(fs, name)
		}

		// --version takes a release only when one follows, as in
		// `update --version v0.1.3`; alone it prints the version.
		if name == "version" && !hasValue && i+1 < len(args) && releaseTag.MatchString(args[i+1]) {
			i++
			value, hasValue = args[i], true
		}

		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() && !hasValue {
			value = "true"
		} else if !hasValue {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") && fs.Lookup(strings.TrimLeft(strings.SplitN(args[i], "=", 2)[0], "-")) != nil {
				return nil, false, invalid("--" + name + " requires a value")
			}

			value = args[i]
		}

		if given[name] && name != "mention" && name != "ack-from" && name != "after" && name != "assign" && name != "path" {
			return nil, false, invalid("--" + name + " is given more than once")
		}

		repeated = repeated || given[name] && name == "after"
		given[name] = true
		if fs.Set(name, value) != nil {
			message := "invalid value " + strconv.Quote(short(value)) + " for --" + name
			if flagValues[name] == "N" {
				message += "; give a whole number"
			}

			return nil, false, invalid(message)
		}
	}

	return command, repeated, nil
}
