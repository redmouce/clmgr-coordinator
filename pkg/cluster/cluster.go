package cluster

import (
	"encoding/json"
	"errors"
	"github.com/coreos/etcd/clientv3"
	"github.com/google/logger"
	. "myproj.com/clmgr-coordinator/pkg/common"
	"myproj.com/clmgr-coordinator/pkg/db"
	"myproj.com/clmgr-coordinator/pkg/node"
	"myproj.com/clmgr-coordinator/pkg/resource"
	"strings"
	"time"
)

type (
	cluster struct {
		config    Config
		nodePool  node.Pool
		agentPool resource.Pool
		clnt      db.Client
	}

	Cluster interface {
		Start(chan error)
		AddConfig(*Config) error
		Stop(chan error)
		GetConfig() Config
		Nodes() node.Pool
	}
)

var Current = New()

func New() Cluster {
	return &cluster{
		DefaultConfig(),
		node.NewPool(),
		resource.NewPool(),
		nil,
	}
}

func initConfig(c *cluster, errChan chan error) {
	clConfig, err := c.clnt.Get(strings.Join([]string{ClmgrKey, "config"}, "/"))
	if err != nil {
		errChan <- err
		return
	}
	if _, ok := clConfig["/cluster/config"]; !ok {
		logger.Info("Didn't found existing config, creating the new one")
		data, err := json.Marshal(c.config)
		if err != nil {
			errChan <- err
			return
		}
		err = c.clnt.Set(strings.Join([]string{ClmgrKey, "config"}, "/"), string(data))
		if err != nil {
			errChan <- err
			return
		}
	} else {
		logger.Info("Got existing cluster config. Setting it to myself")
		data := clConfig["/cluster/config"]
		logger.Info(string(data))
		err := json.Unmarshal(data, &(c.config))
		if err != nil {
			errChan <- err
		}
	}
}

func watchConfig(c *cluster, errChan chan error) {
	watchClusterChan := c.clnt.Watch(strings.Join([]string{ClmgrKey, "config"}, "/"))
	go func() {
		for r := range watchClusterChan {
			logger.Infof("Got key changing %+v", r)
			for _, e := range r.Events {
				if e.IsModify() || e.IsCreate() {
					data := e.Kv.Value
					err := json.Unmarshal(data, &(c.config))
					if err != nil {
						errChan <- err
					}
				}
			}
		}
	}()
}

func updateAlive(errChan chan error) {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			ttlclnt := db.GetTTLClient(30)
			if ttlclnt == nil {
				logger.Error("Can't create TTL client!")
				errChan <- errors.New("can't create TTL client")
			}
			logger.Info("Updating health key")
			ttlclnt.Set(strings.Join([]string{ClmgrKey, "nodes", GetHostname(), IsAliveKey}, "/"), "alive")
			ttlclnt.Close()
		}
	}()
}

func watchNodes(c *cluster, errChan chan error) {
	nodeList, err := c.clnt.Get(strings.Join([]string{ClmgrKey, "nodes"}, "/"))
	if err != nil {
		logger.Error("err: %s", err.Error())
		errChan <- err
	}
	for id, info := range nodeList {
		logger.Infof("Adding %s with info %s", id, string(info))
		n := node.Node{}
		err = json.Unmarshal(info, &n)
		if err != nil {
			logger.Errorf("Unmarshal node error, info: %s", string(info))
			errChan <- err
			continue
		}
		c.nodePool.Add(n, false)
	}

	watchAllNodesChan := c.clnt.Watch(strings.Join([]string{ClmgrKey, "nodes"}, "/"), clientv3.WithPrefix())
	go func() {
		for r := range watchAllNodesChan {
			logger.Infof("Got node changing %+v", r)
			for _, e := range r.Events {
				if e.IsModify() {
					c.nodePool.Change(string(e.Kv.Key), e.Kv.Value)
				} else {
					logger.Info("Cached node creating")
					n := new(node.Node)
					// todo: handle
					_ = json.Unmarshal(e.Kv.Value, n)
					c.nodePool.Add(*n, false)
				}
			}
		}
	}()
}

func (c *cluster) Start(errChan chan error) {
	logger.Info("Starting cluster")

	// creating node only with hostname
	c.nodePool.Add(node.Node{
		Name: GetHostname(),
	}, true)

	c.clnt = db.NewClient()

	// if config already set, not reset it
	initConfig(c, errChan)

	// watching cluster config changes
	watchConfig(c, errChan)

	// setting update node living key
	updateAlive(errChan)

	// watching all node changing
	watchNodes(c, errChan)
}

func (c *cluster) AddConfig(config *Config) error {
	logger.Info("Adding config to cluster")
	if !config.Check() {
		return errors.New("config contains bad value")
	}

	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	err = c.clnt.Set(strings.Join([]string{ClmgrKey, "config"}, "/"), string(data))
	if err != nil {
		return err
	}
	c.config = *config
	return nil
}

func (c *cluster) Stop(errChan chan error) {
	close(errChan)
}

func (c *cluster) GetConfig() Config {
	return c.config
}

func (c *cluster) Nodes() node.Pool {
	return c.nodePool
}
