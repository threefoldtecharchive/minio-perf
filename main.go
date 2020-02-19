package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
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

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{
		TimeFormat: time.RFC3339,
		Out:        os.Stdout,
	})

}

func mkUser(app *App) error {
	_, err := app.TFUser("id")
	return err
}

func mkNetwork(ctx context.Context, app *App) error {
	const (
		schema = "network.json"
	)

	err := R(schema).Redirect(app.TFUser(
		"gen",
		"network",
		"create",
		"--name", NetworkName,
		"--cidr", Network,
	))

	if err != nil {
		return errors.Wrap(err, "failed to create network schema")
	}

	// add node
	_, err = app.TFUser(
		"gen",
		"--schema", schema,
		"network",
		"add-node",
		"--node", app.Node,
		"--subnet", Subnet,
	)

	if err != nil {
		return errors.Wrap(err, "failed to add node to network")
	}

	// add access
	err = R("/etc/wireguard/miniotest.conf").Redirect(
		app.TFUser(
			"gen",
			"--schema", schema,
			"network",
			"add-access",
			"--node", app.Node,
			"--subnet", "10.1.0.0/24",
			"--ip4", //TODO: do we need this ?
		),
	)

	if err != nil {
		return errors.Wrap(err, "failed to add access to network")
	}

	resource, err := app.Provision(schema, app.Node)

	if err != nil {
		return errors.Wrap(err, "failed to provision network")
	}

	AddDestructor(ctx, func() {
		log.Debug().Str("resource", resource.ID()).Msg("de-provision network")
		if err := app.DeProvision(resource); err != nil {
			log.Error().Err(err).Msg("failed to de-provision network")
		}
	})

	wg, err := cmd("wg-quick", "up", "miniotest")
	if err != nil {
		return errors.Wrapf(err, "failed to bring wg interface up: %s", wg)
	}

	AddDestructor(ctx, func() {
		if _, err := cmd("wg-quick", "down", "miniotest"); err != nil {
			log.Error().Err(err).Msg("failed to clean up wireguard setup")
		}
	})

	return nil
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
func (zs ZDBs) Clean(app *App) {
	for _, zdb := range zs {
		if err := app.DeProvision(zdb.Resource()); err != nil {
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

func mkZdb(ctx context.Context, app *App) (zdbs ZDBs, err error) {
	cl := MustExplorer(ExplorerURL)
	nodes, err := cl.Nodes(IsUp(), WithSRU(10))

	if err != nil {
		return nil, errors.Wrap(err, "failed to list nodes from explorer")
	}
	if len(nodes) < app.ZDBs {
		return nil, fmt.Errorf("number of online nodes is not sufficient (required at least: %d", app.ZDBs)
	}

	const (
		password = "password"
	)

	err = R("zdb.json").Redirect(
		app.TFUser(
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
			app.DeProvision(resources...)
		}
	}()

	for i := 0; i < app.ZDBs; i++ {
		var resource Resource
		resource, err = app.Provision("zdb.json", nodes[i].ID)
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

	return zdbs, nil
}

func mkMinio(ctx context.Context, app *App, zdbs ZDBs) error {
	const (
		flist = "https://hub.grid.tf/azmy.3bot/minio.flist"
	)

	data, parity, err := app.Distribution()
	if err != nil {
		return err
	}

	R("container.json").Redirect(
		app.TFUser(
			"generate",
			"container",
			"--flist", flist,
			"--entrypoint", "/bin/entrypoint",
			"--envs", fmt.Sprintf("SHARDS=%s", zdbs.String()),
			"--envs", fmt.Sprintf("DATA=%d", data),
			"--envs", fmt.Sprintf("PARITY=%d", parity),
			"--envs", "ACCESS_KEY=minio",
			"--envs", "SECRET_KEY=passwordpassword",
			"--cpu", "2",
			"--memory", "4096",
			"--ip", MinioIP,
			"--network", NetworkName,
		),
	)

	resource, err := app.Provision("container.json", app.Node)
	if err != nil {
		return err
	}

	cleanUp := func() {
		if err := app.DeProvision(resource); err != nil {
			log.Error().Err(err).Msg("failed to de-provision minio container")
		}
	}
	AddDestructor(ctx, cleanUp)

	cl := MustExplorer(ExplorerURL)
	_, err = cl.Wait(resource)
	if err != nil {
		cleanUp()
		return err
	}

	return nil
}

func run(app *App) ([]Statistics, error) {
	ctx, cancel := WithDestructor(context.Background())
	defer cancel()

	// - Make use seed file.
	if err := mkUser(app); err != nil {
		return nil, errors.Wrap(err, "failed to create user")
	}

	// - Make network and provision it.
	err := mkNetwork(ctx, app)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create network")
	}

	zdbs, err := mkZdb(ctx, app)
	if err != nil {
		return nil, errors.Wrap(err, "failed to deploy zdbs")
	}

	err = mkMinio(ctx, app, zdbs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to deploy minio")
	}

	return test(app)
}

func test(app *App) ([]Statistics, error) {
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

	if _, err := app.MC("-C", configDir, "mb", bucket); err != nil {
		return nil, err
	}

	var statistics []Statistics
	for _, size := range []int64{10, 100, 1024} {
		stats, err := uploadTest(app, configDir, bucket, size)
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

func uploadTest(app *App, config, bucket string, size int64) (*Statistics, error) {
	hash, name, err := MkTestFile(size)
	if err != nil {
		return nil, err
	}

	log := log.With().Int64("size-mb", size).Str("file", name).Str("hash", hash).Logger()
	log.Info().Msg("uploading file")

	uploadDur, err := app.MC(
		"-C", config,
		"cp", name, bucket,
	)

	if err != nil {
		return nil, errors.Wrapf(err, "failed to upload file: %s", name)
	}

	log.Info().Str("duration", uploadDur.String()).Msg("uploading time")

	downloadName := fmt.Sprintf("%s.download", name)
	downloadDur, err := app.MC(
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

	log.Debug().Int("nodes", len(nodes)).Msg("found public nodes")
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
	rand.Seed(time.Now().Unix())
	var app App

	flag.StringVar(&app.TFUserBin, "tfuser", "", "path to tfuser binary. Default to using $PATH")
	flag.StringVar(&app.MCBin, "mc", "", "path to mc binary. Default to using $PATH")
	flag.StringVar(&app.Node, "node", "", "node to install minio. It must have public interface")
	flag.IntVar(&app.ZDBs, "zdbs", 3, "number of zdb namespaces to deploy")
	flag.StringVar(&app.DataParity, "dist", "2/1", "distribution of data/party bit in format of Data/Parity")
	flag.Parse()

	if len(app.TFUserBin) != 0 {
		app.TFUserBin, _ = filepath.Abs(app.TFUserBin)
	}

	if len(app.MCBin) != 0 {
		app.MCBin, _ = filepath.Abs(app.MCBin)
	}

	if _, _, err := app.Distribution(); err != nil {
		log.Fatal().Err(err).Msg("failed to parse data/parity information")
	}

	var err error
	if len(app.Node) == 0 {
		app.Node, err = findNode()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to find node")
		}
	}

	log.Info().Str("node", app.Node).Msg("using node")

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

	stats, err := run(&app)
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
