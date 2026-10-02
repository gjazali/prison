package guest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"prison/internal/broker/protocol"
)

type initConfiguration struct {
	token        string
	brokerPort   int
	sudo         bool
	persistPaths []string
	command      []string
}

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
			"PRISON_TOKEN is not set. Start the box with `prison up`")
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
