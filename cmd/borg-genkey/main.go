package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/config"
)

const (
	defaultSecretSuffix    = "-auth"
	defaultConfigMapSuffix = "-config"
)

type options struct {
	username        string
	namespace       string
	release         string
	keyName         string
	authPrefix      string
	secretSuffix    string
	configMapSuffix string
}

type configInfo struct {
	authKeyName string
	authPrefix  string
}

func main() {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	restConfig, err := loadKubeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[generate_key] cannot load kubeconfig: %v\n", err)
		os.Exit(1)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[generate_key] cannot create Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	if err := run(context.Background(), client, opts, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("borg-genkey", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.namespace, "namespace", "", "Kubernetes namespace")
	flags.StringVar(&opts.namespace, "n", "", "Kubernetes namespace")
	flags.StringVar(&opts.release, "release", "", "Helm release name")
	flags.StringVar(&opts.release, "r", "", "Helm release name")
	flags.StringVar(&opts.keyName, "key-name", "", "Secret data key (overrides ConfigMap)")
	flags.StringVar(&opts.keyName, "k", "", "Secret data key (overrides ConfigMap)")
	flags.StringVar(&opts.authPrefix, "auth-prefix", "", "Prefix for the token plaintext (overrides ConfigMap)")
	flags.StringVar(&opts.secretSuffix, "secret-suffix", defaultSecretSuffix, "Suffix appended to <release> for the Secret")
	flags.StringVar(&opts.configMapSuffix, "configmap-suffix", defaultConfigMapSuffix, "Suffix appended to <release> for the ConfigMap")

	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return options{}, err
	}
	if err := flags.Parse(normalizedArgs); err != nil {
		return options{}, err
	}
	if flags.NArg() != 1 {
		return options{}, errors.New("usage: borg-genkey <username> --namespace <namespace> --release <release>")
	}
	opts.username = flags.Arg(0)
	if opts.namespace == "" {
		return options{}, errors.New("--namespace is required")
	}
	if opts.release == "" {
		return options{}, errors.New("--release is required")
	}
	return opts, nil
}

func normalizeArgs(args []string) ([]string, error) {
	var username string
	normalized := make([]string, 0, len(args))

	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]
		if arg == "--" {
			if len(args[idx+1:]) != 1 || username != "" {
				return nil, errors.New("usage: borg-genkey <username> --namespace <namespace> --release <release>")
			}
			username = args[idx+1]
			break
		}

		if strings.HasPrefix(arg, "-") && arg != "-" {
			normalized = append(normalized, arg)
			if strings.Contains(arg, "=") {
				continue
			}
			if flagRequiresValue(arg) {
				if idx+1 >= len(args) {
					return nil, fmt.Errorf("flag needs an argument: %s", arg)
				}
				idx++
				normalized = append(normalized, args[idx])
			}
			continue
		}

		if username != "" {
			return nil, errors.New("usage: borg-genkey <username> --namespace <namespace> --release <release>")
		}
		username = arg
	}

	if username != "" {
		normalized = append(normalized, username)
	}
	return normalized, nil
}

func flagRequiresValue(arg string) bool {
	switch arg {
	case "-namespace", "--namespace", "-n",
		"-release", "--release", "-r",
		"-key-name", "--key-name", "-k",
		"-auth-prefix", "--auth-prefix",
		"-secret-suffix", "--secret-suffix",
		"-configmap-suffix", "--configmap-suffix":
		return true
	default:
		return false
	}
}

func loadKubeConfig() (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func run(ctx context.Context, client kubernetes.Interface, opts options, stdout io.Writer, stderr io.Writer) error {
	info, err := getConfigInfo(ctx, client, opts.namespace, opts.release, opts.configMapSuffix, stderr)
	if err != nil {
		return err
	}

	keyName := opts.keyName
	if keyName == "" {
		keyName = info.authKeyName
	}

	authPrefix := opts.authPrefix
	if authPrefix == "" {
		authPrefix = info.authPrefix
	}
	if authPrefix == "" {
		authPrefix = config.DefaultAuthPrefix
	}

	key, err := getKey(ctx, client, opts.namespace, opts.release, opts.secretSuffix, keyName, stderr)
	if err != nil {
		return err
	}

	token, err := auth.MintToken(opts.username, key, authPrefix)
	if err != nil {
		return fmt.Errorf("[generate_key] cannot mint token: %w", err)
	}
	_, err = fmt.Fprintln(stdout, token)
	return err
}

func getConfigInfo(ctx context.Context, client kubernetes.Interface, namespace string, release string, configMapSuffix string, stderr io.Writer) (configInfo, error) {
	name := release + configMapSuffix
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if _, ok := err.(*apierrors.StatusError); ok {
			return configInfo{}, nil
		}
		return configInfo{}, fmt.Errorf("[generate_key] cannot read ConfigMap %q in namespace %q: %w", name, namespace, err)
	}

	rawYAML := ""
	if cm.Data != nil {
		rawYAML = cm.Data["config.yaml"]
	}
	if rawYAML == "" {
		return configInfo{}, nil
	}

	var parsed struct {
		Borg struct {
			AuthKeyFromEnv string `yaml:"auth_key_from_env"`
			AuthPrefix     string `yaml:"auth_prefix"`
		} `yaml:"borg"`
	}
	if err := yaml.Unmarshal([]byte(rawYAML), &parsed); err != nil {
		fmt.Fprintf(stderr, "[generate_key] WARNING: cannot parse %s/config.yaml: %v\n", name, err)
		return configInfo{}, nil
	}

	return configInfo{
		authKeyName: parsed.Borg.AuthKeyFromEnv,
		authPrefix:  parsed.Borg.AuthPrefix,
	}, nil
}

func getKey(ctx context.Context, client kubernetes.Interface, namespace string, release string, secretSuffix string, keyName string, stderr io.Writer) ([]byte, error) {
	secretName := release + secretSuffix
	secret, err := client.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if statusErr, ok := err.(*apierrors.StatusError); ok {
			return nil, fmt.Errorf("[generate_key] cannot read Secret %q in namespace %q: %s", secretName, namespace, statusErr.ErrStatus.Reason)
		}
		return nil, fmt.Errorf("[generate_key] cannot read Secret %q in namespace %q: %w", secretName, namespace, err)
	}

	keyName, secretData, err := pickSecretData(secret, secretName, keyName, stderr)
	if err != nil {
		return nil, err
	}
	key, err := auth.DecodeSecretKey(secretData)
	if err != nil {
		return nil, fmt.Errorf("[generate_key] secret data %q did not contain a raw AES key or printable URL-safe auth key: %w", keyName, err)
	}
	return key, nil
}

func pickSecretData(secret *corev1.Secret, secretName string, keyName string, stderr io.Writer) (string, []byte, error) {
	if len(secret.Data) == 0 {
		return "", nil, fmt.Errorf("[generate_key] Secret %q has no data fields", secretName)
	}

	if keyName != "" {
		secretData, ok := secret.Data[keyName]
		if !ok {
			return "", nil, fmt.Errorf("[generate_key] key %q not found in Secret %q", keyName, secretName)
		}
		return keyName, secretData, nil
	}

	keys := make([]string, 0, len(secret.Data))
	for key := range secret.Data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	keyName = keys[0]
	fmt.Fprintf(stderr, "[generate_key] using key %q from Secret %q\n", keyName, secretName)
	return keyName, secret.Data[keyName], nil
}
