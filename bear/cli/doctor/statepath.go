package doctor

import (
	"os"

	"github.com/barad1tos/noxctl/bear/config"
)

// ResolveStatePath picks the state.json doctor stats. Precedence
// (highest to lowest): cliFlag (the --state-path flag) →
// REGEN_STATE_PATH env (config.EnvStatePath) → project-local apply
// state. This lets doctor report the same state file the ordinary
// `noxctl apply` path writes without hiding a fresh project behind an
// unrelated daemon/home state.
func ResolveStatePath(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	if env := os.Getenv(config.EnvStatePath); env != "" {
		return env, nil
	}
	return defaultStatePath, nil
}
