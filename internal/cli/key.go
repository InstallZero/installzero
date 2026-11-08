package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/InstallZero/installzero/internal/keystore"
	"github.com/InstallZero/installzero/internal/llm"
)

var keyProviders = []string{"anthropic", "openai", "xai", "google", "hf"}

// cmdKey keeps hosted-model API keys in the OS credential store.
func cmdKey(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		printUsage(stderr, "key set|remove|status <provider>   (provider: "+strings.Join(keyProviders, ", ")+")")
		return 2
	}
	if len(args) == 1 && args[0] == "status" {
		for _, p := range keyProviders {
			cmdKey([]string{"status", p}, stdout, stderr)
		}
		return 0
	}
	if len(args) != 2 {
		return usage()
	}
	verb, provider := args[0], strings.ToLower(args[1])
	if !llm.IsRemoteSpec(provider + ":x") {
		return usage()
	}
	switch verb {
	case "set":
		in := bufio.NewReader(stdin)
		key := readKey(stdout, in, provider, "Enter to cancel")
		if key == "" {
			return 1
		}
		if provider != "hf" {
			if err := llm.CheckKey(context.Background(), provider, key); err != nil {
				fmt.Fprintf(stderr, "%s did not accept the key (%v); not saved.\n", provider, err)
				return 1
			}
		}
		if err := keystore.Set(provider, key); err != nil {
			fmt.Fprintf(stderr, "could not save the key in %s: %v\n", keystore.Name(provider), err)
			return 1
		}
		fmt.Fprintf(stdout, "Saved in %s.\n", keystore.Name(provider))
	case "remove":
		switch err := keystore.Delete(provider); {
		case errors.Is(err, keystore.ErrNotFound):
			fmt.Fprintf(stdout, "No %s key in %s.\n", provider, keystore.Name(provider))
		case err != nil:
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		default:
			fmt.Fprintf(stdout, "Removed from %s.\n", keystore.Name(provider))
		}
	case "status":
		where := "none"
		switch _, err := keystore.Get(provider); {
		case err == nil:
			where = "stored in " + keystore.Name(provider)
		case !errors.Is(err, keystore.ErrNotFound):
			where = "credential store: " + err.Error()
		}
		for _, v := range llm.KeyVars(provider) {
			if os.Getenv(v) != "" {
				where += "; " + v + " is set (used first)"
			}
		}
		fmt.Fprintf(stdout, "%-10s %s\n", provider, where)
	default:
		return usage()
	}
	return 0
}
