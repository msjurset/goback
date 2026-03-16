# Bash completion script for goback
# Install: goback completion bash > /etc/bash_completion.d/goback
#   — or — source goback.bash

_goback() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "auth init daemon run now dry-run list status version help" -- "$cur") )
        return 0
    fi

    case "$prev" in
        run|dry-run)
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
    esac
}

complete -F _goback goback
