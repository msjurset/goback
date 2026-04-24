# Bash completion script for goback
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
        run|dry-run|clear)
            local names=""
            if [[ -f ~/.config/goback/config.yaml ]]; then
                names=$(grep '^\s*- name:' ~/.config/goback/config.yaml | sed 's/.*name:\s*//')
            fi
            COMPREPLY=( $(compgen -W "$names" -- "$cur") )
            return 0
            ;;
        last)
            local names=""
            if [[ -f ~/.config/goback/config.yaml ]]; then
                names=$(grep '^\s*- name:' ~/.config/goback/config.yaml | sed 's/.*name:\s*//')
            fi
            if [[ "$cur" == -* ]]; then
                COMPREPLY=( $(compgen -W "--epoch -e" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "$names --epoch -e" -- "$cur") )
            fi
            return 0
            ;;
        --epoch|-e)
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
