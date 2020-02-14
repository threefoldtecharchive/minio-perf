package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	// ProvisionDuration time set for all the test reservation
	ProvisionDuration = "1h"
)

var (
	// TFUser binary
	TFUser string = "tfuser"
)

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{
		TimeFormat: time.RFC3339,
		Out:        os.Stdout,
	})

}

// R is a utility to quickly write output of tfuser command to a file
type R string

// Redirect usually called like Redirect(tfuser(...))
func (o R) Redirect(s string, err error) error {
	if err != nil {
		return err
	}

	return ioutil.WriteFile(string(o), []byte(s), 0644)
}

// D is an alias for func() to be used as clean up method
// usually as `defer d.Defer()`
type D func()

// Defer runs this D object, does nothing if d is nil
func (d D) Defer() {
	if d != nil {
		d()
	}
}

// Then return a new D that when called first calls d, then n
func (d D) Then(n D) D {
	return func() {
		d.Defer()
		n.Defer()
	}
}

func cmd(c string, args ...string) (string, error) {
	out, err := exec.Command(c, args...).Output()
	if err != nil {
		return "", err
	}

	return string(out), nil
}

func tfuser(args ...string) (string, error) {
	return cmd(TFUser, args...)
}

func mkUser() error {
	_, err := tfuser("id")
	return err
}

// Resource represents a resource URI
type Resource string

// ID return resource ID
func (r Resource) ID() string {
	return filepath.Base(string(r))
}

func provision(schema, node string) (Resource, error) {

	// provision network
	out, err := tfuser(
		"provision",
		"--schema", schema,
		"--duration", ProvisionDuration,
		"--seed", "user.seed",
		"--node", node,
	)
	if err != nil {
		return "", errors.Wrapf(err, "failed to provision '%s'", schema)
	}

	const (
		prefix = "Resource: "
	)

	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}

		return Resource(strings.TrimPrefix(line, prefix)), nil
	}

	return "", fmt.Errorf("failed to extract resource URI from reservation (%s):\n%s", schema, out)
}

func deProvision(r Resource) error {
	_, err := tfuser(
		"delete",
		"--id", r.ID(),
	)
	return err
}

func mkNetwork(node string) (D, error) {
	const (
		schema = "network.json"
	)

	err := R(schema).Redirect(tfuser(
		"gen",
		"network",
		"create",
		"--name", "minio",
		"--cidr", "172.10.0.0/16",
	))

	if err != nil {
		return nil, errors.Wrap(err, "failed to create network schema")
	}

	// add node
	_, err = tfuser(
		"gen",
		"--schema", schema,
		"network",
		"add-node",
		"--node", node,
		"--subnet", "172.10.1.0/24",
	)

	if err != nil {
		return nil, errors.Wrap(err, "failed to add node to network")
	}

	// add access
	err = R("wg.conf").Redirect(
		tfuser(
			"gen",
			"--schema", schema,
			"network",
			"add-access",
			"--node", node,
			"--subnet", "10.1.0.0/24",
			"--ip4", //TODO: do we need this ?
		),
	)

	if err != nil {
		return nil, errors.Wrap(err, "failed to add access to network")
	}

	resource, err := provision(schema, node)

	if err != nil {
		return nil, errors.Wrap(err, "failed to provision network")
	}

	d := D(func() {
		log.Debug().Msg("de-provision network")
		if err := deProvision(resource); err != nil {
			log.Error().Err(err).Msg("failed to de-provision network")
		}
		//TODO de-provision network
	})

	wg, err := cmd("wg-quick", "up", "wg.conf")
	fmt.Println(wg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to bring wg interface up: %s", wg)
	}

	d = d.Then(func() {
		if _, err := cmd("wq-quick", "down", "wg.conf"); err != nil {
			log.Error().Err(err).Msg("failed to clean up wireguard setup")
		}
	})

	return d, nil
}

func run(node string) error {
	// - Make use seed file.
	if err := mkUser(); err != nil {
		return errors.Wrap(err, "failed to create user")
	}

	// - Make network and provision it.
	dNetwork, err := mkNetwork(node)
	if err != nil {
		return errors.Wrap(err, "failed to create network")
	}

	defer dNetwork.Defer()

	return nil
}

func main() {
	var (
		bin  string
		node string
	)

	flag.StringVar(&bin, "tfuser", "", "path to tfuser binary. Default to using $PATH")
	flag.StringVar(&node, "node", "", "node to install minio. It must have public interface")

	flag.Parse()

	if len(bin) != 0 {
		TFUser, _ = filepath.Abs(bin)
	}

	if len(node) == 0 {
		log.Fatal().Msg("-node is required")
	}

	root, err := ioutil.TempDir("", "minio-perf")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get a temporary root directory")
	}

	//TODO: uncomment after debugging
	// defer func() {
	// 	if err := os.RemoveAll(root); err != nil {
	// 		log.Error().Err(err).Str("root", root).Msg("failed to clean up test root")
	// 	}
	// }()

	if cwd, err := os.Getwd(); err == nil {
		defer os.Chdir(cwd)
	}

	log.Info().Str("root", root).Msg("chainging into test root")

	if err := os.Chdir(root); err != nil {
		log.Fatal().Str("root", root).Err(err).Msg("failed to change directory")
	}

	if err := run(node); err != nil {
		log.Fatal().Err(err).Msg("failed to execute tests")
	}
	/*
		TODO:
		 - Make a new user
		 - We first to need to know which node we need to test. do the following
		  = Deploy a network on that remote node
		  = Apply wireguard configuration
		  = Start wireguard
		   * make sure wireguard config are deleted afterwards
		 - deploy 3 zdbs on random? nodes (or may be specified via command line)
		 - deploy a storage volume
		 - deploy container

		 - run minio upload/download and measure speed. (may be run minio mint)
		 - clean up
	*/
}
