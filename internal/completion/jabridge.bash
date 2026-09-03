_jabridge_completion() {
    local current
    current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()

    if (( COMP_CWORD == 1 )); then
        mapfile -t COMPREPLY < <(compgen -W 'status firmware update --version --help' -- "$current")
        return
    fi

    case "${COMP_WORDS[1]}" in
        update)
            mapfile -t COMPREPLY < <(compgen -W '--check --prerelease --help' -- "$current")
            ;;
        completion)
            mapfile -t COMPREPLY < <(compgen -W 'bash' -- "$current")
            ;;
        firmware|fw)
            if (( COMP_CWORD == 2 )); then
                mapfile -t COMPREPLY < <(compgen -W 'status download verify install --help' -- "$current")
                return
            fi
            case "${COMP_WORDS[2]}" in
                download)
                    mapfile -t COMPREPLY < <(compgen -W '--pid --version --out' -- "$current")
                    ;;
                verify|install)
                    mapfile -t COMPREPLY < <(compgen -f -- "$current")
                    ;;
            esac
            ;;
    esac
}

complete -F _jabridge_completion jabridge ./jabridge
