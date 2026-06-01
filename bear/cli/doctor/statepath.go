package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/barad1tos/noxctl/bear/config"
)

// ResolveStatePath picks the state.json doctor stats, matching the
// daemon's effective resolution so the two commands inspect the SAME
// file. Precedence (highest to lowest): cliFlag (the --state-path flag)
// → REGEN_STATE_PATH env (config.EnvStatePath) → $HOME/.noxctl/state.json
// default. A hardcoded project-relative literal would make doctor stat a
// file the daemon never writes, reporting a false "first run" on a vault
// applied for weeks.
func ResolveStatePath(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	if env := os.Getenv(config.EnvStatePath); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("doctor.ResolveStatePath: UserHomeDir: %w", err)
	}
	return filepath.Join(home, ".noxctl", "state.json"), nil
}
