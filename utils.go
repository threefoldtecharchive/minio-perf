package main

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// R is a utility to quickly write output of tfuser command to a file
type R string

// Redirect usually called like Redirect(tfuser(...))
func (o R) Redirect(s string, err error) error {
	if err != nil {
		return err
	}

	return ioutil.WriteFile(string(o), []byte(s), 0644)
}

func cmd(c string, args ...string) (string, error) {
	out, err := exec.Command(c, args...).Output()
	if err != nil {
		return string(out), err
	}

	return string(out), nil
}

type App struct {
	Node       string
	ZDBs       int
	DataParity string
	TFUserBin  string
	MCBin      string
}

func (a *App) Distribution() (d int, p int, err error) {
	if _, err := fmt.Sscanf(a.DataParity, "%d/%d", &d, &p); err != nil {
		return d, p, errors.Wrap(err, "invalid data parity format expecting D/D")
	}
	if d == 0 || p == 0 {
		return d, p, fmt.Errorf("data nor parity can be zero")
	}
	return
}

func (a *App) TFUser(args ...string) (string, error) {
	return cmd(a.TFUserBin, args...)
}

func (a *App) MC(args ...string) (time.Duration, error) {
	notify := func(err error, d time.Duration) {
		log.Info().Err(err).Dur("duration", d).Msg(strings.Join(args, " "))
	}

	exp := backoff.NewExponentialBackOff()
	exp.MaxInterval = 10 * time.Second
	boff := backoff.WithMaxRetries(exp, 10)
	var d time.Duration
	err := backoff.RetryNotify(func() error {
		cmd := exec.Command(a.MCBin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		t := time.Now()

		err := cmd.Run()
		d = time.Since(t)
		return err

	}, boff, notify)

	return d, err
}

func (a *App) Provision(schema, node string) (Resource, error) {

	// provision network
	out, err := a.TFUser(
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

func (a *App) DeProvision(rs ...Resource) error {
	for _, r := range rs {
		_, err := a.TFUser(
			"delete",
			"--id", r.ID(),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func MkTestFile(sizeMB int64) (hash string, path string, err error) {
	file, err := ioutil.TempFile(".", fmt.Sprintf("random-%dMB-", sizeMB))
	if err != nil {
		return hash, path, err
	}
	hasher := md5.New()
	output := io.MultiWriter(file, hasher)
	defer file.Close()
	_, err = io.CopyN(output, rand.Reader, sizeMB*1024*1024)
	return fmt.Sprintf("%x", hasher.Sum(nil)), file.Name(), err
}

func md5sum(name string) (string, error) {
	out, err := cmd("md5sum", name)
	if err != nil {
		return "", err
	}

	fields := strings.Fields(strings.TrimSpace(out))
	return fields[0], nil
}
