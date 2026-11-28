// Package shell generates shell completion scripts and init snippets for the
// cairnbin binary. Supported shells: bash, zsh, fish, sh, dash, ksh,
// powershell, pwsh.
package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// ErrNoProgrammableCompletion identifies shells that cannot load generated
// completion scripts. The CLI maps this usage error to exit status 2.
var ErrNoProgrammableCompletion = errors.New("no programmable completion")

// NoProgrammableCompletionError identifies the shell without a programmable
// completion interface so callers can print a localized usage error.
type NoProgrammableCompletionError struct {
	Shell string
}

func (e *NoProgrammableCompletionError) Error() string {
	return fmt.Sprintf("%s: %s", e.Shell, ErrNoProgrammableCompletion)
}

func (e *NoProgrammableCompletionError) Unwrap() error {
	return ErrNoProgrammableCompletion
}

// Detect returns the name of the running shell by inspecting $SHELL, the
// parent process, and $0 in that order. Falls back to "bash".
func Detect() string {
	// 1. $SHELL environment variable.
	if sh := os.Getenv("SHELL"); sh != "" {
		base := strings.ToLower(filepath.Base(sh))
		if isKnown(base) {
			return base
		}
	}

	// 2. $0 (process name when run via eval).
	if z := os.Getenv("ZSH_VERSION"); z != "" {
		return "zsh"
	}
	if b := os.Getenv("BASH_VERSION"); b != "" {
		return "bash"
	}
	if f := os.Getenv("FISH_VERSION"); f != "" {
		return "fish"
	}

	return "bash"
}

// Normalize returns the canonical lowercase shell name or an error if the
// shell is unsupported.
func Normalize(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "pwsh":
		return "powershell", nil
	case "bash", "zsh", "fish", "sh", "dash", "ksh", "powershell":
		return n, nil
	default:
		return "", fmt.Errorf("unsupported shell %q — supported: bash, zsh, fish, sh, dash, ksh, powershell, pwsh", name)
	}
}

func isKnown(name string) bool {
	_, err := Normalize(name)
	return err == nil
}

// PrintHelp writes the --shell --help output to stdout.
func PrintHelp(binName string, languages ...string) {
	lang := "en"
	if len(languages) > 0 {
		lang = i18n.GetLanguage(languages[0], "")
	}
	fmt.Println(i18n.TranslateFormat(lang, "cli.shell_help_full", i18n.Args("binary", binName)))
}

// PrintInit writes the shell init snippet (meant to be eval'd) for the
// detected or named shell.
func PrintInit(binName, shellName string) error {
	sh := shellName
	if sh == "" {
		sh = Detect()
	} else {
		var err error
		sh, err = Normalize(sh)
		if err != nil {
			return err
		}
	}

	switch sh {
	case "bash":
		fmt.Printf("source <(%s --shell completions bash)\n", binName)
	case "fish":
		fmt.Printf("%s --shell completions fish | source\n", binName)
	case "zsh":
		fmt.Printf("source <(%s --shell completions zsh)\n", binName)
	case "powershell":
		fmt.Printf("%s --shell completions powershell | Out-String | Invoke-Expression\n", binName)
	case "sh", "dash", "ksh":
		fmt.Printf("# %s: %s has no programmable completion, nothing to load\n", binName, sh)
	default:
		return fmt.Errorf("unsupported shell %q", sh)
	}
	return nil
}

// PrintCompletions writes the completion script for the detected or named shell.
func PrintCompletions(binName, shellName string) error {
	sh := shellName
	if sh == "" {
		sh = Detect()
	} else {
		var err error
		sh, err = Normalize(sh)
		if err != nil {
			return err
		}
	}

	switch sh {
	case "bash":
		printBash(binName)
	case "zsh":
		printZsh(binName)
	case "fish":
		printFish(binName)
	case "sh", "dash", "ksh":
		return &NoProgrammableCompletionError{Shell: sh}
	case "powershell":
		printPowerShell(binName)
	}
	return nil
}

// ── Shell-specific completion generators ────────────────────────────────────

