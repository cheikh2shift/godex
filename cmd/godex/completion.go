package main

import (
	"fmt"
	"os"

	"github.com/cheikh-seck/godex/internal/config"
)

const bashCompletionTemplate = `#!/bin/bash
# shellcheck disable=SC2034
PROG="{{.ProgName}}"

{{.ProgName}}_get_providers() {
    local config_path="${HOME}/.godex/providers.yaml"
    if [[ -f "$config_path" ]]; then
        grep -E '^    - name:' "$config_path" | sed 's/.*- name://' | tr -d ' "'
    fi
}

{{.ProgName}}_get_flags_with_desc() {
    echo "--config:provider configuration YAML"
    echo "--provider:provider name to use"
    echo "--wizard:launch provider configuration wizard"
    echo "--prompt:run a single prompt non-interactively"
    echo "--auto-confirm:auto-run suggested commands"
    echo "--version:print version information"
    echo "--debug:enable debug mode"
    echo "--completion:generate shell completion"
}

_{{.ProgName}}_completion() {
    local cur prev words cword
    _init_completion || return
    
    case "${prev}" in
    --provider)
        local providers
        providers=$(${PROG}_get_providers)
        COMPREPLY=($(compgen -W "$providers" -- "${cur}"))
        return
        ;;
    --config)
        _filedir yaml yml
        return
        ;;
    --completion)
        COMPREPLY=($(compgen -W "bash zsh fish" -- "${cur}"))
        return
        ;;
    esac
    
    local flags
    flags=$(${PROG}_get_flags_with_desc)
    
    if [[ -z "${cur}" ]]; then
        # Empty input - show all flags
        while IFS=: read -r flag desc; do
            COMPREPLY+=("$flag")
        done <<< "$flags"
    elif [[ "${cur}" == -* ]]; then
        # User started with dash - filter flags
        local filter="${cur}"
        while IFS=: read -r flag desc; do
            if [[ "$flag" == "$filter"* ]]; then
                COMPREPLY+=("$flag")
            fi
        done <<< "$flags"
    else
        # Show providers for non-flag input
        local providers
        providers=$(${PROG}_get_providers)
        while IFS= read -r p; do
            COMPREPLY+=("$p")
        done <<< "$providers"
    fi
}

complete -F _{{.ProgName}}_completion {{.ProgName}}
`

const zshCompletionTemplate = `#compdef godex

_{{.ProgName}}_completion() {
    local -a providers
    local config_path="${HOME}/.godex/providers.yaml"
    
    if [[ -f "$config_path" ]]; then
        providers=(${(f)"$(GODEX_COMPLETE="providers $config_path" {{.ProgName}})"})
    fi
    
    _arguments \
        '1: :->cmd' \
        '2: :->arg' \
        '*::arg:->args'
    
    case "$state" in
        cmd)
            _describe 'providers' providers
            ;;
    esac
}

_{{.ProgName}}_completion "$@"
`

type completionData struct {
	ProgName string
}

func generateBashCompletion() string {
	data := completionData{ProgName: "godex"}
	return executeTemplate("bash", bashCompletionTemplate, data)
}

func generateZshCompletion() string {
	data := completionData{ProgName: "godex"}
	return executeTemplate("zsh", zshCompletionTemplate, data)
}

func executeTemplate(shell string, tmpl string, data completionData) string {
	result := tmpl
	result = replaceAll(result, "{{.ProgName}}", data.ProgName)
	return result
}

func replaceAll(s, old, new string) string {
	for {
		idx := -1
		for i := 0; i < len(s); i++ {
			if i+len(old) <= len(s) && s[i:i+len(old)] == old {
				idx = i
				break
			}
		}
		if idx == -1 {
			break
		}
		s = s[:idx] + new + s[idx+len(old):]
	}
	return s
}

func listProviders(configPath string) []string {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil
	}

	var names []string
	for _, p := range cfg.Providers {
		names = append(names, p.Name)
	}
	return names
}

func runCompletion(args []string) {
	if len(args) == 0 {
		printCompletionHelp()
		return
	}

	switch args[0] {
	case "bash":
		fmt.Print(generateBashCompletion())
	case "zsh":
		fmt.Print(generateZshCompletion())
	case "fish":
		generateFishCompletion()
	case "flags":
		printFlagCompletions()
	case "providers":
		if len(args) > 1 {
			providers := listProviders(args[1])
			for _, p := range providers {
				fmt.Println(p)
			}
		}
	default:
		printCompletionHelp()
	}
}

func printFlagCompletions() {
	flags := []string{
		"--config",
		"--provider",
		"--wizard",
		"--prompt",
		"--auto-confirm",
		"--version",
		"--debug",
		"--completion",
		"--help",
	}
	for _, f := range flags {
		fmt.Println(f)
	}
}

func generateFishCompletion() {
	configPath := os.ExpandEnv("${HOME}/.godex/providers.yaml")

	fmt.Printf(`# fish completion for godex

function __godex_complete_providers
    set -l config_path %s
    if test -f "$config_path"
        GODEX_COMPLETE="providers $config_path" godex
    end
end

complete -c godex -l config -r -f
complete -c godex -l provider -x -a "(__godex_complete_providers)"
complete -c godex -l wizard -s w -n "__fish_use_subcommand" -f
complete -c godex -l prompt -s p -x
complete -c godex -l auto-confirm -s y -f
complete -c godex -l version -s v -f
complete -c godex -l debug -s d -f
complete -c godex -l completion -x -a "bash zsh fish" -d "Generate shell completion"
`, configPath)
}

func printCompletionHelp() {
	fmt.Print(`Shell completion for godex

Usage:
  godex --completion <shell>

Shells:
  bash    Generate bash completion script
  zsh     Generate zsh completion script
  fish    Generate fish completion script
`)
}
