# Bash completion script for goback
# Install: eval "$(goback completion bash)"

_goback() {
    local cur cmd
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    cmd="${COMP_WORDS[1]}"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "auth clear completion init daemon run now dry-run list status last version help" -- "$cur") )
        return 0
    fi

    local names=""
    if [[ -f ~/.config/goback/config.yaml ]]; then
        names=$(grep '^\s*- name:' ~/.config/goback/config.yaml | sed 's/.*name:\s*//')
    fi

    case "$cmd" in
        run|dry-run)
            COMPREPLY=( $(compgen -W "--local $names" -- "$cur") )
            ;;
        now)
            COMPREPLY=( $(compgen -W "--local" -- "$cur") )
            ;;
        clear)
            COMPREPLY=( $(compgen -W "$names" -- "$cur") )
            ;;
        last)
            COMPREPLY=( $(compgen -W "--epoch -e $names" -- "$cur") )
            ;;
        auth)
            COMPREPLY=( $(compgen -W "--clear" -- "$cur") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") )
            ;;
    esac
    return 0
}

complete -F _goback goback
