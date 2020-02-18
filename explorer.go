package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// Resource represents a resource URI
type Resource string

// ID return resource ID
func (r Resource) ID() string {
	return filepath.Base(string(r))
}

// PublicConfig struct
type PublicConfig struct{}

// Node represents a grid node
type Node struct {
	ID        string `json:"node_id"`
	Updated   int64  `json:"updated"`
	Resources struct {
		SRU int `json:"sru"`
	} `json:"total_resources"`
	PublicConfig *PublicConfig `json:"public_config,omitempty"`
}

// Result struct
type Result struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	State string          `json:"state"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// Client for explorer
type Client struct {
	base *url.URL
}

// NewExplorer client
func NewExplorer(base string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	return &Client{u}, nil
}

// MustExplorer panics if can't create a client
func MustExplorer(base string) *Client {
	client, err := NewExplorer(base)
	if err != nil {
		panic(err)
	}
	return client
}

func (c *Client) join(elem ...string) *url.URL {
	u := *c.base
	u.Path = filepath.Join(u.Path, filepath.Join(elem...))
	return &u
}

// Nodes return all nodes in explorer
func (c *Client) Nodes(filter ...Filter) ([]Node, error) {

	response, err := http.Get(c.join("nodes").String())
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()
	defer ioutil.ReadAll(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list node: %s", response.Status)
	}

	var nodes []Node
	dec := json.NewDecoder(response.Body)

	if err := dec.Decode(&nodes); err != nil {
		return nil, errors.Wrap(err, "failed to decode nodes result")
	}

	if len(filter) > 0 {
		var filtered []Node
	skip:
		for _, node := range nodes {
			for _, f := range filter {
				if !f(&node) {
					continue skip
				}
			}
			filtered = append(filtered, node)
		}

		return filtered, nil
	}

	return nodes, nil
}

// Result result
func (c *Client) Result(r Resource) (Result, error) {
	response, err := http.Get(c.join("reservations", r.ID()).String())
	if err != nil {
		return Result{}, err
	}

	defer response.Body.Close()
	defer ioutil.ReadAll(response.Body)

	var data struct {
		Result Result `json:"result"`
	}

	dec := json.NewDecoder(response.Body)

	if err := dec.Decode(&data); err != nil {
		return Result{}, errors.Wrap(err, "failed to decode 'result' result")
	}

	return data.Result, nil
}

// Wait for all results to finish
func (c *Client) Wait(rs ...Resource) ([]Result, error) {
	var results []Result
	for _, r := range rs {
		times := 0
		for {
			log.Debug().Str("reservation", r.ID()).Msg("waiting for reservation")
			res, err := c.Result(r)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to get reserrvation: %s", r)
			}
			log.Debug().Str("reservation", r.ID()).Str("state", res.State).Msg("reservation status")
			if res.State == "ok" || res.State == "error" {
				results = append(results, res)
				break
			}
			<-time.After(1 * time.Second)
			times++
			if times >= 20 { // 20 seconds max
				return nil, fmt.Errorf("failed to wait for reservation '%s', timeout exceeded", r)
			}
		}
	}

	return results, nil
}

// Filter function
type Filter func(n *Node) bool

// IsUp filter
func IsUp() Filter {
	return func(n *Node) bool {
		now := time.Now()
		return (now.Unix() - n.Updated) < 10*60
	}
}

//IsPublic return nodes that has public interface
func IsPublic() Filter {
	return func(n *Node) bool {
		return n.PublicConfig != nil
	}
}

// WithSRU return nodes that can provide this sru
func WithSRU(sru int) Filter {
	return func(n *Node) bool {
		return n.Resources.SRU > sru
	}
}

// Shuffle nodes
func Shuffle(nodes []Node) {
	rand.Shuffle(len(nodes), func(i, j int) {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	})
}