func printBash(bin string) {
	fmt.Printf(`# bash completion for %s
_%s() {
    local cur prev opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    opts="--help --version --status --daemon --debug
          --shell --service --maintenance --update
          --config --data --cache --log --backup --pid
          --port --address --baseurl --mode --color --lang"

    case "${prev}" in
        --shell)
            COMPREPLY=($(compgen -W "completions init --help" -- "${cur}"))
            return 0 ;;
        --service)
            COMPREPLY=($(compgen -W "start stop restart reload --install --uninstall --disable --help" -- "${cur}"))
            return 0 ;;
        --maintenance)
            COMPREPLY=($(compgen -W "backup restore update mode setup pgp token data compliance scheduler --help" -- "${cur}"))
            return 0 ;;
        --update)
            COMPREPLY=($(compgen -W "check yes branch --help" -- "${cur}"))
            return 0 ;;
        --mode)
            COMPREPLY=($(compgen -W "production development" -- "${cur}"))
            return 0 ;;
        --color)
            COMPREPLY=($(compgen -W "auto yes no" -- "${cur}"))
            return 0 ;;
        --config|--data|--cache|--log|--backup)
            COMPREPLY=($(compgen -d -- "${cur}"))
            return 0 ;;
        --pid)
            COMPREPLY=($(compgen -f -- "${cur}"))
            return 0 ;;
    esac

    COMPREPLY=($(compgen -W "${opts}" -- "${cur}"))
    return 0
}
complete -F _%s %s
`, bin, bin, bin, bin)
}

func printZsh(bin string) {
	completion := fmt.Sprintf(`#compdef %s
# zsh completion for %s
_%s() {
    local -a opts
    opts=(
        '--help[Show help]'
        '--version[Show version]'
        '--status[Show server status]'
        '--daemon[Run as background daemon]'
        '--debug[Enable debug logging]'
        '--shell[Shell integration]:subcmd:(completions init --help)'
        '--service[Service management]:subcmd:(start stop restart reload --install --uninstall --disable --help)'
        '--maintenance[Maintenance tasks]:subcmd:(backup restore update mode setup pgp token data compliance scheduler --help)'
        '--update[Update management]:subcmd:(check yes branch --help)'
        '--config[Config directory]:dir:_directories'
        '--data[Data directory]:dir:_directories'
        '--cache[Cache directory]:dir:_directories'
        '--log[Log directory]:dir:_directories'
        '--backup[Backup directory]:dir:_directories'
        '--pid[PID file path]:file:_files'
        '--port[Listen port]:port:'
        '--address[Listen address]:addr:'
        '--baseurl[Base URL]:url:'
        '--mode[Application mode]:mode:(production development)'
        '--color[Color output]:when:(auto yes no)'
        '--lang[Language]:lang:(%s)'
    )
    _arguments -s "${opts[@]}"
}
_%s "$@"
`, bin, bin, bin, strings.Join(i18n.SupportedCodes(), " "), bin)
	fmt.Print(completion)
}

func printFish(bin string) {
	completion := fmt.Sprintf(`# fish completion for %s
complete -c %s -l help    -d 'Show help'
complete -c %s -l version -d 'Show version'
complete -c %s -l status  -d 'Show server status'
complete -c %s -l daemon  -d 'Run as background daemon'
complete -c %s -l debug   -d 'Enable debug logging'

complete -c %s -l shell -d 'Shell integration' -xa 'completions init --help'
complete -c %s -l service -d 'Service management' -xa 'start stop restart reload --install --uninstall --disable --help'
complete -c %s -l maintenance -d 'Maintenance tasks' -xa 'backup restore update mode setup pgp token data compliance scheduler --help'
complete -c %s -l update -d 'Update management' -xa 'check yes branch --help'

complete -c %s -l config  -d 'Config directory'  -xa '(__fish_complete_directories)'
complete -c %s -l data    -d 'Data directory'    -xa '(__fish_complete_directories)'
complete -c %s -l cache   -d 'Cache directory'   -xa '(__fish_complete_directories)'
complete -c %s -l log     -d 'Log directory'     -xa '(__fish_complete_directories)'
complete -c %s -l backup  -d 'Backup directory'  -xa '(__fish_complete_directories)'
complete -c %s -l pid     -d 'PID file path'     -r

complete -c %s -l port    -d 'Listen port'
complete -c %s -l address -d 'Listen address'
complete -c %s -l baseurl -d 'Base URL'
complete -c %s -l mode    -d 'Application mode'  -xa 'production development'
complete -c %s -l color   -d 'Color output'      -xa 'auto yes no'
complete -c %s -l lang    -d 'Language'          -xa '%s'
`, bin,
		bin, bin, bin, bin, bin,
		bin, bin, bin, bin,
		bin, bin, bin, bin, bin, bin,
		bin, bin, bin, bin, bin, bin, strings.Join(i18n.SupportedCodes(), " "))
	fmt.Print(completion)
}

func printPowerShell(bin string) {
	fmt.Printf(`# PowerShell completion for %s
Register-ArgumentCompleter -Native -CommandName '%s' -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $opts = @(
        '--help','--version','--status','--daemon','--debug',
        '--shell','--service','--maintenance','--update',
        '--config','--data','--cache','--log','--backup','--pid',
        '--port','--address','--baseurl','--mode','--color','--lang'
    )
    $opts | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`, bin, bin)
}
