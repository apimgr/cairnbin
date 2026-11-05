// Package shell — client completion generators for cairnbin-cli.
package shell

import "fmt"

// PrintClientCompletions writes completion scripts for cairnbin-cli to stdout.
// It follows the same shell-detection logic as PrintCompletions.
func PrintClientCompletions(binName, shellName string) error {
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
		printClientBash(binName)
	case "zsh":
		printClientZsh(binName)
	case "fish":
		printClientFish(binName)
	case "sh", "dash", "ksh":
		printClientPOSIX(binName)
	case "powershell":
		printClientPowerShell(binName)
	}
	return nil
}

func printClientBash(bin string) {
	fmt.Printf(`# bash completion for %s
_%s() {
    local cur prev words cword
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    words=("${COMP_WORDS[@]}")
    cword=$COMP_CWORD

    local global_opts="--server --json --color --debug --version --help --shell -h -v"
    local commands="create get delete del rm list ls scheduler update version"

    local cmd=""
    for w in "${words[@]:1}"; do
        case "$w" in
            create|get|delete|del|rm|list|ls|scheduler|update|version) cmd="$w"; break ;;
        esac
    done

    case "${prev}" in
        --color)
            COMPREPLY=($(compgen -W "auto yes no" -- "${cur}"))
            return 0 ;;
        --shell)
            COMPREPLY=($(compgen -W "completions init --help" -- "${cur}"))
            return 0 ;;
        --lang)
            COMPREPLY=($(compgen -W "text go python javascript typescript rust java c cpp csharp php ruby bash powershell html css json yaml toml xml sql markdown" -- "${cur}"))
            return 0 ;;
        --expiry)
            COMPREPLY=($(compgen -W "1h 1d 1w 1m 3m 6m 1y 2y never" -- "${cur}"))
            return 0 ;;
        --server|--title|--burn|--limit|--page) return 0 ;;
    esac

    if [[ -z "$cmd" ]]; then
        COMPREPLY=($(compgen -W "$commands $global_opts" -- "${cur}"))
        return 0
    fi

    case "$cmd" in
        create)
            COMPREPLY=($(compgen -W "--lang --expiry --burn --unlisted --title" -- "${cur}"))
            ;;
        get) ;;
        delete|del|rm) ;;
        list|ls)
            COMPREPLY=($(compgen -W "--limit --page" -- "${cur}"))
            ;;
    esac
    return 0
}
complete -F _%s %s
`, bin, bin, bin, bin)
}

func printClientZsh(bin string) {
	fmt.Printf(`#compdef %s
# zsh completion for %s
_%s() {
    local state
    _arguments -C \
        '--server[Server base URL]:url:' \
        '--json[Machine-readable JSON output]' \
        '--color[Color output]:when:(auto yes no)' \
        '--debug[Enable debug output]' \
        '--version[Print version]' \
        '--shell[Shell integration]:subcmd:(completions init --help)' \
        '(-h --help)'{-h,--help}'[Show help]' \
        '(-v --version)'{-v,--version}'[Print version]' \
        ':command:->command' \
        '*::args:->args'

    case $state in
        command)
            local cmds
            cmds=(
                'create:Create paste from stdin or file'
                'get:Fetch raw paste content'
                'delete:Delete paste using delete token'
                'list:List recent public pastes'
                'version:Print version'
            )
            _describe 'command' cmds ;;
        args)
            case $words[1] in
                create)
                    _arguments \
                        '--lang[Syntax language]:lang:(text go python javascript typescript rust java c cpp bash html css json yaml toml xml sql markdown)' \
                        '--expiry[Expiry]:duration:(1h 1d 1w 1m 3m 6m 1y 2y never)' \
                        '--burn[Delete after N views]:n:' \
                        '--unlisted[Create as unlisted]' \
                        '--title[Paste title]:title:' \
                        '*:file:_files' ;;
                list)
                    _arguments \
                        '--limit[Results per page]:n:' \
                        '--page[Page number]:n:' ;;
            esac ;;
    esac
}
_%s "$@"
`, bin, bin, bin, bin)
}

func printClientFish(bin string) {
	fmt.Printf(`# fish completion for %s
# Global flags
complete -c %s -l server  -d 'Server base URL' -r
complete -c %s -l json    -d 'Machine-readable JSON output'
complete -c %s -l color   -d 'Color output' -xa 'auto yes no'
complete -c %s -l debug   -d 'Enable debug output'
complete -c %s -l version -d 'Print version'
complete -c %s -l shell   -d 'Shell integration' -xa 'completions init --help'
complete -c %s -s h -l help    -d 'Show help'
complete -c %s -s v -l version -d 'Print version'

# Subcommands (only when no subcommand given yet)
complete -c %s -n '__fish_use_subcommand' -f -a 'create' -d 'Create paste from stdin or file'
complete -c %s -n '__fish_use_subcommand' -f -a 'get'    -d 'Fetch raw paste content'
complete -c %s -n '__fish_use_subcommand' -f -a 'delete' -d 'Delete paste using delete token'
complete -c %s -n '__fish_use_subcommand' -f -a 'list'   -d 'List recent public pastes'
complete -c %s -n '__fish_use_subcommand' -f -a 'version' -d 'Print version'

# create flags
complete -c %s -n '__fish_seen_subcommand_from create' -l lang    -d 'Syntax language' -xa 'text go python javascript typescript rust java c cpp bash html css json yaml toml xml sql markdown'
complete -c %s -n '__fish_seen_subcommand_from create' -l expiry  -d 'Expiry duration' -xa '1h 1d 1w 1m 3m 6m 1y 2y never'
complete -c %s -n '__fish_seen_subcommand_from create' -l burn    -d 'Delete after N views' -r
complete -c %s -n '__fish_seen_subcommand_from create' -l unlisted -d 'Create as unlisted'
complete -c %s -n '__fish_seen_subcommand_from create' -l title   -d 'Paste title' -r

# list flags
complete -c %s -n '__fish_seen_subcommand_from list' -l limit -d 'Results per page' -r
complete -c %s -n '__fish_seen_subcommand_from list' -l page  -d 'Page number' -r
`, bin,
		bin, bin, bin, bin, bin, bin, bin, bin,
		bin, bin, bin, bin, bin,
		bin, bin, bin, bin, bin,
		bin, bin)
}

func printClientPOSIX(bin string) {
	fmt.Printf(`# POSIX sh/dash/ksh completion for %s — source this file
# Usage: . <(%s --shell completions sh)
if [ -n "$BASH_VERSION" ]; then
    complete -W "--server --json --color --debug --version --help --shell create get delete list scheduler update version" %s
fi
`, bin, bin, bin)
}

func printClientPowerShell(bin string) {
	fmt.Printf(`# PowerShell completion for %s
Register-ArgumentCompleter -Native -CommandName '%s' -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $cmds = @('create','get','delete','del','rm','list','ls','scheduler','update','version')
    $flags = @('--server','--json','--color','--debug','--version','--help','--shell','-h','-v')
    $all = $cmds + $flags
    $all | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`, bin, bin)
}
