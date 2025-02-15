// Copyright 2022 SphereEx Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/database-mesh/pisanix/pisa-controller/pkg/kubernetes"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/dynamic"
)

func GetConfig(ctx *gin.Context) {
	namespace := ctx.Param("namespace")
	appname := ctx.Param("appname")
	client := kubernetes.GetClient()
	proxyConfig, err := getConfig(client.Client, namespace, appname)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, err)
		return
	}

	ctx.JSON(http.StatusOK, proxyConfig)
}

func getConfig(client dynamic.Interface, namespace, appname string) (interface{}, error) {
	ctx := context.Background()

	proxyconfig := PisaProxyConfig{Admin: struct {
		Host     string `json:"host,omitempty"`
		Port     uint32 `json:"port,omitempty"`
		LogLevel string `json:"log_level"`
	}(struct {
		Host     string
		Port     uint32
		LogLevel string
	}{LogLevel: "INFO"})}

	virtualdatabases := schema.GroupVersionResource{
		Group:    "core.database-mesh.io",
		Version:  "v1alpha1",
		Resource: "virtualdatabases",
	}
	trafficstrategies := schema.GroupVersionResource{
		Group:    "core.database-mesh.io",
		Version:  "v1alpha1",
		Resource: "trafficstrategies",
	}
	databaseendpoints := schema.GroupVersionResource{
		Group:    "core.database-mesh.io",
		Version:  "v1alpha1",
		Resource: "databaseendpoints",
	}

	vdb, err := client.Resource(virtualdatabases).Namespace(namespace).Get(ctx, appname, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	vdbSpec := &kubernetes.VirtualDatabaseSpec{}
	vdbs, _ := json.Marshal(vdb.Object["spec"])
	if err != nil {
		log.Errorf("%v", err)
		return nil, err
	}
	_ = json.Unmarshal(vdbs, vdbSpec)
	if err != nil {
		log.Errorf("%v", err)
		return nil, err
	}

	for _, service := range vdbSpec.Services {
		ts, err := client.Resource(trafficstrategies).Namespace(namespace).Get(ctx, service.TrafficStrategy, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		tsSpec := &kubernetes.TrafficStrategySpec{}
		tsj, _ := json.Marshal(ts.Object["spec"])
		_ = json.Unmarshal(tsj, tsSpec)
		proxy := Proxy{}
		if service.DatabaseService.DatabaseMySQL != nil {
			proxy.BackendType = "mysql"
			proxy.DB = service.DatabaseService.DatabaseMySQL.DB
			proxy.Name = service.Name
			proxy.User = service.DatabaseService.DatabaseMySQL.User
			proxy.Password = service.DatabaseService.DatabaseMySQL.Password
			proxy.PoolSize = service.DatabaseService.DatabaseMySQL.PoolSize
			if service.DatabaseMySQL.Host == "" {
				service.DatabaseMySQL.Host = "0.0.0.0"
			}
			if service.DatabaseMySQL.Port == 0 {
				service.DatabaseMySQL.Port = 3306
			}
			proxy.ListenAddr = fmt.Sprintf("%s:%d", service.DatabaseService.DatabaseMySQL.Host, service.DatabaseService.DatabaseMySQL.Port)
			proxy.ServerVersion = service.DatabaseService.DatabaseMySQL.ServerVersion
			if tsSpec.LoadBalance.SimpleLoadBalance != nil {
				proxy.SimpleLoadBalance.BalancerType = tsSpec.LoadBalance.SimpleLoadBalance.Kind
			}
			if len(tsSpec.CircuitBreaks) != 0 {
				proxy.Plugin.CircuitBreaks = tsSpec.CircuitBreaks
			}
			if len(tsSpec.ConcurrencyControls) != 0 {
				for _, control := range tsSpec.ConcurrencyControls {
					// TODO: Convert CRD to configuration file json format.Need a better implementation
					// Ref: https://stackoverflow.com/questions/24613271/golang-is-conversion-between-different-struct-types-possible
					proxy.Plugin.ConcurrencyControls = append(proxy.Plugin.ConcurrencyControls, *(*ConcurrencyControl)(&control))
				}
			}
		}
		dbes, err := client.Resource(databaseendpoints).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.FormatLabels(tsSpec.Selector.MatchLabels)})
		if err != nil {
			log.Errorf("%v", err)
			return nil, err
		}
		for _, dbe := range dbes.Items {
			dbeSpec := &kubernetes.DatabaseEndpointSpec{}
			dbej, _ := json.Marshal(dbe.Object["spec"])
			_ = json.Unmarshal(dbej, dbeSpec)
			if dbeSpec.Database.MySQL != nil {
				proxyconfig.Mysql.Nodes = append(proxyconfig.Mysql.Nodes, Node{
					Name:     dbe.GetName(),
					Db:       dbeSpec.Database.MySQL.DB,
					User:     dbeSpec.Database.MySQL.User,
					Password: dbeSpec.Database.MySQL.Password,
					Host:     dbeSpec.Database.MySQL.Host,
					Port:     dbeSpec.Database.MySQL.Port,
					Weight:   1,
				})
			}
			if tsSpec.LoadBalance.SimpleLoadBalance != nil {
				proxy.SimpleLoadBalance.Nodes = append(proxy.SimpleLoadBalance.Nodes, dbe.GetName())
			}
		}
		proxyconfig.Proxy.Configs = append(proxyconfig.Proxy.Configs, proxy)
	}
	return proxyconfig, nil
}
