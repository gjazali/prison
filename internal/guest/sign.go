package guest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"prison/internal/broker/protocol"
)

// boxClient holds no token because the tunnel adds the token to each
// broker connection.
type boxClient struct {
	baseURL    string
	httpClient *http.Client
}

func newBoxClient(timeout time.Duration) *boxClient {
	return &boxClient{
		baseURL:    "http://" + protocol.BrokerHost,
		httpClient: &http.Client{Timeout: timeout},
	}
}

type signRequest struct {
	Secret    string `json:"secret"`
	Data      string `json:"data"`
	Namespace string `json:"namespace,omitempty"`
}

type signingKey struct {
	Secret      string `json:"secret"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
	Namespace   string `json:"namespace"`
	PublicKey   string `json:"public_key"`
	Available   bool   `json:"available"`
}

func brokerError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil &&
		envelope.Error.Message != "" {
		return errors.New(envelope.Error.Message)
	}
	return fmt.Errorf("the broker returned %s", response.Status)
}

func (c *boxClient) sign(ctx context.Context, secret string, data []byte,
	namespace string) (string, error) {
	body, err := json.Marshal(signRequest{
		Secret:    secret,
		Data:      base64.StdEncoding.EncodeToString(data),
		Namespace: namespace,
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+protocol.PathSign, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("cannot reach the broker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", brokerError(response)
	}
	var answer struct {
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", fmt.Errorf("cannot read the signature: %w", err)
	}
	if answer.Signature == "" {
		return "", errors.New("the broker sent no signature")
	}
	return answer.Signature, nil
}

func (c *boxClient) keys(ctx context.Context) ([]signingKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+protocol.PathKeys, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the broker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, brokerError(response)
	}
	var answer struct {
		Keys []signingKey `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, fmt.Errorf("cannot read the key list: %w", err)
	}
	return answer.Keys, nil
}

func runSign(arguments []string, client *boxClient, stdin io.Reader,
	stdout, stderr io.Writer) int {
	if len(arguments) < 1 || len(arguments) > 2 {
		fmt.Fprintln(stderr, "usage: prison-sign <secret> [file]")
		return 2
	}
	secret := arguments[0]
	var payload []byte
	var err error
	if len(arguments) == 2 {
		payload, err = os.ReadFile(arguments[1])
	} else {
		payload, err = io.ReadAll(stdin)
	}
	if err != nil {
		fmt.Fprintf(stderr, "prison-sign: %v\n", err)
		return 1
	}
	signature, err := client.sign(context.Background(), secret, payload,
		os.Getenv("PRISON_SIGN_NAMESPACE"))
	if err != nil {
		fmt.Fprintf(stderr, "prison-sign: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, withTrailingNewline(signature))
	return 0
}

func withTrailingNewline(text string) string {
	return strings.TrimRight(text, "\n") + "\n"
}
