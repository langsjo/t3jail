package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/paths"
)

const desktopBinary = "t3code-desktop"

func newGUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gui",
		Short: "Open the T3 Code desktop app, connected only to the VM",
		Long: `Open the T3 Code desktop app with a profile of its own, in which the app's
local environment (which would run agents directly on this machine) is off.

The app is, in order of preference: [gui] command in config.toml,
t3code-desktop on PATH, or t3code fetched from nixpkgs after asking.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			cfg, err := config.Load(inst.ConfigFile())
			if err != nil {
				return err
			}
			argv, err := desktopCommand(inst, cfg.GUI.Command, askOnTerminal, fetchDesktopApp)
			if err != nil {
				return err
			}
			profile := guiProfile{dir: inst.GUIProfile()}
			if err := profile.prepare(); err != nil {
				return err
			}

			if !profile.paired() {
				url, err := pairingURL(cmd.Context(), inst)
				if err != nil {
					fmt.Printf("Could not create a pairing link (%v).\nOnce the VM runs, get one with `t3jail pair`.\n", err)
				} else {
					fmt.Printf("To connect the app to the VM, open Settings > Connections > Add environment\nand paste this one-time link as the host:\n\n  %s\n\n", url)
				}
			}
			return launchDetached(argv, profile.env(), filepath.Join(inst.Logs(), "gui.log"))
		},
	}
}

// desktopCommand picks the app: the configured command, t3code-desktop on
// PATH, or t3code from the nixpkgs flake registry entry, fetched on request
// and kept alive by a GC root in the state directory.
func desktopCommand(inst paths.Instance, configured []string, ask func(string) (string, error), fetch func(root string) (string, error)) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	if p, err := exec.LookPath(desktopBinary); err == nil {
		return []string{p}, nil
	}

	root := filepath.Join(inst.GCRoots(), "t3code")
	previous, err := os.Readlink(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if previous == "" {
		answer, err := ask(fmt.Sprintf("%s is not on PATH and [gui] command is not set.\nFetch t3code from nixpkgs (a download of several GiB)? [y/N] ", desktopBinary))
		if err != nil {
			return nil, err
		}
		if !isYes(answer) {
			return nil, fmt.Errorf("no T3 desktop app: install %s or set [gui] command in %s", desktopBinary, inst.ConfigFile())
		}
	} else {
		answer, err := ask(fmt.Sprintf("Use the t3code fetched before (%s), or fetch nixpkgs' current one? [U/f] ", previous))
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "f") {
			return []string{filepath.Join(root, "bin", desktopBinary)}, nil
		}
	}

	if err := os.MkdirAll(inst.GCRoots(), 0o700); err != nil {
		return nil, err
	}
	// Building to the same out-link replaces the old GC root, so the
	// previous version becomes garbage once nothing else uses it.
	current, err := fetch(root)
	if err != nil {
		return nil, err
	}
	switch previous {
	case "":
		fmt.Printf("Fetched %s\n", current)
	case current:
		fmt.Println("Already the newest t3code in nixpkgs.")
	default:
		fmt.Printf("Updated from %s\nto %s\n", previous, current)
	}
	return []string{filepath.Join(root, "bin", desktopBinary)}, nil
}

func isYes(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

func askOnTerminal(question string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("cannot ask without a terminal: %s", strings.TrimSpace(question))
	}
	fmt.Print(question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

func fetchDesktopApp(root string) (string, error) {
	cmd := exec.Command("nix", "build", "nixpkgs#t3code", "--out-link", root, "--print-out-paths")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix build nixpkgs#t3code: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", desktopBinary)); err != nil {
		return "", fmt.Errorf("nixpkgs#t3code has no bin/%s: %w", desktopBinary, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// guiProfile is the desktop app's dedicated configuration. The app takes its
// Electron data from $XDG_CONFIG_HOME/t3code-v2 and its own state from
// $T3CODE_HOME/userdata; it sets the Electron path itself, so
// --user-data-dir would not isolate it.
type guiProfile struct{ dir string }

func (p guiProfile) configHome() string { return filepath.Join(p.dir, "config") }
func (p guiProfile) t3Home() string     { return filepath.Join(p.dir, "t3") }
func (p guiProfile) stateDir() string   { return filepath.Join(p.t3Home(), "userdata") }

func (p guiProfile) env() []string {
	return withEnv(os.Environ(), "XDG_CONFIG_HOME="+p.configHome(), "T3CODE_HOME="+p.t3Home())
}

// prepare turns off the app's local environment, which would run agents
// directly on this machine. It is enforced on every launch because the app
// lets the user switch it back on.
func (p guiProfile) prepare() error {
	if err := os.MkdirAll(p.stateDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(p.configHome(), 0o700); err != nil {
		return err
	}
	path := filepath.Join(p.stateDir(), "desktop-settings.json")
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON (%w); fix or delete it", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if settings["localEnvironmentEnabled"] == false {
		return nil
	}
	settings["localEnvironmentEnabled"] = false
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// paired reports whether the app has saved any environment yet.
func (p guiProfile) paired() bool {
	data, err := os.ReadFile(filepath.Join(p.stateDir(), "saved-environments.json"))
	if err != nil {
		return false
	}
	var registry struct {
		Records []json.RawMessage `json:"records"`
	}
	return json.Unmarshal(data, &registry) == nil && len(registry.Records) > 0
}

// withEnv returns env with the given KEY=value entries set, replacing any
// existing values.
func withEnv(env []string, set ...string) []string {
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		replaced := false
		for _, s := range set {
			if strings.HasPrefix(s, key+"=") {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// launchDetached starts the app in its own session, so closing the terminal
// does not close it, with its output in logPath.
func launchDetached(argv []string, env []string, logPath string) error {
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", argv[0], err)
	}
	fmt.Printf("Started T3 Code (output in %s)\n", logPath)
	return cmd.Process.Release()
}
