package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	seer "github.com/n0sn0de/tenderduty-nos/seer"
	"golang.org/x/term"
)

const (
	binaryName       = "nosnode-seer"
	defaultStateFile = ".nosnode-seer-state.json"
	legacyStateFile  = ".tenderduty-state.json"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

//go:embed example-config.yml
var defaultConfig []byte

type options struct {
	configFile           string
	chainConfigDirectory string
	stateFile            string
	encryptedFile        string
	password             string
	dumpConfig           bool
	encryptConfig        bool
	decryptConfig        bool
	showVersion          bool
	usingLegacyState     bool
}

func parseOptions(args []string, stderr io.Writer, getenv func(string) string, fileExists func(string) bool) (options, error) {
	var opts options
	flags := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "%s — %s | %s\n\nUsage: %s [options]\n\n", seer.ProductName, seer.BrandName, "Cosmos validator monitoring", binaryName)
		flags.PrintDefaults()
	}
	flags.StringVar(&opts.configFile, "f", "config.yml", "configuration file (or CONFIG environment variable)")
	flags.StringVar(&opts.encryptedFile, "encrypted-config", "config.yml.asc", "encrypted configuration used with -encrypt or -decrypt")
	flags.StringVar(&opts.password, "password", "", "configuration encryption password (or PASSWORD environment variable; prompt if unset)")
	flags.StringVar(&opts.stateFile, "state", defaultStateFile, "state file used between restarts")
	flags.StringVar(&opts.chainConfigDirectory, "cc", "chains.d", "directory containing additional chain YAML files")
	flags.BoolVar(&opts.dumpConfig, "example-config", false, "print an example config.yml and exit")
	flags.BoolVar(&opts.encryptConfig, "encrypt", false, "encrypt -f into -encrypted-config")
	flags.BoolVar(&opts.decryptConfig, "decrypt", false, "decrypt -encrypted-config into -f")
	flags.BoolVar(&opts.showVersion, "version", false, "print version information and exit")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	stateExplicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "state" {
			stateExplicit = true
		}
	})
	if !stateExplicit && !fileExists(defaultStateFile) && fileExists(legacyStateFile) {
		opts.stateFile = legacyStateFile
		opts.usingLegacyState = true
	}
	if opts.configFile == "config.yml" && getenv("CONFIG") != "" {
		opts.configFile = getenv("CONFIG")
	}
	if getenv("PASSWORD") != "" {
		opts.password = getenv("PASSWORD")
	}
	return opts, nil
}

func runApp(args []string, stdout, stderr io.Writer, getenv func(string) string, fileExists func(string) bool) int {
	opts, err := parseOptions(args, stderr, getenv, fileExists)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if opts.showVersion {
		_, _ = fmt.Fprintf(stdout, "%s (%s) %s; commit=%s; built=%s\n", seer.ProductName, seer.BrandName, version, commit, date)
		return 0
	}
	if opts.dumpConfig {
		_, _ = fmt.Fprint(stdout, string(defaultConfig))
		return 0
	}
	if opts.usingLegacyState {
		_, _ = fmt.Fprintf(stderr, "%s: using legacy state file %s; pass -state %s after migration\n", seer.BrandName, legacyStateFile, defaultStateFile)
	}

	if opts.encryptConfig || opts.decryptConfig {
		if opts.password == "" {
			_, _ = fmt.Fprint(stdout, "Please enter the encryption password: ")
			pass, readErr := term.ReadPassword(int(syscall.Stdin))
			if readErr != nil {
				_, _ = fmt.Fprintf(stderr, "could not read password: %v\n", readErr)
				return 1
			}
			_, _ = fmt.Fprintln(stdout)
			opts.password = string(pass)
			for i := range pass {
				pass[i] = 0
			}
		}
		decrypt := opts.decryptConfig
		if err := seer.EncryptedConfig(opts.configFile, opts.encryptedFile, opts.password, decrypt); err != nil {
			_, _ = fmt.Fprintf(stderr, "configuration encryption operation failed: %v\n", err)
			return 1
		}
		return 0
	}

	if err := seer.Run(opts.configFile, opts.stateFile, opts.chainConfigDirectory, &opts.password); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s stopped: %q\n", seer.ProductName, err)
		return 1
	}
	return 0
}

func fileExists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

func main() {
	os.Exit(runApp(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, fileExists))
}
