package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
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
	//ExplorerURL endpoint
	ExplorerURL = "https://explorer.devnet.grid.tf/"

	// NetworkName network name
	NetworkName = "minio"
	//Network full range
	Network = "172.10.0.0/16"
	// Subnet network subnet
	Subnet = "172.10.1.0/24"
	// MinioIP value
	MinioIP = "172.10.1.100"
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

func mkUser() error {
	_, err := tfuser("id")
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
		"--name", NetworkName,
		"--cidr", Network,
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
		"--subnet", Subnet,
	)

	if err != nil {
		return nil, errors.Wrap(err, "failed to add node to network")
	}

	// add access
	err = R("/etc/wireguard/miniotest.conf").Redirect(
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
		log.Debug().Str("resource", resource.ID()).Msg("de-provision network")
		if err := deProvision(resource); err != nil {
			log.Error().Err(err).Msg("failed to de-provision network")
		}
	})

	wg, err := cmd("wg-quick", "up", "miniotest")
	fmt.Println(wg)
	if err != nil {
		return d, errors.Wrapf(err, "failed to bring wg interface up: %s", wg)
	}

	d = d.Then(func() {
		if _, err := cmd("wg-quick", "down", "miniotest"); err != nil {
			log.Error().Err(err).Msg("failed to clean up wireguard setup")
		}
	})

	return d, nil
}

// ZDB result object
type ZDB struct {
	Namespace string `json:"Namespace"`
	IP        string `json:"IP"`
	Port      int    `json:"Port"`
	Password  string `json:"-"`
}

// Resource return the resource associated with this zdb
func (z *ZDB) Resource() Resource {
	return Resource(filepath.Join("reservations", z.Namespace))
}

func (z *ZDB) String() string {
	return fmt.Sprintf("%s:%s@[%s]:%d", z.Namespace, z.Password, z.IP, z.Port)
}

// ZDBs alias for []ZDB
type ZDBs []ZDB

// Clean clean zdbs
func (zs ZDBs) Clean() {
	for _, zdb := range zs {
		if err := deProvision(zdb.Resource()); err != nil {
			log.Error().Err(err).Str("reservation", zdb.Resource().ID()).Msg("failed to delete reservation")
		}
	}
}

func (zs ZDBs) String() string {
	var buf strings.Builder
	for _, z := range zs {
		if buf.Len() != 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(z.String())
	}

	return buf.String()
}

func mkZdb(n int) (zdbs ZDBs, err error) {
	cl := MustExplorer(ExplorerURL)
	nodes, err := cl.Nodes(IsUp(), WithSRU(10))

	if err != nil {
		return nil, errors.Wrap(err, "failed to list nodes from explorer")
	}
	if len(nodes) < n {
		return nil, fmt.Errorf("number of online nodes is not sufficient (required at least: %d", n)
	}

	const (
		password = "password"
	)

	err = R("zdb.json").Redirect(
		tfuser(
			"generate",
			"storage",
			"zdb",
			"--size", "10",
			"--type", "SSD",
			"--mode", "seq",
			"--password", password,
		),
	)

	if err != nil {
		return nil, err
	}

	Shuffle(nodes)

	var resources []Resource

	defer func() {
		if err != nil {
			deProvision(resources...)
		}
	}()

	for i := 0; i < n; i++ {
		var resource Resource
		resource, err = provision("zdb.json", nodes[i].ID)
		if err != nil {
			return nil, err
		}

		resources = append(resources, resource)
	}

	results, err := cl.Wait(resources...)
	if err != nil {
		return nil, err
	}

	for _, result := range results {
		if result.State != "ok" {
			err = fmt.Errorf("reservation '%s' has status: %s", result.ID, result.State)
			return
		}

		var zdb ZDB
		if err = json.Unmarshal(result.Data, &zdb); err != nil {
			return nil, err
		}
		zdb.Password = password
		zdbs = append(zdbs, zdb)
	}

	// TODO: wait for resources to finish deployment
	return zdbs, nil
}

func mkMinio(node string, zdbs ZDBs) (D, error) {
	const (
		flist = "https://hub.grid.tf/azmy.3bot/minio.flist"
	)

	R("container.json").Redirect(
		tfuser(
			"generate",
			"container",
			"--flist", flist,
			"--entrypoint", "/bin/entrypoint",
			"--envs", fmt.Sprintf("SHARDS=%s", zdbs.String()),
			"--envs", "DATA=2",
			"--envs", "PARITY=1",
			"--envs", "ACCESS_KEY=minio",
			"--envs", "SECRET_KEY=passwordpassword",
			"--cpu", "2",
			"--memory", "4096",
			"--ip", MinioIP,
		),
	)

	resource, err := provision("container.json", node)
	if err != nil {
		return nil, err
	}

	d := D(func() {
		if err := deProvision(resource); err != nil {
			log.Error().Err(err).Msg("failed to de-provision minio container")
		}
	})

	cl := MustExplorer(ExplorerURL)
	_, err = cl.Wait(resource)
	if err != nil {
		d.Defer()
		return nil, err
	}

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

	zdbs, err := mkZdb(3)
	if err != nil {
		return errors.Wrap(err, "failed to deploy zdbs")
	}

	defer zdbs.Clean()

	for _, zdb := range zdbs {
		fmt.Println(zdb.String())
	}

	dMinio, err := mkMinio(node, zdbs)
	if err != nil {
		return errors.Wrap(err, "failed to deploy minio")
	}
	defer dMinio.Defer()

	fmt.Println("exit with no clean up")
	os.Exit(0)
	// Now deploy the minio container
	return nil
}

func test() {
	const (
		flist = "https://hub.grid.tf/azmy.3bot/minio.flist"
	)

	zdbs := ZDBs{
		{Namespace: "ns1", IP: "192.168.1.1", Port: 1234, Password: "password"},
		{Namespace: "ns2", IP: "192.168.1.2", Port: 5467, Password: "password"},
	}
	TFUser = "/home/azmy/tmp/tfuser"

	err := R("container.json").Redirect(
		tfuser(
			"generate",
			"container",
			"--flist", flist,
			"--entrypoint", "/bin/entrypoint",
			"--envs", fmt.Sprintf("SHARDS=%s", zdbs.String()),
			"--envs", "DATA=2",
			"--envs", "PARITY=1",
			"--envs", "ACCESS_KEY=minio",
			"--envs", "SECRET_KEY=passwordpassword",
			"--cpu", "2",
			"--memory", "4096",
		),
	)

	if err != nil {
		panic(err)
	}
	// cl, err := NewExplorer("https://explorer.devnet.grid.tf/")
	// if err != nil {
	// 	panic(err)
	// }

	// nodes, err := cl.Nodes(IsUp())
	// if err != nil {
	// 	panic(err)
	// }
	// enc := json.NewEncoder(os.Stdout)
	// enc.SetIndent("", "  ")
	// enc.Encode(nodes)

	// fmt.Println("Nodes:", len(nodes))
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
