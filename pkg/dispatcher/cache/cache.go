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

package cache

import (
	"fmt"
	"sync"

	workv1alpha2 "github.com/karmada-io/karmada/pkg/apis/work/v1alpha2"
	karmadaclientset "github.com/karmada-io/karmada/pkg/generated/clientset/versioned"
	karmadainformerfactory "github.com/karmada-io/karmada/pkg/generated/informers/externalversions"
	informerworkv1aplha2 "github.com/karmada-io/karmada/pkg/generated/informers/externalversions/work/v1alpha2"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	schedv1 "k8s.io/client-go/informers/scheduling/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	schedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	volcanoclientset "volcano.sh/apis/pkg/client/clientset/versioned"
	volcanoinformer "volcano.sh/apis/pkg/client/informers/externalversions"
	volcanoinformerfactory "volcano.sh/apis/pkg/client/informers/externalversions"
	schedulinginformer "volcano.sh/apis/pkg/client/informers/externalversions/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/kube"
	schedulingapi "volcano.sh/volcano/pkg/scheduler/api"

	"volcano.sh/volcano-global/pkg/dispatcher/api"
	"volcano.sh/volcano-global/pkg/dispatcher/cache/utils"
)

type DispatcherCacheOption struct {
	DefaultQueueName  string
	KubeClientOptions kube.ClientOptions
}

type DispatcherCache struct {
	mutex sync.Mutex

	kubeClient    kubernetes.Interface
	vcClient      volcanoclientset.Interface
	karmadaClient karmadaclientset.Interface

	informerFactory        informers.SharedInformerFactory
	volcanoInformerFactory volcanoinformer.SharedInformerFactory
	karmadaInformerFactor  karmadainformerfactory.SharedInformerFactory

	queueInformer schedulinginformer.QueueInformer
	queues        map[string]*schedulingapi.QueueInfo
	defaultQueue  string

	podGroupInformer schedulinginformer.PodGroupInformer
	// podGroups[namespace][name] = target RodGroup.
	podGroups map[string]map[string]*schedulingv1beta1.PodGroup

	priorityClassInformer schedv1.PriorityClassInformer
	priorityClasses       map[string]*schedulingv1.PriorityClass
	defaultPriorityClass  *schedulingv1.PriorityClass

	resourceBindingInformer informerworkv1aplha2.ResourceBindingInformer
	// resourceBindings[namespace][name] = target ResourceBinding.
	resourceBindings map[string]map[string]*workv1alpha2.ResourceBinding

	// The infos only save basic information like RB, ResourceUID, Status in the cache, the PodGroup,
	// Queue, and Priority will update when Snapshot.
	// resourceBindingInfos[namespace][name] = target ResourceBindingInfo.
	resourceBindingInfos map[string]map[string]*api.ResourceBindingInfo

	// Its queue for unsuspend the ResourceBinding, when a ResourceBinding finish dispatch,
	// The Dispatcher will add a task to here, and update the ResourceBinding.spec.Suspend = false.
	unSuspendRBTaskQueue workqueue.Interface
}

func NewDispatcherCache(option *DispatcherCacheOption) DispatcherCacheInterface {
	config, err := kube.BuildConfig(option.KubeClientOptions)
	if err != nil {
		panic(fmt.Sprintf("failed to build kubeConfig, with err: %v", err))
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("failed to init kubeClient, with err: %v", err))
	}
	volcanoClient, err := volcanoclientset.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("failed to init vcClient, with err: %v", err))
	}
	karmadaClient, err := karmadaclientset.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("failed to init karmadaClient, with err: %v", err))
	}

	// Create the default queue
	utils.CreateDefaultQueue(volcanoClient, option.DefaultQueueName)

	sc := &DispatcherCache{
		kubeClient:    kubeClient,
		vcClient:      volcanoClient,
		karmadaClient: karmadaClient,

		informerFactory:        informers.NewSharedInformerFactory(kubeClient, 0),
		volcanoInformerFactory: volcanoinformerfactory.NewSharedInformerFactory(volcanoClient, 0),
		karmadaInformerFactor:  karmadainformerfactory.NewSharedInformerFactory(karmadaClient, 0),

		queues:           map[string]*schedulingapi.QueueInfo{},
		defaultQueue:     option.DefaultQueueName,
		podGroups:        map[string]map[string]*schedulingv1beta1.PodGroup{},
		priorityClasses:  map[string]*schedulingv1.PriorityClass{},
		resourceBindings: map[string]map[string]*workv1alpha2.ResourceBinding{},

		resourceBindingInfos: map[string]map[string]*api.ResourceBindingInfo{},

		unSuspendRBTaskQueue: workqueue.New(),
	}

	sc.queueInformer = sc.volcanoInformerFactory.Scheduling().V1beta1().Queues()
	sc.queueInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    sc.addQueue,
		UpdateFunc: sc.updateQueue,
		DeleteFunc: sc.deleteQueue,
	})

	sc.podGroupInformer = sc.volcanoInformerFactory.Scheduling().V1beta1().PodGroups()
	sc.podGroupInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    sc.addPodGroup,
		UpdateFunc: sc.updatePodGroup,
		DeleteFunc: sc.deletePodGroup,
	})

	sc.priorityClassInformer = sc.informerFactory.Scheduling().V1().PriorityClasses()
	sc.priorityClassInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    sc.addPriorityClass,
		UpdateFunc: sc.updatePriorityClass,
		DeleteFunc: sc.deletePriorityClass,
	})

	sc.resourceBindingInformer = sc.karmadaInformerFactor.Work().V1alpha2().ResourceBindings()
	sc.resourceBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    sc.addResourceBinding,
		UpdateFunc: sc.updateResourceBinding,
		DeleteFunc: sc.deleteResourceBinding,
	})

	return sc
}

func (dc *DispatcherCache) Run(stopCh <-chan struct{}) {
	// Start the factories, and wait for cache sync
	dc.informerFactory.Start(stopCh)
	dc.volcanoInformerFactory.Start(stopCh)
	dc.karmadaInformerFactor.Start(stopCh)
	for informerType, ok := range dc.informerFactory.WaitForCacheSync(stopCh) {
		if !ok {
			klog.Errorf("Caches failed to sync: %v", informerType)
		}
	}
	for informerType, ok := range dc.volcanoInformerFactory.WaitForCacheSync(stopCh) {
		if !ok {
			klog.Errorf("Caches failed to sync: %v", informerType)
		}
	}
	for informerType, ok := range dc.karmadaInformerFactor.WaitForCacheSync(stopCh) {
		if !ok {
			klog.Errorf("Caches failed to sync: %v", informerType)
		}
	}

	go wait.Until(dc.unSuspendResourceBindingTaskWorker, 0, stopCh)

	klog.V(2).Infof("DispatcherCache completes initialization and start to run.")
}
