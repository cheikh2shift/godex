package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cheikh2shift/godex/internal/config"
	"github.com/cheikh2shift/godex/modelquery"
)

const bashCompletionTemplate = `#!/bin/bash
# shellcheck disable=SC2034
PROG="{{.ProgName}}"

{{.ProgName}}_get_config_path() {
    local default_path="${HOME}/.godex/providers.yaml"
    for ((i=1; i<cword; i++)); do
        if [[ "${words[i]}" == "--config" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
            echo "${words[i+1]}"
            return
        fi
    done
    echo "$default_path"
}

{{.ProgName}}_get_providers() {
    local config_path=$(${PROG}_get_config_path)
    if [[ -f "$config_path" ]]; then
        grep -E '^    - name:' "$config_path" | sed 's/.*- name://' | tr -d ' '
    fi
}

{{.ProgName}}_get_default_provider() {
    local config_path=$(${PROG}_get_config_path)
    if [[ -f "$config_path" ]]; then
        grep -E '^    - name:' "$config_path" | head -1 | sed 's/.*- name://' | tr -d ' '
    fi
}

{{.ProgName}}_get_models() {
    local config_path=$(${PROG}_get_config_path)
    local provider_name="${1:-}"
    local query="${2:-}"
    if [[ -f "$config_path" ]]; then
        if [[ -z "$provider_name" ]]; then
            provider_name=$(${PROG}_get_default_provider)
        fi
        ${PROG} --completion models "$config_path" "$provider_name" "$query"
    fi
}

{{.ProgName}}_get_flags_with_desc() {
    echo "--config:provider configuration YAML"
    echo "--provider:provider name to use"
    echo "--model:override provider model"
    echo "--hive:enable hive mode with a shared secret"
    echo "--wizard:launch provider configuration wizard"
    echo "--prompt:run a single prompt non-interactively"
    echo "--auto-confirm:auto-run suggested commands"
    echo "--version:print version information"
    echo "--debug:enable debug mode"
    echo "--completion:generate shell completion"
    echo "--llama-server:external llama.cpp server URL"
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
        COMPREPLY=($(compgen -d -S/ -- "${cur}"))
        COMPREPLY+=($(compgen -f -X '!*.yaml' -X '!*.yml' -- "${cur}"))
        return
        ;;
    --completion)
        COMPREPLY=($(compgen -W "bash zsh fish" -- "${cur}"))
        return
        ;;
    --model)
        local provider_name=""
        local i
        for ((i=1; i<cword; i++)); do
            if [[ "${words[i]}" == "--provider" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
                provider_name="${words[i+1]}"
                break
            fi
        done
        if [[ -z "$provider_name" ]]; then
            provider_name=$(${PROG}_get_default_provider)
        fi
        while IFS= read -r model; do
            COMPREPLY+=("$model")
        done < <(${PROG} --completion models "$(${PROG}_get_config_path)" "$provider_name" "${cur}")
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

{{.ProgName}}_completion() {
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
        args)
            case "$words[1]" in
                --config)
                    _files -g "*.yaml" -g "*.yml"
                    ;;
                --model)
                    local -a models
                    local provider_name=""
                    for ((i=2; i<=CURRENT; i++)); do
                        if [[ "${words[i-1]}" == "--provider" ]] && [[ -n "${words[i]}" ]] && [[ "${words[i]}" != --* ]]; then
                            provider_name="${words[i]}"
                            break
                        fi
                    done
                    if [[ -z "$provider_name" ]]; then
                        provider_name="${providers[1]}"
                    fi
                    if [[ -n "$provider_name" ]]; then
                        local -a all_models
                        all_models=(${(f)"$({{.ProgName}} --completion models "$config_path" "$provider_name" \"\")"})
                        _describe 'models' all_models
                    fi
                    ;;
            esac
            ;;
    esac
}

{{.ProgName}}_completion "$@"
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

func listModels(configPath string, providerName string, query string) []string {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil
	}

	var provider *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == providerName {
			provider = &cfg.Providers[i]
			break
		}
	}
	if provider == nil {
		if len(cfg.Providers) > 0 {
			provider = &cfg.Providers[0]
		}
	}
	if provider == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mqProvider := buildMQProvider(provider)

	var models []string
	var err2 error

	if query == "" {
		models, err2 = listAllModels(ctx, mqProvider)
	} else {
		models, err2 = searchModels(ctx, mqProvider, query)
	}

	if err2 != nil {
		return nil
	}
	return models
}

func listAllModels(ctx context.Context, p modelquery.Provider) ([]string, error) {
	models, err := modelquery.ListModels(ctx, p)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range models {
		names = append(names, m.ID)
	}
	return names, nil
}

func searchModels(ctx context.Context, p modelquery.Provider, query string) ([]string, error) {
	models, err := modelquery.SearchModels(ctx, p, query)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range models {
		names = append(names, m.ID)
	}
	return names, nil
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
	case "models":
		configPath := os.ExpandEnv("${HOME}/.godex/providers.yaml")
		providerName := ""
		query := ""
		if len(args) > 1 {
			configPath = args[1]
		}
		if len(args) > 2 {
			providerName = args[2]
		}
		if len(args) > 3 {
			query = args[3]
		}
		models := listModels(configPath, providerName, query)
		for _, m := range models {
			fmt.Println(m)
		}
	default:
		printCompletionHelp()
	}
}

func printFlagCompletions() {
	flags := []string{
		"--config",
		"--provider",
		"--model",
		"--hive",
		"--wizard",
		"--prompt",
		"--auto-confirm",
		"--version",
		"--debug",
		"--completion",
		"--llama-server",
		"--help",
	}
	for _, f := range flags {
		fmt.Println(f)
	}
}

func generateFishCompletion() {
	defaultConfigPath := os.ExpandEnv("${HOME}/.godex/providers.yaml")

	fmt.Printf(`# fish completion for godex

function __godex_complete_providers
    set -l config_path %s
    for arg in (commandline -opc)
        if test "$arg" = "--config"
            set config_path (commandline -opc)[(contains -i "$arg" (commandline -opc)) + 1]
        end
    end
    if test -f "$config_path"
        GODEX_COMPLETE="providers $config_path" godex
    end
end

function __godex_complete_models
    set -l default_path %s
    set -l config_path "$default_path"
    set -l current_provider ""
    for i in (seq (count (commandline -opc)))
        set -l arg (commandline -opc)[$i]
        if test "$arg" = "--config"
            set config_path (commandline -opc)[(math $i + 1)]
        else if test "$arg" = "--provider"
            set current_provider (commandline -opc)[(math $i + 1)]
        end
    end
    if test -z "$current_provider"
        set current_provider (godex --completion providers "$config_path" 2>/dev/null | head -1)
    end
    if test -f "$config_path"
        godex --completion models "$config_path" "$current_provider"
    end
end

complete -c godex -l config -r -f
complete -c godex -l provider -x -a "(__godex_complete_providers)"
complete -c godex -l model -x -a "(__godex_complete_models)"
complete -c godex -l wizard -s w -n "__fish_use_subcommand" -f
complete -c godex -l prompt -s p -x
complete -c godex -l auto-confirm -s y -f
complete -c godex -l version -s v -f
complete -c godex -l debug -s d -f
complete -c godex -l completion -x -a "bash zsh fish" -d "Generate shell completion"
complete -c godex -l llama-server -s l -x -d "External llama.cpp server URL"
`, defaultConfigPath, defaultConfigPath)
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
