_jabridge_completion() {
    local current
    current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()

    if (( COMP_CWORD == 1 )); then
        COMPREPLY=( $(compgen -W 'status update --version --help' -- "$current") )
        return
    fi

    case "${COMP_WORDS[1]}" in
        update)
            COMPREPLY=( $(compgen -W '--check --prerelease --help' -- "$current") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W 'bash' -- "$current") )
            ;;
    esac
}

complete -F _jabridge_completion jabridge
