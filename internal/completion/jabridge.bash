_jabridge_completion() {
    local current
    current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()

    if (( COMP_CWORD == 1 )); then
        mapfile -t COMPREPLY < <(compgen -W 'status battery settings model sound firmware update completion daemon --version --help' -- "$current")
        return
    fi

    case "${COMP_WORDS[1]}" in
        update)
            mapfile -t COMPREPLY < <(compgen -W '--check --prerelease --help' -- "$current")
            ;;
        completion)
            mapfile -t COMPREPLY < <(compgen -W 'bash' -- "$current")
            ;;
        settings)
            if (( COMP_CWORD == 2 )); then
                mapfile -t COMPREPLY < <(compgen -W 'list set --help' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 3 ]]; then
                mapfile -t COMPREPLY < <(compgen -W 'dongle.auto-pairing dongle.prioritize-computer-audio dongle.dedicated-call dongle.bluetooth-radio dongle.softphone-integration headset.sidetone headset.in-call-busylight headset.on-head-detection headset.music-mode headset.auto-answer-on-head headset.auto-pause-music headset.reverse-stereo headset.smart-ringer headset.sidetone-level headset.voice-prompts headset.controller-ringer-volume headset.controller-ringtone headset.call-button headset.mute-button headset.three-dot-button headset.four-dot-button' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 4 ]]; then
                case "${COMP_WORDS[3]}" in
                    headset.sidetone-level)
                        mapfile -t COMPREPLY < <(compgen -W '-9-db -6-db -3-db 0-db 3-db 6-db' -- "$current")
                        ;;
                    headset.voice-prompts)
                        mapfile -t COMPREPLY < <(compgen -W 'tones voice off' -- "$current")
                        ;;
                    headset.controller-ringer-volume)
                        mapfile -t COMPREPLY < <(compgen -W 'off low medium high' -- "$current")
                        ;;
                    headset.controller-ringtone)
                        mapfile -t COMPREPLY < <(compgen -W 'tone-1 tone-2 tone-3 tone-4 tone-5 tone-6 tone-7 tone-8 tone-9 tone-10 custom random off' -- "$current")
                        ;;
                    headset.call-button|headset.mute-button|headset.three-dot-button|headset.four-dot-button)
                        mapfile -t COMPREPLY < <(compgen -W 'none call-handling mute speed-dial busylight push-to-talk headset-busylight microsoft-teams music' -- "$current")
                        ;;
                    *)
                        mapfile -t COMPREPLY < <(compgen -W 'on off' -- "$current")
                        ;;
                esac
                return
            fi
            ;;
        sound|audio)
            if (( COMP_CWORD == 2 )); then
                mapfile -t COMPREPLY < <(compgen -W 'status output volume mute --help' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "mute" && $COMP_CWORD -eq 3 ]]; then
                mapfile -t COMPREPLY < <(compgen -W 'on off toggle' -- "$current")
                return
            fi
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
