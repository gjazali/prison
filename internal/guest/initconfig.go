package guest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"prison/internal/broker/protocol"
)

// initConfiguration holds the settings for `prison-guest init`: the
// project token, broker port, sudo flag, persist paths, and the
// command to run.
type initConfiguration struct {
	token        string
	brokerPort   int
	sudo         bool
	persistPaths []string
	command      []string
}

// readInitConfiguration builds the init configuration from arguments
// and environment variables. It takes the command arguments and an env
// lookup function. Returns an error if the token is missing, the port
// is invalid, or the command is empty.
func readInitConfiguration(arguments []string,
	lookupEnv func(string) (string, bool)) (initConfiguration, error) {
	configuration := initConfiguration{
		brokerPort: protocol.DefaultBrokerPort,
		command:    arguments,
	}
	if len(arguments) == 0 {
		return configuration, errors.New("init needs the command to run")
	}
	token, ok := lookupEnv("PRISON_TOKEN")
	if !ok || token == "" {
		return configuration, errors.New(
			"PRISON_TOKEN is not set; the box was not created by prison up")
	}
	configuration.token = token
	if text, ok := lookupEnv("PRISON_BROKER_PORT"); ok && text != "" {
		port, err := strconv.Atoi(text)
		if err != nil || port < 1 || port > 65535 {
			return configuration, fmt.Errorf(
				"PRISON_BROKER_PORT=%q is not a port number", text)
		}
		configuration.brokerPort = port
	}
	if value, ok := lookupEnv("PRISON_SUDO"); ok {
		configuration.sudo = value == "yes"
	}
	if value, ok := lookupEnv("PRISON_PERSIST_PATHS"); ok {
		for _, path := range strings.Split(value, ":") {
			if path != "" {
				configuration.persistPaths = append(configuration.persistPaths,
					path)
			}
		}
	}
	return configuration, nil
}
