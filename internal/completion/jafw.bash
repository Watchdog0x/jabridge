_jafw_completion() {
    local current command
    current="${COMP_WORDS[COMP_CWORD]}"
    command="${COMP_WORDS[1]}"
    COMPREPLY=()

    if (( COMP_CWORD == 1 )); then
        COMPREPLY=( $(compgen -W 'status download verify --version --help' -- "$current") )
        return
    fi

    case "$command" in
        completion)
            COMPREPLY=( $(compgen -W 'bash' -- "$current") )
            ;;
        download)
            COMPREPLY=( $(compgen -W '--pid --version --out' -- "$current") )
            ;;
        detect|manifest|verify|flash)
            COMPREPLY=( $(compgen -f -- "$current") )
            ;;
    esac
}

complete -F _jafw_completion jafw
