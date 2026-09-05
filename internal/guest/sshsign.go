package guest

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// sshSignOptions holds parsed arguments in the `ssh-keygen -Y sign`
// style.
type sshSignOptions struct {
	printPublicKey bool
	mode           string
	namespace      string
	keyFile        string
	target         string
}

// parseSSHSignArguments parses arguments as git passes them to
// gpg.ssh.program. The namespace defaults to "git".
func parseSSHSignArguments(arguments []string) sshSignOptions {
	options := sshSignOptions{namespace: "git"}
	if len(arguments) > 0 && arguments[0] == "--print-public-key" {
		options.printPublicKey = true
		return options
	}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		value := ""
		if index+1 < len(arguments) {
			value = arguments[index+1]
		}
		switch {
		case argument == "-Y":
			options.mode = value
			index++
		case argument == "-n":
			options.namespace = value
			index++
		case argument == "-f":
			options.keyFile = value
			index++
		case strings.HasPrefix(argument, "-"):
		default:
			options.target = argument
		}
	}
	return options
}

// publicKeyPrefix returns the type and key material from an OpenSSH
// public key line, without the comment.
func publicKeyPrefix(text string) string {
	fields := strings.Fields(strings.SplitN(text, "\n", 2)[0])
	if len(fields) < 2 {
		return strings.Join(fields, " ")
	}
	return fields[0] + " " + fields[1]
}

// chooseSigningKey picks the key matching wanted, or the only
// available key if there is exactly one. Returns false if no choice
// is possible.
func chooseSigningKey(keys []signingKey, wanted string) (signingKey, bool) {
	var candidates []signingKey
	for _, key := range keys {
		if key.PublicKey == "" {
			continue
		}
		candidates = append(candidates, key)
		if wanted != "" && publicKeyPrefix(key.PublicKey) == wanted {
			return key, true
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return signingKey{}, false
}

// runSSHSign acts as a stand-in for `ssh-keygen -Y sign`. It takes
// the argument list, a broker client, and writers for stdout and
// stderr. It returns 0 on success, 1 on failure, or 2 on bad usage.
func runSSHSign(arguments []string, client *boxClient, stdout,
	stderr io.Writer) int {
	options := parseSSHSignArguments(arguments)
	ctx := context.Background()
	if options.printPublicKey {
		keys, err := client.keys(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "prison-ssh-sign: %v\n", err)
			return 1
		}
		for _, key := range keys {
			if key.PublicKey != "" {
				fmt.Fprint(stdout, withTrailingNewline(key.PublicKey))
				return 0
			}
		}
		fmt.Fprintln(stderr, "prison-ssh-sign: this project holds no signing"+
			" key with a public half; `prison secret grant` one on the host")
		return 1
	}
	if options.mode != "sign" {
		fmt.Fprintln(stderr, "prison-ssh-sign: only `-Y sign` is stood in for"+
			" here; verification still needs the real ssh-keygen")
		return 2
	}
	wanted := ""
	if options.keyFile != "" {
		if content, err := os.ReadFile(options.keyFile); err == nil {
			wanted = publicKeyPrefix(string(content))
		}
	}
	keys, err := client.keys(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "prison-ssh-sign: %v\n", err)
		return 1
	}
	key, ok := chooseSigningKey(keys, wanted)
	if !ok {
		fmt.Fprintln(stderr, "prison-ssh-sign: this project holds no signing"+
			" secret prison can match; `prison secret grant` one on the host")
		return 1
	}
	var payload []byte
	if options.target != "" {
		payload, err = os.ReadFile(options.target)
	} else {
		payload, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintf(stderr, "prison-ssh-sign: %v\n", err)
		return 1
	}
	signature, err := client.sign(ctx, key.Secret, payload, options.namespace)
	if err != nil {
		fmt.Fprintf(stderr, "prison-ssh-sign: %v\n", err)
		return 1
	}
	output := withTrailingNewline(signature)
	if options.target == "" {
		fmt.Fprint(stdout, output)
		return 0
	}
	if err := os.WriteFile(options.target+".sig", []byte(output),
		0o644); err != nil {
		fmt.Fprintf(stderr, "prison-ssh-sign: %v\n", err)
		return 1
	}
	return 0
}
