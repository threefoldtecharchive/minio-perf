package main

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
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
		return string(out), err
	}

	return string(out), nil
}

func tfuser(args ...string) (string, error) {
	return cmd(TFUserBin, args...)
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

func deProvision(rs ...Resource) error {
	for _, r := range rs {
		_, err := tfuser(
			"delete",
			"--id", r.ID(),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func mkTestFile(sizeMB int64) (hash string, path string, err error) {
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
