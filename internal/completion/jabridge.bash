_jabridge_completion() {
    local current
    current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()

    if (( COMP_CWORD == 1 )); then
        mapfile -t COMPREPLY < <(compgen -W 'status battery diagnose debug buttons settings model models sound use firmware update setup service ipc completion daemon --version --help' -- "$current")
        return
    fi

    case "${COMP_WORDS[1]}" in
        debug)
            if [[ "${COMP_WORDS[COMP_CWORD-1]}" == "--output" ]]; then
                mapfile -t COMPREPLY < <(compgen -f -- "$current")
            else
                mapfile -t COMPREPLY < <(compgen -W '--output --buttons --help' -- "$current")
            fi
            ;;
        buttons)
            mapfile -t COMPREPLY < <(compgen -W '--seconds --help' -- "$current")
            ;;
        update)
            mapfile -t COMPREPLY < <(compgen -W '--check --prerelease --help' -- "$current")
            ;;
        completion)
            mapfile -t COMPREPLY < <(compgen -W 'bash' -- "$current")
            ;;
        ipc)
            if (( COMP_CWORD == 2 )); then
                mapfile -t COMPREPLY < <(compgen -W 'ping devices battery settings set select watch --help' -- "$current")
                return
            fi
            if [[ ("${COMP_WORDS[2]}" == "settings" || "${COMP_WORDS[2]}" == "set") && $COMP_CWORD -eq 3 ]]; then
                mapfile -t COMPREPLY < <(compgen -W 'dongle headset' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 4 ]]; then
                if [[ "${COMP_WORDS[3]}" == "dongle" ]]; then
                    mapfile -t COMPREPLY < <(compgen -W 'auto-pairing prioritize-computer-audio dedicated-call bluetooth-radio softphone-integration' -- "$current")
                else
                    mapfile -t COMPREPLY < <(compgen -W 'noise-control hearthrough-level answer-on-undock wind-noise-reduction spatial-call-audio spatial-media-audio sidetone in-call-busylight on-head-detection music-mode auto-answer-on-head auto-pause-music reverse-stereo smart-ringer boom-arm-answer auto-reject-call button-sounds firmware-upgrade-lock prioritize-computer-audio headset-ringer headset-name sidetone-level voice-prompts controller-ringer-volume controller-ringtone boom-arm-action boom-arm-guidance audio-protection auto-sleep mute-reminder sound-mode call-button mute-button three-dot-button four-dot-button' -- "$current")
                fi
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 5 ]]; then
                case "${COMP_WORDS[4]}" in
                    sidetone-level)
                        mapfile -t COMPREPLY < <(compgen -W '-9-db -6-db -3-db 0-db 3-db 6-db' -- "$current")
                        ;;
                    noise-control)
                        mapfile -t COMPREPLY < <(compgen -W 'off hearthrough anc' -- "$current")
                        ;;
                    hearthrough-level)
                        mapfile -t COMPREPLY < <(compgen -W 'level-1 level-2 level-3' -- "$current")
                        ;;
                    voice-prompts)
                        mapfile -t COMPREPLY < <(compgen -W 'tones voice off' -- "$current")
                        ;;
                    controller-ringer-volume)
                        mapfile -t COMPREPLY < <(compgen -W 'off low medium high' -- "$current")
                        ;;
                    controller-ringtone)
                        mapfile -t COMPREPLY < <(compgen -W 'tone-1 tone-2 tone-3 tone-4 tone-5 tone-6 tone-7 tone-8 tone-9 tone-10 custom random off' -- "$current")
                        ;;
                    boom-arm-action)
                        mapfile -t COMPREPLY < <(compgen -W 'disabled mute end-call full-mute' -- "$current")
                        ;;
                    boom-arm-guidance)
                        mapfile -t COMPREPLY < <(compgen -W 'sound-effects voice-prompts off' -- "$current")
                        ;;
                    audio-protection)
                        mapfile -t COMPREPLY < <(compgen -W 'basic-peakstop level-1 level-2 level-3 level-4 g616' -- "$current")
                        ;;
                    auto-sleep)
                        mapfile -t COMPREPLY < <(compgen -W 'never 30-min 1-hour 2-hours 4-hours 8-hours 12-hours 16-hours' -- "$current")
                        ;;
                    mute-reminder)
                        mapfile -t COMPREPLY < <(compgen -W 'off 10-seconds 20-seconds 30-seconds 40-seconds 50-seconds 60-seconds' -- "$current")
                        ;;
                    sound-mode)
                        mapfile -t COMPREPLY < <(compgen -W 'normal bass treble' -- "$current")
                        ;;
                    headset-name)
                        COMPREPLY=()
                        ;;
                    call-button|mute-button|three-dot-button|four-dot-button)
                        mapfile -t COMPREPLY < <(compgen -W 'none call-handling mute speed-dial busylight push-to-talk headset-busylight microsoft-teams music' -- "$current")
                        ;;
                    *)
                        mapfile -t COMPREPLY < <(compgen -W 'on off' -- "$current")
                        ;;
                esac
                return
            fi
            ;;
        service)
            mapfile -t COMPREPLY < <(compgen -W 'start status stop restart --help' -- "$current")
            ;;
        settings)
            if (( COMP_CWORD == 2 )); then
                mapfile -t COMPREPLY < <(compgen -W 'list set --help' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 3 ]]; then
                mapfile -t COMPREPLY < <(compgen -W 'dongle.auto-pairing dongle.prioritize-computer-audio dongle.dedicated-call dongle.bluetooth-radio dongle.softphone-integration headset.noise-control headset.hearthrough-level headset.answer-on-undock headset.wind-noise-reduction headset.spatial-call-audio headset.spatial-media-audio headset.sidetone headset.in-call-busylight headset.on-head-detection headset.music-mode headset.auto-answer-on-head headset.auto-pause-music headset.reverse-stereo headset.smart-ringer headset.boom-arm-answer headset.auto-reject-call headset.button-sounds headset.firmware-upgrade-lock headset.prioritize-computer-audio headset.headset-ringer headset.headset-name headset.sidetone-level headset.voice-prompts headset.controller-ringer-volume headset.controller-ringtone headset.boom-arm-action headset.boom-arm-guidance headset.audio-protection headset.auto-sleep headset.mute-reminder headset.sound-mode headset.call-button headset.mute-button headset.three-dot-button headset.four-dot-button' -- "$current")
                return
            fi
            if [[ "${COMP_WORDS[2]}" == "set" && $COMP_CWORD -eq 4 ]]; then
                case "${COMP_WORDS[3]}" in
                    headset.sidetone-level)
                        mapfile -t COMPREPLY < <(compgen -W '-9-db -6-db -3-db 0-db 3-db 6-db' -- "$current")
                        ;;
                    headset.noise-control)
                        mapfile -t COMPREPLY < <(compgen -W 'off hearthrough anc' -- "$current")
                        ;;
                    headset.hearthrough-level)
                        mapfile -t COMPREPLY < <(compgen -W 'level-1 level-2 level-3' -- "$current")
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
                    headset.boom-arm-action)
                        mapfile -t COMPREPLY < <(compgen -W 'disabled mute end-call full-mute' -- "$current")
                        ;;
                    headset.boom-arm-guidance)
                        mapfile -t COMPREPLY < <(compgen -W 'sound-effects voice-prompts off' -- "$current")
                        ;;
                    headset.audio-protection)
                        mapfile -t COMPREPLY < <(compgen -W 'basic-peakstop level-1 level-2 level-3 level-4 g616' -- "$current")
                        ;;
                    headset.auto-sleep)
                        mapfile -t COMPREPLY < <(compgen -W 'never 30-min 1-hour 2-hours 4-hours 8-hours 12-hours 16-hours' -- "$current")
                        ;;
                    headset.mute-reminder)
                        mapfile -t COMPREPLY < <(compgen -W 'off 10-seconds 20-seconds 30-seconds 40-seconds 50-seconds 60-seconds' -- "$current")
                        ;;
                    headset.sound-mode)
                        mapfile -t COMPREPLY < <(compgen -W 'normal bass treble' -- "$current")
                        ;;
                    headset.headset-name)
                        COMPREPLY=()
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
        use)
            mapfile -t COMPREPLY < <(compgen -W 'usb dongle --help' -- "$current")
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
                verify)
                    mapfile -t COMPREPLY < <(compgen -f -- "$current")
                    ;;
                install)
                    mapfile -t COMPREPLY < <(compgen -W '--i-accept-risk' -- "$current"; compgen -f -- "$current")
                    ;;
            esac
            ;;
    esac
}

complete -F _jabridge_completion jabridge ./jabridge
