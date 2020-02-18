package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v3"
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
	// TFUserBin binary
	TFUserBin string = "tfuser"
	// MCBin binary
	MCBin string = "mc"
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
			"--network", NetworkName,
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

func run(node string) ([]Statistics, error) {
	// - Make use seed file.
	if err := mkUser(); err != nil {
		return nil, errors.Wrap(err, "failed to create user")
	}

	// - Make network and provision it.
	dNetwork, err := mkNetwork(node)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create network")
	}

	defer dNetwork.Defer()

	zdbs, err := mkZdb(3)
	if err != nil {
		return nil, errors.Wrap(err, "failed to deploy zdbs")
	}

	defer zdbs.Clean()

	dMinio, err := mkMinio(node, zdbs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to deploy minio")
	}

	defer dMinio.Defer()

	return test()
}

func mc(args ...string) (time.Duration, error) {
	notify := func(err error, d time.Duration) {
		log.Info().Err(err).Dur("duration", d).Msg(strings.Join(args, " "))
	}

	exp := backoff.NewExponentialBackOff()
	exp.MaxInterval = 10 * time.Second
	boff := backoff.WithMaxRetries(exp, 10)
	var d time.Duration
	err := backoff.RetryNotify(func() error {
		cmd := exec.Command(MCBin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		t := time.Now()

		err := cmd.Run()
		d = time.Since(t)
		return err

	}, boff, notify)

	return d, err
}

func test() ([]Statistics, error) {
	const (
		configDir = "mc"
	)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	config := `{
	"version": "9",
	"hosts": {
		"test": {
			"url": "http://%s:9000",
			"accessKey": "minio",
			"secretKey": "passwordpassword",
			"api": "s3v4",
			"lookup": "auto"
		}
	}
}
`

	config = fmt.Sprintf(config, MinioIP)

	if err := ioutil.WriteFile("mc/config.json", []byte(config), 0644); err != nil {
		return nil, err
	}

	const (
		bucket = "test/bucket"
	)

	if _, err := mc("-C", configDir, "mb", bucket); err != nil {
		return nil, err
	}

	var statistics []Statistics
	for _, size := range []int64{10, 100, 1024} {
		stats, err := uploadTest(configDir, bucket, size)
		if err != nil {
			log.Error().Err(err).Msgf("error while testing %dmb file upload", size)
		}

		statistics = append(statistics, *stats)
	}

	return statistics, nil
}

type Statistics struct {
	HashMatch bool          `json:"hash-match"`
	Size      int64         `json:"size-mb"`
	Upload    time.Duration `json:"upload-ns"`
	Download  time.Duration `json:"download-ns"`
}

func uploadTest(config, bucket string, size int64) (*Statistics, error) {
	hash, name, err := mkTestFile(size)
	if err != nil {
		return nil, err
	}

	log := log.With().Int64("size-mb", size).Str("file", name).Str("hash", hash).Logger()
	log.Info().Msg("uploading file")

	uploadDur, err := mc(
		"-C", config,
		"cp", name, bucket,
	)

	if err != nil {
		return nil, errors.Wrapf(err, "failed to upload file: %s", name)
	}

	log.Info().Str("duration", uploadDur.String()).Msg("uploading time")

	downloadName := fmt.Sprintf("%s.download", name)
	downloadDur, err := mc(
		"-C", config,
		"cp", filepath.Join(bucket, name), downloadName,
	)

	if err != nil {
		return nil, errors.Wrapf(err, "failed to download file: %s", name)
	}

	log.Info().Str("duration", downloadDur.String()).Msg("downloading time")

	downloadHash, err := md5sum(downloadName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to md5sum of file: %s", downloadName)
	}

	if downloadHash != hash {
		log.Warn().Str("destination", downloadName).Str("download-hash", downloadHash).Msg("hash does not match")
	} else {
		log.Info().Str("destination", downloadName).Msg("hash matches")
	}

	return &Statistics{
		HashMatch: hash == downloadHash,
		Size:      size,
		Upload:    uploadDur,
		Download:  downloadDur,
	}, nil
}

func findNode() (string, error) {
	cl := MustExplorer(ExplorerURL)
	nodes, err := cl.Nodes(IsUp(), IsPublic())
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("no public nodes found")
	}
	Shuffle(nodes)
	return nodes[0].ID, nil
}

func main() {
	/*
		Process:
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
	var (
		tfBin string
		mcBin string
		node  string
	)

	flag.StringVar(&tfBin, "tfuser", "", "path to tfuser binary. Default to using $PATH")
	flag.StringVar(&mcBin, "mc", "", "path to mc binary. Default to using $PATH")
	flag.StringVar(&node, "node", "", "node to install minio. It must have public interface")

	flag.Parse()

	if len(tfBin) != 0 {
		TFUserBin, _ = filepath.Abs(tfBin)
	}

	if len(mcBin) != 0 {
		MCBin, _ = filepath.Abs(mcBin)
	}

	var err error
	if len(node) == 0 {
		node, err = findNode()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to find node")
		}
	}

	log.Info().Str("node", node).Msg("using node")

	root, err := ioutil.TempDir("", "minio-perf")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get a temporary root directory")
	}

	defer func() {
		if err := os.RemoveAll(root); err != nil {
			log.Error().Err(err).Str("root", root).Msg("failed to clean up test root")
		}
	}()

	cwd, err := os.Getwd()
	if err != nil {
		log.Error().Err(err).Msg("failed to get cwd of the process")
	}

	log.Info().Str("root", root).Msg("chainging into test root")

	if err := os.Chdir(root); err != nil {
		log.Fatal().Str("root", root).Err(err).Msg("failed to change directory")
	}

	stats, err := run(node)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to execute tests")
	}

	os.Chdir(cwd)
	output, err := os.Create("statistics.json")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create statistics file")
	}

	defer output.Close()
	enc := json.NewEncoder(output)
	for _, st := range stats {
		enc.Encode(st)
	}
}
