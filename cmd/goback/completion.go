package main

import (
	"fmt"
	"os"
)

func cmdCompletion() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: goback completion <bash|zsh>")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "zsh":
		fmt.Print(zshCompletion)
	case "bash":
		fmt.Print(bashCompletion)
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell: %s (supported: bash, zsh)\n", os.Args[2])
		os.Exit(1)
	}
}

const zshCompletion = `#compdef goback

# Zsh completion script for goback
# Install: goback completion zsh > ~/.oh-my-zsh/custom/completions/_goback

_goback() {
    local -a commands
    commands=(
        'auth:Resolve op:// secrets and cache in system keychain'
        'clear:Remove cached secrets from keychain'
        'completion:Output shell completion script'
        'init:Create default config file'
        'daemon:Run the backup scheduler (foreground)'
        'run:Manually trigger one or all backups'
        'now:Run all backups immediately'
        'dry-run:Simulate backups (connect but don'\''t transfer)'
        'list:Show configured backup jobs'
        'status:Show recent backup history'
        'last:Print timestamp of last successful backup'
        'version:Print version information'
        'help:Show usage information'
    )

    _arguments -C \
        '1:command:->command' \
        '*::arg:->args'

    case $state in
        command)
            _describe -t commands 'goback command' commands
            ;;
        args)
            case $words[1] in
                run|dry-run|clear|last)
                    _goback_backup_names
                    ;;
                auth)
                    _arguments '--clear[Remove cached secrets from keychain]'
                    ;;
                completion)
                    _describe -t shells 'shell' '(bash zsh)'
                    ;;
            esac
            ;;
    esac
}

_goback_backup_names() {
    local -a names
    if [[ -f ~/.config/goback/config.yaml ]]; then
        names=(${(f)"$(grep '^\s*- name:' ~/.config/goback/config.yaml | sed 's/.*name:\s*//')"})
    fi
    _describe -t names 'backup name' names
}

_goback "$@"
`

const bashCompletion = `# Bash completion script for goback
# Install: eval "$(goback completion bash)"

_goback() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "auth clear completion init daemon run now dry-run list status last version help" -- "$cur") )
        return 0
    fi

    case "$prev" in
        run|dry-run|clear|last)
            local names=""
            if [[ -f ~/.config/goback/config.yaml ]]; then
                names=$(grep '^\s*- name:' ~/.config/goback/config.yaml | sed 's/.*name:\s*//')
            fi
            COMPREPLY=( $(compgen -W "$names" -- "$cur") )
            return 0
            ;;
        auth)
            COMPREPLY=( $(compgen -W "--clear" -- "$cur") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") )
            return 0
            ;;
    esac
}

complete -F _goback goback
`
