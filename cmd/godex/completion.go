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

{{.ProgName}}_get_mcp_servers() {
    local config_path=$(${PROG}_get_config_path)
    local provider_name="${1:-}"
    if [[ -z "$provider_name" ]]; then
        provider_name=$(${PROG}_get_default_provider)
    fi
    if [[ -f "$config_path" ]]; then
        ${PROG} --completion mcp-servers "$config_path" "$provider_name"
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
    echo "mcp:manage MCP servers (subcommand)"
}

_{{.ProgName}}_completion() {
    local cur prev words cword
    _init_completion || return

    if [[ "${words[1]}" == "mcp" ]]; then
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "add remove" -- "${cur}"))
            return
        fi
        case "${prev}" in
        --name)
            local provider_name=""
            local i
            for ((i=1; i<cword; i++)); do
                if [[ "${words[i]}" == "--provider" ]] && [[ -n "${words[i+1]}" ]] && [[ "${words[i+1]}" != --* ]]; then
                    provider_name="${words[i+1]}"
                    break
                fi
            done
            if [[ "${words[2]}" == "remove" ]]; then
                local servers
                servers=$(${PROG}_get_mcp_servers "$provider_name")
                COMPREPLY=($(compgen -W "$servers" -- "${cur}"))
            else
                COMPREPLY=($(compgen -W "filesystem bash webscraper" -- "${cur}"))
            fi
            return
            ;;
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
        --allowed-path)
            COMPREPLY=($(compgen -d -S/ -- "${cur}"))
            return
            ;;
        esac

        local mcp_flags="--provider --name --command --args --env --transport --allowed-path --allowed-url"
        if [[ "${cur}" == -* ]]; then
            COMPREPLY=($(compgen -W "${mcp_flags}" -- "${cur}"))
            return
        fi
    fi
    
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
        COMPREPLY+=("mcp")
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
            local -a commands
            commands=("mcp:manage MCP servers")
            _describe 'commands' commands
            _describe 'providers' providers
            ;;
        args)
            case "$words[1]" in
                mcp)
                    if [[ $CURRENT -eq 2 ]]; then
                        _describe 'subcommands' 'add:register MCP server' 'remove:unregister MCP server'
                        return
                    fi
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
                    local -a mcp_servers
                    if [[ -n "$provider_name" ]]; then
                        mcp_servers=(${(f)"$({{.ProgName}} --completion mcp-servers "$config_path" "$provider_name")"})
                    fi
                    _arguments \
                        '--provider[provider name]:provider:->providers' \
                        '--name[MCP server name]:server:->mcp_name' \
                        '--command[external MCP command]' \
                        '--args[command arg]' \
                        '--env[env var KEY=VALUE]' \
                        '--transport[MCP transport]' \
                        '--allowed-path[allowed path]' \
                        '--allowed-url[allowed URL]'
                    return
                    ;;
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
        mcp_servers)
            if (( ${#mcp_servers[@]} )); then
                _describe 'mcp servers' mcp_servers
            fi
            ;;
        mcp_name)
            if [[ "${words[2]}" == "add" ]]; then
                local -a builtins
                builtins=("filesystem" "bash" "webscraper")
                _describe 'mcp servers' builtins
            else
                if (( ${#mcp_servers[@]} )); then
                    _describe 'mcp servers' mcp_servers
                fi
            fi
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

func listMCPServers(configPath string, providerName string) []string {
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

	var names []string
	for _, s := range provider.MCPServers {
		names = append(names, s.Name)
	}
	return names
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
	case "mcp-servers":
		configPath := os.ExpandEnv("${HOME}/.godex/providers.yaml")
		providerName := ""
		if len(args) > 1 {
			configPath = args[1]
		}
		if len(args) > 2 {
			providerName = args[2]
		}
		servers := listMCPServers(configPath, providerName)
		for _, s := range servers {
			fmt.Println(s)
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

function __godex_complete_mcp_servers
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
        godex --completion mcp-servers "$config_path" "$current_provider"
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
complete -c godex -n "__fish_use_subcommand" -a "mcp" -d "Manage MCP servers"
complete -c godex -n "__fish_seen_subcommand_from mcp" -a "add remove serve" -d "MCP subcommand"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l provider -x -a "(__godex_complete_providers)"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l name -x -a "filesystem bash webscraper"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l command -x
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l args -x
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l env -x
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l transport -x
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l allowed-path -x -a "(__fish_complete_directories)"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from add" -l allowed-url -x
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from remove" -l provider -x -a "(__godex_complete_providers)"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from remove" -l name -x -a "(__godex_complete_mcp_servers)"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from serve" -a "filesystem" -d "Serve built-in MCP server"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from serve; and __fish_seen_subcommand_from filesystem" -l allowed-path -x -a "(__fish_complete_directories)"
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from serve; and __fish_seen_subcommand_from filesystem" -l auto-confirm -f
complete -c godex -n "__fish_seen_subcommand_from mcp; and __fish_seen_subcommand_from serve; and __fish_seen_subcommand_from filesystem" -l use-roots -x -a "true false"
`, defaultConfigPath, defaultConfigPath, defaultConfigPath)
	}

func printCompletionHelp() {
	fmt.Print(`Shell completion for godex

Usage:
  godex --completion <shell>

Includes flags plus the "mcp" subcommands (add/remove) and their options.

Shells:
  bash    Generate bash completion script
  zsh     Generate zsh completion script
  fish    Generate fish completion script
`)
}
