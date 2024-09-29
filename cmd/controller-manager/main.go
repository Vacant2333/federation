/*
Copyright 2024 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/pflag"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	"volcano.sh/volcano/cmd/controller-manager/app"
	"volcano.sh/volcano/cmd/controller-manager/app/options"
	"volcano.sh/volcano/pkg/version"

	_ "volcano.sh/volcano/pkg/controllers/garbagecollector"
	_ "volcano.sh/volcano/pkg/controllers/job"
	_ "volcano.sh/volcano/pkg/controllers/podgroup"
	_ "volcano.sh/volcano/pkg/controllers/queue"

	_ "volcano.sh/volcano-global/pkg/controllers/deployment"
	_ "volcano.sh/volcano-global/pkg/dispatcher"
)

//const componentName = "volcano-global-controller-manager"

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	klog.InitFlags(nil)

	serverOption := options.NewServerOption()
	serverOption.AddFlags(pflag.CommandLine)

	// todo: enable this when volcano release new version.
	//util.LeaderElectionDefault(&serverOption.LeaderElection)
	//serverOption.LeaderElection.ResourceName = componentName
	//componentbaseoptions.BindLeaderElectionFlags(&serverOption.LeaderElection, fs)

	cliflag.InitFlags()

	if serverOption.PrintVersion {
		version.PrintVersionAndExit()
	}
	if err := serverOption.CheckOptionOrDie(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if serverOption.CaCertFile != "" && serverOption.CertFile != "" && serverOption.KeyFile != "" {
		if err := serverOption.ParseCAFiles(nil); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse CA files: %v\n", err)
			os.Exit(1)
		}
	}

	klog.StartFlushDaemon(5 * time.Second)
	defer klog.Flush()

	if err := app.Run(serverOption); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
